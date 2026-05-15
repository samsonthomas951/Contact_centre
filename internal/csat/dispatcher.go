package csat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nats-io/nats.go/jetstream"
)

// Dispatcher subscribes to ticket.state_change events on JetStream;
// when one signals new_state=resolved, it creates a survey and
// queues an outbound message to the customer over the channel they
// used. The outbound is a connector-specific job published onto
// `outbound.<channel>.text` so the existing connector workers (FB
// Sender, X v2, WA /messages, IG Send API, widget WS, voice SMS)
// pick it up without bespoke CSAT plumbing.
//
// One Dispatcher per gateway process. The state-change subject is the
// same one routing.Engine already publishes to from ChangeState; we
// piggy-back on that rather than introducing a second event.
type Dispatcher struct {
	JS       jetstream.JetStream
	Pool     *pgxpool.Pool
	Repo     *Repo
	// LinkBase is the public host the survey link is built from, e.g.
	// "https://app.example.co.ke/csat". The token is appended.
	LinkBase string
	// Now is overridable for tests.
	Now func() time.Time
}

// NewDispatcher constructs one. Operator wires LinkBase + JS + Pool +
// Repo from the gateway's run().
func NewDispatcher(js jetstream.JetStream, pool *pgxpool.Pool, repo *Repo, linkBase string) *Dispatcher {
	return &Dispatcher{
		JS: js, Pool: pool, Repo: repo,
		LinkBase: strings.TrimRight(linkBase, "/"),
		Now:      time.Now,
	}
}

// StateChangeEvent is the relevant subset of the ticket.state_change
// payload. The ticket service publishes the full event; here we only
// care about the transition shape.
type StateChangeEvent struct {
	TicketID uuid.UUID `json:"ticket_id"`
	TenantID uuid.UUID `json:"tenant_id"`
	From     string    `json:"from"`
	To       string    `json:"to"`
}

// Run binds a durable consumer for ticket.state_change events.
// Idempotent: if a survey already exists for this ticket, we don't
// create a second one (a flapping ticket that goes resolved -> open
// -> resolved should still only generate one survey).
func (d *Dispatcher) Run(ctx context.Context) error {
	cons, err := d.JS.CreateOrUpdateConsumer(ctx, "INGRESS", jetstream.ConsumerConfig{
		Durable:        "csat-dispatcher",
		FilterSubject:  "ticket.state_change",
		AckPolicy:      jetstream.AckExplicitPolicy,
		MaxAckPending:  16,
		AckWait:        20 * time.Second,
		MaxDeliver:     5,
		DeliverPolicy:  jetstream.DeliverNewPolicy,
	})
	if err != nil {
		return fmt.Errorf("csat: dispatcher consumer: %w", err)
	}
	iter, err := cons.Messages(jetstream.PullMaxMessages(8))
	if err != nil {
		return fmt.Errorf("csat: dispatcher iter: %w", err)
	}
	defer iter.Stop()

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
			slog.WarnContext(ctx, "csat: iter next",
				slog.String("err", err.Error()))
			continue
		}
		d.handle(ctx, msg)
	}
}

func (d *Dispatcher) handle(ctx context.Context, msg jetstream.Msg) {
	var ev StateChangeEvent
	if err := json.Unmarshal(msg.Data(), &ev); err != nil {
		slog.ErrorContext(ctx, "csat: malformed state_change, terminating",
			slog.String("err", err.Error()))
		_ = msg.Term()
		return
	}
	if ev.To != "resolved" {
		_ = msg.Ack()
		return
	}
	if err := d.OnResolved(ctx, ev.TenantID, ev.TicketID); err != nil {
		slog.WarnContext(ctx, "csat: dispatch failed, will redeliver",
			slog.String("err", err.Error()),
			slog.String("ticket_id", ev.TicketID.String()))
		_ = msg.Nak()
		return
	}
	_ = msg.Ack()
}

