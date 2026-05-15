package webhooks

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nats-io/nats.go/jetstream"
)

// Dispatcher subscribes to the broad event streams (ticket.>, message.>,
// sla.>) and fans matched events to per-subscription delivery jobs on
// the WEBHOOKS work-queue stream. Pairs with a Retry worker (also in
// this file) that picks up Transient failures and re-publishes them
// with a delayed delivery using the BackoffFor schedule.
//
// Decryption: subscription secrets are stored ciphertext on
// webhook_subscriptions. Production wiring fetches the per-tenant DEK
// via a Vault transit secret. For Phase-1 single-VPS we accept a
// SecretDecoder interface so tests pass plaintext through directly.
type Dispatcher struct {
	JS        jetstream.JetStream
	Pool      *pgxpool.Pool
	Deliverer *Deliverer
	Decoder   SecretDecoder
}

// SecretDecoder turns the ciphertext + DEK ID stored on
// webhook_subscriptions into the plaintext HMAC secret. The Vault
// implementation lives outside this package; tests supply a stub.
type SecretDecoder interface {
	Decode(ctx context.Context, ciphertext []byte, dekID string) (string, error)
}

// PlaintextDecoder is the dev/test implementation. Treats the
// ciphertext column as already-plaintext bytes -- safe for tests and
// for the in-process bootstrap case where Vault isn't wired yet.
type PlaintextDecoder struct{}

// Decode returns the bytes verbatim as a string.
func (PlaintextDecoder) Decode(_ context.Context, ct []byte, _ string) (string, error) {
	return string(ct), nil
}

// JobSubject is the work-queue subject we publish onto. The Retry
// worker subscribes here too -- a retry is just a re-publish with the
// `attempt` header bumped.
const JobSubject = "webhooks.deliver"

// HeaderAttempt + HeaderSubID + HeaderEventID + HeaderEventSubject
// ride on every JetStream message in the work queue so the worker
// doesn't have to re-derive them from the body.
const (
	HeaderAttempt      = "X-Attempt"
	HeaderSubID        = "X-Sub-Id"
	HeaderEventID      = "X-Event-Id"
	HeaderEventSubject = "X-Event-Subject"
)

// FanOut consumes one normalised platform event (e.g. published to
// `ticket.assigned`) and queues one delivery job per matching active
// subscription. Idempotency is handled by the per-subscription
// Nats-Msg-Id we set: "<sub_id>:<event_id>:<attempt>".
//
// Run as a JetStream durable consumer over `ticket.> message.> sla.>`
// (whichever subjects this tenant cares about) -- one Dispatcher per
// gateway process; the work-queue semantics on WEBHOOKS guarantee one
// worker picks up each delivery job.
func (d *Dispatcher) FanOut(ctx context.Context, tenantID uuid.UUID, subject string, eventID string, body []byte, ts time.Time) error {
	subs, err := d.activeSubscriptions(ctx, tenantID, subject)
	if err != nil {
		return err
	}
	for _, s := range subs {
		if err := d.queue(ctx, s.ID, subject, eventID, body, ts, 1); err != nil {
			slog.WarnContext(ctx, "webhooks: enqueue",
				slog.String("err", err.Error()),
				slog.String("sub_id", s.ID.String()))
		}
	}
	return nil
}

// SubscriptionRow is the DB row reader. Exposed for the worker too.
type SubscriptionRow struct {
	ID       uuid.UUID
	URL      string
	Subjects []string
	// Decrypted secret -- only populated by loadSubscription, never
	// stored on this struct longer than the call.
	Secret string
}

