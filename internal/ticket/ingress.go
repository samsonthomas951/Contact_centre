package ticket

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
	"github.com/nats-io/nats.go/jetstream"
)

// Ingress consumer: subscribes to ingress.> on the INGRESS JetStream
// stream, turns each normalised connector event into rows on
// customers + conversations + tickets + messages.
//
// Idempotency: the subject + Nats-Msg-Id chain on JetStream gives us
// at-most-once-per-message-id semantics on the consumer side; we pair
// that with a per-conversation upsert so a duplicate inbound message
// from the same `platform_msg_id` is a no-op.

// IngressEvent is the cross-channel shape every connector publishes.
// Connectors use channel-specific structs (facebook.InboundMessage,
// whatsapp.InboundMessage, etc.); this package is interested only in
// the union subset needed to upsert state.
type IngressEvent struct {
	TenantID         string    `json:"tenant_id"`
	Channel          string    `json:"channel"`
	Kind             string    `json:"kind"`              // message | dm | postback | comment | mention | identified | ...
	CustomerExternal string    `json:"customer_external"` // platform user id (PSID, X user id, wa_id, ig user id, visitor id)
	CustomerHandle   string    `json:"customer_handle,omitempty"`
	CustomerName     string    `json:"customer_name,omitempty"`
	CustomerEmail    string    `json:"customer_email,omitempty"`
	CustomerPhone    string    `json:"customer_phone,omitempty"`
	ConversationKey  string    `json:"conversation_key"`  // per-channel thread id
	PlatformMsgID    string    `json:"platform_msg_id,omitempty"`
	Body             string    `json:"body,omitempty"`
	OccurredAt       time.Time `json:"occurred_at"`
}

// Consumer drives one durable JetStream consumer for ingress.> and
// dispatches each message through Handle(). It's a separate type from
// Repo so testing the dispatcher logic doesn't need a NATS fixture --
// Handle() takes a parsed event and a Repo.
type Consumer struct {
	JS   jetstream.JetStream
	Repo *Repo
}

// NewConsumer constructs the Consumer.
func NewConsumer(js jetstream.JetStream, r *Repo) *Consumer {
	return &Consumer{JS: js, Repo: r}
}

// Run binds the durable consumer and pumps messages until ctx cancels.
// MaxAckPending is generous because dispatch is per-conversation
// idempotent; we don't need strict ordering across the whole stream.
func (c *Consumer) Run(ctx context.Context) error {
	cons, err := c.JS.CreateOrUpdateConsumer(ctx, "INGRESS", jetstream.ConsumerConfig{
		Durable:        "ticket-ingress",
		FilterSubject:  "ingress.>",
		AckPolicy:      jetstream.AckExplicitPolicy,
		MaxAckPending:  64,
		AckWait:        30 * time.Second,
		MaxDeliver:     5,
		DeliverPolicy:  jetstream.DeliverAllPolicy,
	})
	if err != nil {
		return fmt.Errorf("ticket: ingress consumer: %w", err)
	}

	iter, err := cons.Messages(jetstream.PullMaxMessages(16))
	if err != nil {
		return fmt.Errorf("ticket: ingress messages: %w", err)
	}
	defer iter.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		default:
		}
		msg, err := iter.Next()
		if errors.Is(err, jetstream.ErrMsgIteratorClosed) {
			return nil
		}
		if err != nil {
			slog.WarnContext(ctx, "ticket: iter next", slog.String("err", err.Error()))
			continue
		}
		c.handle(ctx, msg)
	}
}

// handle parses one JetStream message, dispatches to the repo. Bad
// JSON is Term'd (poison message); transient DB errors are Nak'd for
// JetStream redelivery.
func (c *Consumer) handle(ctx context.Context, msg jetstream.Msg) {
	var e IngressEvent
	if err := json.Unmarshal(msg.Data(), &e); err != nil {
		slog.ErrorContext(ctx, "ticket: ingress: malformed event, terminating",
			slog.String("err", err.Error()),
			slog.String("subject", msg.Subject()))
		_ = msg.Term()
		return
	}
	if err := c.Handle(ctx, e); err != nil {
		slog.WarnContext(ctx, "ticket: ingress: handle failed, will redeliver",
			slog.String("err", err.Error()),
			slog.String("subject", msg.Subject()))
		_ = msg.Nak()
		return
	}
	_ = msg.Ack()
}