// OnResolved is the pure entry point: takes a tenant + ticket and
// (a) creates a survey if none exists, (b) queues an outbound job on
// the channel the customer used. Idempotent on (b) too via a Nats-Msg-Id
// keyed on the survey id.
func (d *Dispatcher) OnResolved(ctx context.Context, tenantID, ticketID uuid.UUID) error {
	customerID, channel, ok, err := d.lookupTicket(ctx, tenantID, ticketID)
	if err != nil {
		return err
	}
	if !ok {
		// Ticket gone (rare race) -- ack and move on.
		return nil
	}

	// Guard: don't re-create. A ticket that re-resolves (after being
	// reopened) only gets one survey because the agent's quality is
	// what we measure on the first close.
	already, err := d.surveyExists(ctx, tenantID, ticketID)
	if err != nil {
		return err
	}
	if already {
		return nil
	}

	survey, token, err := d.Repo.Create(ctx, CreateParams{
		TenantID: tenantID, TicketID: ticketID, CustomerID: customerID,
		Channel: channel,
	})
	if err != nil {
		return err
	}

	link := fmt.Sprintf("%s/%s", d.LinkBase, token)
	body := fmt.Sprintf("Thanks for reaching out — could you rate how we did? %s", link)

	return d.queueOutbound(ctx, tenantID, ticketID, channel, customerID, body, survey.ID)
}

// lookupTicket returns (customer_id, channel, true) or (_, _, false)
// when the ticket isn't found.
func (d *Dispatcher) lookupTicket(ctx context.Context, tenantID, ticketID uuid.UUID) (uuid.UUID, string, bool, error) {
	var (
		customerID uuid.UUID
		channel    string
	)
	err := d.Pool.QueryRow(ctx, `
		SELECT c.customer_id, c.channel
		FROM tickets t
		JOIN conversations c ON c.id = t.conversation_id
		WHERE t.tenant_id = $1 AND t.id = $2`,
		tenantID, ticketID).Scan(&customerID, &channel)
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, "", false, nil
	}
	if err != nil {
		return uuid.Nil, "", false, err
	}
	return customerID, channel, true, nil
}

// surveyExists tells us whether this ticket already has a survey row.
func (d *Dispatcher) surveyExists(ctx context.Context, tenantID, ticketID uuid.UUID) (bool, error) {
	var exists bool
	err := d.Pool.QueryRow(ctx, `
		SELECT EXISTS(
		  SELECT 1 FROM csat_surveys
		  WHERE tenant_id = $1 AND ticket_id = $2 AND revoked_at IS NULL
		)`, tenantID, ticketID).Scan(&exists)
	return exists, err
}

// queueOutbound publishes one outbound text job onto JetStream. The
// per-connector workers (FB Sender, X v2, WA /messages, etc.) read
// from outbound.<channel>.text and ship.
func (d *Dispatcher) queueOutbound(ctx context.Context, tenantID, ticketID uuid.UUID,
	channel string, customerID uuid.UUID, body string, surveyID uuid.UUID,
) error {
	if d.JS == nil {
		return errors.New("csat: jetstream not configured")
	}
	job := struct {
		TenantID   uuid.UUID `json:"tenant_id"`
		TicketID   uuid.UUID `json:"ticket_id"`
		CustomerID uuid.UUID `json:"customer_id"`
		Channel    string    `json:"channel"`
		Body       string    `json:"body"`
		Kind       string    `json:"kind"` // "csat_survey"
	}{tenantID, ticketID, customerID, channel, body, "csat_survey"}
	enc, err := json.Marshal(job)
	if err != nil {
		return err
	}
	subject := fmt.Sprintf("outbound.%s.text", channel)
	// Nats-Msg-Id keyed on survey id so a redelivery doesn't queue a
	// second outbound.
	_, err = d.JS.Publish(ctx, subject, enc,
		jetstream.WithMsgID("csat:"+surveyID.String()))
	return err
}