// activeSubscriptions returns the active rows whose subject patterns
// match `subject`.
func (d *Dispatcher) activeSubscriptions(ctx context.Context, tenantID uuid.UUID, subject string) ([]SubscriptionRow, error) {
	rows, err := d.Pool.Query(ctx, `
		SELECT id, url, subjects
		FROM webhook_subscriptions
		WHERE tenant_id = $1 AND active = TRUE`, tenantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []SubscriptionRow{}
	for rows.Next() {
		var s SubscriptionRow
		if err := rows.Scan(&s.ID, &s.URL, &s.Subjects); err != nil {
			return nil, err
		}
		if MatchAny(s.Subjects, subject) {
			out = append(out, s)
		}
	}
	return out, rows.Err()
}

// queue publishes one delivery job onto WEBHOOKS.
func (d *Dispatcher) queue(ctx context.Context, subID uuid.UUID, subject, eventID string, body []byte, ts time.Time, attempt int) error {
	if d.JS == nil {
		return errors.New("webhooks: jetstream not configured")
	}
	job := struct {
		SubID    uuid.UUID `json:"sub_id"`
		EventID  string    `json:"event_id"`
		Subject  string    `json:"subject"`
		Body     []byte    `json:"body"`
		Ts       time.Time `json:"ts"`
		Attempt  int       `json:"attempt"`
	}{subID, eventID, subject, body, ts, attempt}
	enc, err := json.Marshal(job)
	if err != nil {
		return err
	}
	msgID := fmt.Sprintf("%s:%s:%d", subID, eventID, attempt)
	_, err = d.JS.Publish(ctx, JobSubject, enc,
		jetstream.WithMsgID(msgID))
	return err
}

// Worker pulls jobs off WEBHOOKS, loads the (decrypted) subscription
// secret, attempts one Deliverer.Deliver, and either acks (delivered
// or permanent), nacks for redelivery (if JetStream still has retries
// budget on the message), or re-queues the same job with attempt+1
// after BackoffFor(attempt+1).
type Worker struct {
	JS        jetstream.JetStream
	Pool      *pgxpool.Pool
	Deliverer *Deliverer
	Decoder   SecretDecoder
}

// Run binds the durable WEBHOOKS consumer and pumps jobs.
func (w *Worker) Run(ctx context.Context) error {
	cons, err := w.JS.CreateOrUpdateConsumer(ctx, "WEBHOOKS", jetstream.ConsumerConfig{
		Durable:        "webhooks-worker",
		FilterSubject:  JobSubject,
		AckPolicy:      jetstream.AckExplicitPolicy,
		MaxAckPending:  32,
		AckWait:        DeliveryTimeout * 3,
		MaxDeliver:     1, // we handle retries by re-publishing, not by JetStream redelivery
		DeliverPolicy:  jetstream.DeliverAllPolicy,
	})
	if err != nil {
		return fmt.Errorf("webhooks: worker consumer: %w", err)
	}
	iter, err := cons.Messages(jetstream.PullMaxMessages(8))
	if err != nil {
		return fmt.Errorf("webhooks: worker iter: %w", err)
	}
	defer iter.Stop()

	// iter.Next() blocks; ctx.Done() can't unblock it. Spawn a watcher
	// that closes the iterator when ctx cancels so Run() returns.
	go func() {
		<-ctx.Done()
		iter.Stop()
	}()

	for {
		msg, err := iter.Next()
		if errors.Is(err, jetstream.ErrMsgIteratorClosed) {
			return nil
		}
		if err != nil {
			slog.WarnContext(ctx, "webhooks: iter next",
				slog.String("err", err.Error()))
			continue
		}
		w.handle(ctx, msg)
	}
}

// jobPayload mirrors Dispatcher.queue's anonymous struct.
type jobPayload struct {
	SubID   uuid.UUID `json:"sub_id"`
	EventID string    `json:"event_id"`
	Subject string    `json:"subject"`
	Body    []byte    `json:"body"`
	Ts      time.Time `json:"ts"`
	Attempt int       `json:"attempt"`
}

func (w *Worker) handle(ctx context.Context, msg jetstream.Msg) {
	var j jobPayload
	if err := json.Unmarshal(msg.Data(), &j); err != nil {
		slog.ErrorContext(ctx, "webhooks: bad job, terminating",
			slog.String("err", err.Error()))
		_ = msg.Term()
		return
	}

	sub, ok, err := w.loadSubscription(ctx, j.SubID)
	if err != nil {
		slog.WarnContext(ctx, "webhooks: load sub",
			slog.String("err", err.Error()),
			slog.String("sub_id", j.SubID.String()))
		_ = msg.Nak()
		return
	}
	if !ok {
		// Subscription gone or disabled -- no point retrying.
		_ = msg.Term()
		return
	}

	out, err := w.Deliverer.Deliver(ctx, sub, Event{
		ID: j.EventID, Subject: j.Subject, Ts: j.Ts, Body: j.Body,
	}, j.Attempt)
	if err != nil {
		slog.WarnContext(ctx, "webhooks: deliver",
			slog.String("err", err.Error()))
		_ = msg.Nak()
		return
	}

	switch out.Class {
	case ClassifyDelivered, ClassifyPermanent:
		_ = msg.Ack()
	case ClassifyTransient:
		// Re-queue with attempt+1 if we have budget.
		next := j.Attempt + 1
		if next > MaxAttempts {
			slog.WarnContext(ctx, "webhooks: max attempts exhausted",
				slog.String("sub_id", j.SubID.String()),
				slog.String("event_id", j.EventID))
			_ = msg.Ack() // give up; the per-attempt rows in
			// webhook_deliveries are the audit trail.
			return
		}
		// Re-publish with the backoff -- AckWait would otherwise
		// time out before our backoff elapses for high attempt
		// numbers, so the cleanest approach is to ack *this*
		// message and queue a fresh one, with the JetStream-side
		// dedupe key including the attempt number so we don't loop.
		go w.requeueAfter(j, next)
		_ = msg.Ack()
	}
}

// requeueAfter sleeps the backoff window then re-publishes. Runs in
// its own goroutine so the worker keeps draining the queue.
func (w *Worker) requeueAfter(j jobPayload, nextAttempt int) {
	wait := BackoffFor(nextAttempt)
	t := time.NewTimer(wait)
	defer t.Stop()
	<-t.C
	ctx, cancel := context.WithTimeout(context.Background(), DeliveryTimeout*2)
	defer cancel()

	job := j
	job.Attempt = nextAttempt
	enc, _ := json.Marshal(job)
	msgID := fmt.Sprintf("%s:%s:%d", j.SubID, j.EventID, nextAttempt)
	if _, err := w.JS.Publish(ctx, JobSubject, enc,
		jetstream.WithMsgID(msgID)); err != nil {
		slog.Warn("webhooks: requeue",
			slog.String("err", err.Error()),
			slog.String("sub_id", j.SubID.String()),
			slog.String("attempt", strconv.Itoa(nextAttempt)))
	}
}

// loadSubscription returns the row + decrypted secret. Returns
// (zero, false, nil) when the subscription no longer exists or has
// been disabled.
func (w *Worker) loadSubscription(ctx context.Context, id uuid.UUID) (Subscription, bool, error) {
	var (
		s        Subscription
		secretCT []byte
		dekID    string
	)
	err := w.Pool.QueryRow(ctx, `
		SELECT id, tenant_id, url, secret_ct, dek_id, subjects, active
		FROM webhook_subscriptions
		WHERE id = $1`, id,
	).Scan(&s.ID, &s.TenantID, &s.URL, &secretCT, &dekID, &s.Subjects, &s.Active)
	if errors.Is(err, pgx.ErrNoRows) {
		return Subscription{}, false, nil
	}
	if err != nil {
		return Subscription{}, false, err
	}
	if !s.Active {
		return Subscription{}, false, nil
	}
	plain, err := w.Decoder.Decode(ctx, secretCT, dekID)
	if err != nil {
		return Subscription{}, false, fmt.Errorf("webhooks: decode secret: %w", err)
	}
	s.Secret = plain
	return s, true, nil
}
