//go:build integration

package ticket

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestIngress_FirstFBMessageCreatesEverything(t *testing.T) {
	ctx := context.Background()
	pool := freshPool(t)

	// Seed only the tenant; the ingress consumer should create the
	// customer + conversation + ticket + message itself.
	tenantID := uuid.New()
	if _, err := pool.Exec(ctx,
		`INSERT INTO tenants(id, slug, display_name) VALUES($1,'acme','Acme')`,
		tenantID); err != nil {
		t.Fatal(err)
	}

	c := NewConsumer(nil, NewRepo(pool))
	err := c.Handle(ctx, IngressEvent{
		TenantID:         tenantID.String(),
		Channel:          "fb",
		Kind:             "message",
		CustomerExternal: "PSID_ALICE",
		CustomerName:     "Alice",
		ConversationKey:  "PSID_ALICE",
		PlatformMsgID:    "mid.1",
		Body:             "My order is late",
		OccurredAt:       time.Now().UTC().Add(-time.Minute),
	})
	if err != nil {
		t.Fatalf("handle: %v", err)
	}

	// Customer
	var customerID uuid.UUID
	var displayName string
	if err := pool.QueryRow(ctx, `
		SELECT id, display_name FROM customers
		WHERE tenant_id = $1 AND external_refs->>'fb' = 'PSID_ALICE'`,
		tenantID).Scan(&customerID, &displayName); err != nil {
		t.Fatalf("customer not created: %v", err)
	}
	if displayName != "Alice" {
		t.Errorf("display_name = %q, want Alice", displayName)
	}

	// Conversation
	var convID uuid.UUID
	if err := pool.QueryRow(ctx, `
		SELECT id FROM conversations
		WHERE tenant_id = $1 AND channel = 'fb' AND channel_thread_id = 'PSID_ALICE'`,
		tenantID).Scan(&convID); err != nil {
		t.Fatalf("conversation not created: %v", err)
	}

	// Ticket
	var ticketID uuid.UUID
	var state string
	var priority int16
	if err := pool.QueryRow(ctx, `
		SELECT id, state, priority FROM tickets
		WHERE tenant_id = $1 AND conversation_id = $2`,
		tenantID, convID).Scan(&ticketID, &state, &priority); err != nil {
		t.Fatalf("ticket not created: %v", err)
	}
	if state != "new" || priority != 3 {
		t.Errorf("ticket: state=%q priority=%d (want new/3)", state, priority)
	}

	// Message
	var direction, body string
	if err := pool.QueryRow(ctx, `
		SELECT direction, body FROM messages
		WHERE tenant_id = $1 AND ticket_id = $2`,
		tenantID, ticketID).Scan(&direction, &body); err != nil {
		t.Fatalf("message not created: %v", err)
	}
	if direction != "in" || body != "My order is late" {
		t.Errorf("message: direction=%q body=%q", direction, body)
	}
}

func TestIngress_DuplicatePlatformMsgIDIsNoop(t *testing.T) {
	ctx := context.Background()
	pool := freshPool(t)
	tenantID := uuid.New()
	if _, err := pool.Exec(ctx,
		`INSERT INTO tenants(id, slug, display_name) VALUES($1,'acme','Acme')`,
		tenantID); err != nil {
		t.Fatal(err)
	}
	c := NewConsumer(nil, NewRepo(pool))
	ev := IngressEvent{
		TenantID:         tenantID.String(),
		Channel:          "wa",
		Kind:             "message",
		CustomerExternal: "254700111222",
		ConversationKey:  "254700111222",
		PlatformMsgID:    "wamid.DUP",
		Body:             "first",
		OccurredAt:       time.Now().UTC(),
	}
	if err := c.Handle(ctx, ev); err != nil {
		t.Fatal(err)
	}
	// Second delivery with same wamid -- should NOT create a second
	// message row.
	ev.Body = "should not duplicate"
	if err := c.Handle(ctx, ev); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM messages WHERE platform_message_id = 'wamid.DUP'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("duplicate wamid produced %d rows, want 1", n)
	}
}

func TestIngress_ReopensClosedTicketOnNewInbound(t *testing.T) {
	ctx := context.Background()
	pool := freshPool(t)
	tenantID := uuid.New()
	if _, err := pool.Exec(ctx,
		`INSERT INTO tenants(id, slug, display_name) VALUES($1,'acme','Acme')`,
		tenantID); err != nil {
		t.Fatal(err)
	}
	c := NewConsumer(nil, NewRepo(pool))
	ev := IngressEvent{
		TenantID:         tenantID.String(),
		Channel:          "x",
		Kind:             "dm",
		CustomerExternal: "X_USR_42",
		ConversationKey:  "X_USR_42:ACCT",
		PlatformMsgID:    "x.1",
		Body:             "first contact",
		OccurredAt:       time.Now().UTC(),
	}
	if err := c.Handle(ctx, ev); err != nil {
		t.Fatal(err)
	}

	// Walk the ticket to closed.
	var ticketID uuid.UUID
	if err := pool.QueryRow(ctx, `
		SELECT t.id FROM tickets t
		JOIN conversations c ON c.id = t.conversation_id
		WHERE t.tenant_id = $1 AND c.channel = 'x' AND c.channel_thread_id = $2`,
		tenantID, ev.ConversationKey).Scan(&ticketID); err != nil {
		t.Fatal(err)
	}
	repo := NewRepo(pool)
	for _, st := range []State{StateOpen, StateResolved, StateClosed} {
		if _, err := repo.ChangeState(ctx, tenantID, ticketID, st); err != nil {
			t.Fatalf("ChangeState %s: %v", st, err)
		}
	}

	// New inbound on the same conversation; should reopen the same
	// ticket, not create a fresh one.
	ev2 := ev
	ev2.PlatformMsgID = "x.2"
	ev2.Body = "back again"
	if err := c.Handle(ctx, ev2); err != nil {
		t.Fatalf("handle 2: %v", err)
	}

	var n int
	if err := pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM tickets WHERE tenant_id = $1`, tenantID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("reopened path produced %d tickets, want 1", n)
	}
	var state string
	if err := pool.QueryRow(ctx,
		`SELECT state FROM tickets WHERE id = $1`, ticketID).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != "reopened" {
		t.Errorf("state = %q, want reopened", state)
	}
}

func TestIngress_StatusEventsDontCreateState(t *testing.T) {
	ctx := context.Background()
	pool := freshPool(t)
	tenantID := uuid.New()
	if _, err := pool.Exec(ctx,
		`INSERT INTO tenants(id, slug, display_name) VALUES($1,'acme','Acme')`,
		tenantID); err != nil {
		t.Fatal(err)
	}
	c := NewConsumer(nil, NewRepo(pool))
	for _, kind := range []string{"read", "delivery", "status", "favorite", "follow", "typing"} {
		if err := c.Handle(ctx, IngressEvent{
			TenantID: tenantID.String(), Channel: "fb", Kind: kind,
			CustomerExternal: "PSID_BOB", ConversationKey: "PSID_BOB",
		}); err != nil {
			t.Fatalf("handle %s: %v", kind, err)
		}
	}
	var n int
	if err := pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM tickets WHERE tenant_id = $1`, tenantID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("signal kinds produced %d tickets, want 0", n)
	}
}
