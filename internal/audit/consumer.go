package audit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

// StreamName is the NATS JetStream stream that buffers audit events.
const StreamName = "AUDIT"

// SubjectAll matches every event published by every service.
const SubjectAll = "audit.events.>"

// EnsureStream creates the AUDIT stream if it does not exist. Retention
// is interest-based with a 7-day max age — the writer is the only
// consumer, so once it acks an event the message can be discarded.
func EnsureStream(ctx context.Context, js jetstream.JetStream) error {
	_, err := js.CreateOrUpdateStream(ctx, jetstream.StreamConfig{
		Name:        StreamName,
		Subjects:    []string{"audit.events.>"},
		Retention:   jetstream.InterestPolicy,
		Storage:     jetstream.FileStorage,
		MaxAge:      7 * 24 * time.Hour,
		Discard:     jetstream.DiscardOld,
		Duplicates:  5 * time.Minute, // dedupe by Nats-Msg-Id
		Description: "Append-only audit ledger feed",
	})
	return err
}

// Consumer drains the AUDIT stream into the ledger Writer. Messages are
// acked only after a successful Append; transient failures fall back to
// JetStream's redelivery policy. Permanent failures (validation errors)
// are TermAcked so they do not block the stream.
type Consumer struct {
	w  *Writer
	js jetstream.JetStream
}

// NewConsumer constructs a Consumer.
func NewConsumer(w *Writer, js jetstream.JetStream) *Consumer {
	return &Consumer{w: w, js: js}
}

// Run binds a durable pull consumer and processes events until ctx is
// cancelled. Returns the first non-recoverable error.
func (c *Consumer) Run(ctx context.Context) error {
	cons, err := c.js.CreateOrUpdateConsumer(ctx, StreamName, jetstream.ConsumerConfig{
		Durable:        "audit-writer",
		AckPolicy:      jetstream.AckExplicitPolicy,
		FilterSubject:  SubjectAll,
		MaxAckPending:  1, // serialise — chain integrity demands it
		AckWait:        30 * time.Second,
		MaxDeliver:     5,
		DeliverPolicy:  jetstream.DeliverAllPolicy,
		ReplayPolicy:   jetstream.ReplayInstantPolicy,
	})
	if err != nil {
		return fmt.Errorf("audit: consumer: %w", err)
	}

	iter, err := cons.Messages(jetstream.PullMaxMessages(1))
	if err != nil {
		return fmt.Errorf("audit: messages iter: %w", err)
	}
	defer iter.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		default:
		}

		msg, err := iter.Next()
		switch {
		case errors.Is(err, jetstream.ErrMsgIteratorClosed):
			return nil
		case err != nil:
			slog.WarnContext(ctx, "audit: iterator next", slog.String("err", err.Error()))
			continue
		}

		c.handle(ctx, msg)
	}
}

func (c *Consumer) handle(ctx context.Context, msg jetstream.Msg) {
	var e Event
	if err := json.Unmarshal(msg.Data(), &e); err != nil {
		slog.ErrorContext(ctx, "audit: malformed event, terminating",
			slog.String("err", err.Error()),
			slog.String("subject", msg.Subject()))
		_ = msg.Term()
		return
	}

	if err := c.w.Append(ctx, &e); err != nil {
		// Validation errors are permanent — Term so we don't loop.
		if isValidation(err) {
			slog.ErrorContext(ctx, "audit: validation error, terminating",
				slog.String("err", err.Error()))
			_ = msg.Term()
			return
		}
		slog.WarnContext(ctx, "audit: append failed, will redeliver",
			slog.String("err", err.Error()))
		_ = msg.Nak()
		return
	}
	_ = msg.Ack()
}

func isValidation(err error) bool {
	// Validation errors from event.Validate() are plain fmt.Errorf
	// values; treat the well-known prefixes as permanent so JetStream
	// doesn't redeliver poison messages forever.
	if err == nil {
		return false
	}
	s := err.Error()
	for _, sub := range []string{
		"audit: invalid",
		"audit: tenant_id required",
		"audit: action required",
		"audit: correlation_id required",
	} {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

// Publish is the producer-side helper every other service uses. It sets
// a Nats-Msg-Id header derived from the event's correlation + action so
// JetStream's 5-minute dedupe window catches accidental double-publish.
func Publish(ctx context.Context, js jetstream.JetStream, e *Event) error {
	if err := e.Validate(); err != nil {
		return err
	}
	body, err := json.Marshal(e)
	if err != nil {
		return fmt.Errorf("audit: marshal: %w", err)
	}
	subject := fmt.Sprintf("audit.events.%s", e.Action)
	hdr := nats.Header{}
	hdr.Set(jetstream.MsgIDHeader, fmt.Sprintf("%s:%s", e.CorrelationID, e.Action))
	_, err = js.PublishMsg(ctx, &nats.Msg{Subject: subject, Header: hdr, Data: body})
	return err
}