// Handle is the pure-data entry point: takes an IngressEvent and
// upserts customer + conversation + ticket + message inside one tx.
// Returns nil for events we deliberately ignore (e.g. status / read
// receipts that don't create state).
func (c *Consumer) Handle(ctx context.Context, e IngressEvent) error {
	if !shouldCreateTicket(e) {
		// status / read / typing / favorite / follow events are
		// surfaced via the realtime layer but don't change ticket
		// state -- ack and move on.
		return nil
	}

	tenantID, err := uuid.Parse(e.TenantID)
	if err != nil {
		// Termable: tenant_id should never be malformed if the
		// connector did its job. Returning nil so it's Ack'd, not
		// retried forever.
		slog.WarnContext(ctx, "ticket: ingress: bad tenant_id",
			slog.String("tenant_id", e.TenantID))
		return nil
	}

	tx, err := c.Repo.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return fmt.Errorf("ticket: ingress begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	customerID, err := upsertCustomer(ctx, tx, tenantID, e)
	if err != nil {
		return err
	}
	conversationID, err := upsertConversation(ctx, tx, tenantID, customerID, e.Channel, e.ConversationKey)
	if err != nil {
		return err
	}
	ticketID, err := openOrReopenTicket(ctx, tx, tenantID, conversationID)
	if err != nil {
		return err
	}
	if err := appendInboundMessage(ctx, tx, tenantID, ticketID, e); err != nil {
		return err
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("ticket: ingress commit: %w", err)
	}
	return nil
}

// shouldCreateTicket whitelists the kinds that warrant ticket state.
// Reading the existing connectors:
//   facebook   -> message | postback                          (message kinds)
//                 read | delivery                             (signal only)
//   x          -> mention | reply | quote | dm                (message kinds)
//                 favorite | follow | unfollow                (signal only)
//   whatsapp   -> message                                     (message kind)
//                 status                                      (signal only)
//   instagram  -> dm | comment | mention                      (message kinds)
//                 read                                        (signal only)
//   widget     -> message | identified                        (message kinds)
//                 typing                                      (signal only)
func shouldCreateTicket(e IngressEvent) bool {
	switch e.Kind {
	case "message", "postback", "mention", "reply", "quote", "dm", "comment":
		return true
	case "identified":
		// Not a customer message per se, but the first identification
		// often arrives without a body and we want a ticket so the
		// agent has something to assign. Treat as ticket-worthy.
		return true
	default:
		return false
	}
}

// upsertCustomer finds the existing customer by tenant + external_ref,
// or inserts a new one. Returns the resolved id.
func upsertCustomer(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, e IngressEvent) (uuid.UUID, error) {
	// We index external_refs as a JSONB field with a key per channel.
	// e.g. {"fb":"PSID_123"} | {"x":"USR_42"} | {"wa":"254799000111"}.
	key := externalRefKey(e.Channel)
	var id uuid.UUID
	err := tx.QueryRow(ctx, `
		SELECT id FROM customers
		WHERE tenant_id = $1 AND external_refs ->> $2 = $3
		LIMIT 1`,
		tenantID, key, e.CustomerExternal).Scan(&id)
	if err == nil {
		// Already known; opportunistically backfill display_name /
		// email / phone if the connector sent newer values.
		if e.CustomerName != "" || e.CustomerEmail != "" || e.CustomerPhone != "" {
			if _, err := tx.Exec(ctx, `
				UPDATE customers SET
				  display_name = COALESCE(NULLIF($2, ''), display_name),
				  email        = COALESCE(NULLIF($3, '')::citext, email),
				  phone        = COALESCE(NULLIF($4, ''), phone)
				WHERE id = $1`,
				id, e.CustomerName, e.CustomerEmail, e.CustomerPhone); err != nil {
				return uuid.Nil, err
			}
		}
		return id, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, err
	}

	// Create.
	displayName := strings.TrimSpace(e.CustomerName)
	if displayName == "" {
		displayName = strings.TrimSpace(e.CustomerHandle)
	}
	if displayName == "" {
		displayName = e.Channel + ":" + e.CustomerExternal
	}
	if err := tx.QueryRow(ctx, `
		INSERT INTO customers (tenant_id, display_name, email, phone, external_refs)
		VALUES ($1, $2, NULLIF($3, '')::citext, NULLIF($4, ''),
		        jsonb_build_object($5::text, $6::text))
		RETURNING id`,
		tenantID, displayName, e.CustomerEmail, e.CustomerPhone,
		key, e.CustomerExternal,
	).Scan(&id); err != nil {
		return uuid.Nil, err
	}
	return id, nil
}

func externalRefKey(channel string) string {
	switch channel {
	case "fb", "x", "wa", "ig", "widget", "voice":
		return channel
	}
	return channel
}

// upsertConversation enforces the (tenant, channel, channel_thread_id)
// UNIQUE constraint via ON CONFLICT and returns the resolved id.
func upsertConversation(ctx context.Context, tx pgx.Tx, tenantID, customerID uuid.UUID, channel, threadKey string) (uuid.UUID, error) {
	var id uuid.UUID
	if err := tx.QueryRow(ctx, `
		INSERT INTO conversations (tenant_id, customer_id, channel, channel_thread_id)
		VALUES ($1, $2, $3, NULLIF($4, ''))
		ON CONFLICT (tenant_id, channel, channel_thread_id) DO UPDATE
		  SET customer_id = EXCLUDED.customer_id
		RETURNING id`,
		tenantID, customerID, channel, threadKey,
	).Scan(&id); err != nil {
		return uuid.Nil, err
	}
	return id, nil
}

// openOrReopenTicket finds the most recent ticket on the conversation
// and either reuses it (if it's still open) or creates a fresh one
// (if it's closed/resolved). Per §6 of the plan: "from resolved/closed
// can reopened -> open on customer reply within retention window".
func openOrReopenTicket(ctx context.Context, tx pgx.Tx, tenantID, conversationID uuid.UUID) (uuid.UUID, error) {
	var (
		id    uuid.UUID
		state State
	)
	err := tx.QueryRow(ctx, `
		SELECT id, state FROM tickets
		WHERE tenant_id = $1 AND conversation_id = $2
		ORDER BY created_at DESC
		LIMIT 1`, tenantID, conversationID).Scan(&id, &state)

	switch {
	case errors.Is(err, pgx.ErrNoRows):
		// First contact -- create a fresh ticket via the existing
		// repo path so SLA deadlines + defaults are applied uniformly.
		return createTicketInTx(ctx, tx, tenantID, conversationID)
	case err != nil:
		return uuid.Nil, err
	}

	if state == StateResolved || state == StateClosed {
		// Reopen the existing ticket -- valid transitions are
		// resolved->reopened and closed->reopened.
		if _, err := tx.Exec(ctx,
			`UPDATE tickets SET state = 'reopened' WHERE id = $1`, id); err != nil {
			return uuid.Nil, err
		}
	}
	return id, nil
}

// createTicketInTx inlines the SQL from Repo.CreateTicket so the whole
// ingress upsert sits in one transaction. Defaults match Repo.CreateTicket.
func createTicketInTx(ctx context.Context, tx pgx.Tx, tenantID, conversationID uuid.UUID) (uuid.UUID, error) {
	const priority int16 = 3
	now := time.Now().UTC()
	// Mirror sla.PolicyFor(3): 1h first response, 24h resolution.
	frDue := now.Add(time.Hour)
	resDue := now.Add(24 * time.Hour)

	var id uuid.UUID
	if err := tx.QueryRow(ctx, `
		INSERT INTO tickets (tenant_id, conversation_id, state, priority, required_skills,
		                     sla_first_response_due, sla_resolution_due)
		VALUES ($1, $2, 'new', $3, ARRAY[]::text[], $4, $5)
		RETURNING id`,
		tenantID, conversationID, priority, frDue, resDue,
	).Scan(&id); err != nil {
		return uuid.Nil, err
	}
	return id, nil
}

// appendInboundMessage writes the message row. Idempotent on the
// (tenant_id, platform_message_id) lookup -- see the Phase 0 §17 note
// on dedupe via Redis SET; here we additionally guard with an EXISTS
// check so a JetStream redelivery + a Redis miss don't double-write.
func appendInboundMessage(ctx context.Context, tx pgx.Tx, tenantID, ticketID uuid.UUID, e IngressEvent) error {
	if e.PlatformMsgID != "" {
		var seen bool
		if err := tx.QueryRow(ctx, `
			SELECT EXISTS(
			  SELECT 1 FROM messages
			  WHERE tenant_id = $1 AND platform_message_id = $2
			)`, tenantID, e.PlatformMsgID).Scan(&seen); err != nil {
			return err
		}
		if seen {
			return nil // already ingested
		}
	}
	_, err := tx.Exec(ctx, `
		INSERT INTO messages
		  (tenant_id, ticket_id, direction, body, attachments, platform_message_id, created_at)
		VALUES ($1, $2, 'in', $3, ARRAY[]::uuid[], NULLIF($4, ''), $5)`,
		tenantID, ticketID, e.Body, e.PlatformMsgID, ingressTimestamp(e))
	return err
}

func ingressTimestamp(e IngressEvent) time.Time {
	if e.OccurredAt.IsZero() {
		return time.Now().UTC()
	}
	return e.OccurredAt.UTC()
}
