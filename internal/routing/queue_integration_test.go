//go:build integration

package routing

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestReevaluator_RoutesUnassignedAfterAgentComesOnline(t *testing.T) {
	ctx := context.Background()
	pool := freshPool(t)

	// Seed tenant + conversation.
	tenant, conv := seed(t, ctx, pool)

	// Create a ticket while no agent is online -- it stays unassigned.
	var ticketID uuid.UUID
	if err := pool.QueryRow(ctx, `
		INSERT INTO tickets(tenant_id, conversation_id, priority, created_at)
		VALUES($1, $2, 3, now() - interval '1 minute')
		RETURNING id`,
		tenant, conv).Scan(&ticketID); err != nil {
		t.Fatal(err)
	}

	// Now bring an agent online with capacity.
	agent := addAgent(t, ctx, pool, tenant, "ada@x", "online", 0, nil)

	r := NewReevaluator(New(pool))
	r.UnassignedAge = 0 // don't wait

	if err := r.Tick(ctx); err != nil {
		t.Fatalf("tick: %v", err)
	}

	var assigned *uuid.UUID
	if err := pool.QueryRow(ctx,
		`SELECT assigned_agent_id FROM tickets WHERE id = $1`, ticketID).Scan(&assigned); err != nil {
		t.Fatal(err)
	}
	if assigned == nil || *assigned != agent {
		t.Errorf("ticket not routed to online agent: got %v want %v", assigned, agent)
	}
}

func TestReevaluator_ReassignsOnAgentOffline(t *testing.T) {
	ctx := context.Background()
	pool := freshPool(t)

	tenant, conv := seed(t, ctx, pool)

	// Two agents: one will go offline, one stays online to pick up.
	bob := addAgent(t, ctx, pool, tenant, "bob@x", "online", 1, nil)
	carol := addAgent(t, ctx, pool, tenant, "carol@x", "online", 0, nil)

	// Pre-assign a ticket to bob.
	var ticketID uuid.UUID
	if err := pool.QueryRow(ctx, `
		INSERT INTO tickets(tenant_id, conversation_id, priority, state, assigned_agent_id, created_at)
		VALUES($1, $2, 3, 'open'::ticket_state, $3, now() - interval '2 minutes')
		RETURNING id`,
		tenant, conv, bob).Scan(&ticketID); err != nil {
		t.Fatal(err)
	}

	// Bob's heartbeat goes stale (offline).
	if _, err := pool.Exec(ctx, `
		UPDATE agent_status SET status='offline', last_heartbeat = now() - interval '5 minutes'
		WHERE agent_id = $1`, bob); err != nil {
		t.Fatal(err)
	}

	r := NewReevaluator(New(pool))
	r.OfflineGrace = 1 * time.Second // shrink window for the test

	if err := r.Tick(ctx); err != nil {
		t.Fatalf("tick: %v", err)
	}

	var assigned *uuid.UUID
	if err := pool.QueryRow(ctx,
		`SELECT assigned_agent_id FROM tickets WHERE id = $1`, ticketID).Scan(&assigned); err != nil {
		t.Fatal(err)
	}
	if assigned == nil || *assigned != carol {
		t.Errorf("ticket not reassigned to online peer: got %v want %v", assigned, carol)
	}

	// And the assignment row records the right reason.
	var reason string
	if err := pool.QueryRow(ctx,
		`SELECT reason FROM assignments WHERE ticket_id = $1 ORDER BY at DESC LIMIT 1`,
		ticketID).Scan(&reason); err != nil {
		t.Fatal(err)
	}
	if reason != string(ReasonReassignOffline) {
		t.Errorf("reason = %q, want %q", reason, ReasonReassignOffline)
	}
}

func TestEngine_Escalate_PicksSeniorAgent(t *testing.T) {
	ctx := context.Background()
	pool := freshPool(t)
	tenant, conv := seed(t, ctx, pool)

	jr := addAgent(t, ctx, pool, tenant, "junior@x", "online", 1, nil)
	// Promote one agent to senior.
	sr := uuid.New()
	if _, err := pool.Exec(ctx,
		`INSERT INTO agents(id, tenant_id, email, display_name, role) VALUES($1,$2,'sr@x','Senior','senior_agent')`,
		sr, tenant); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO agent_status(agent_id, status, current_load) VALUES($1,'online',0)`,
		sr); err != nil {
		t.Fatal(err)
	}

	var ticketID uuid.UUID
	if err := pool.QueryRow(ctx, `
		INSERT INTO tickets(tenant_id, conversation_id, priority, state, assigned_agent_id)
		VALUES($1,$2,2,'open'::ticket_state,$3) RETURNING id`,
		tenant, conv, jr).Scan(&ticketID); err != nil {
		t.Fatal(err)
	}

	dec, err := New(pool).Escalate(ctx, tenant, ticketID, TierAgent)
	if err != nil {
		t.Fatalf("escalate: %v", err)
	}
	if dec.AgentID != sr {
		t.Errorf("escalated to %v, want %v (senior)", dec.AgentID, sr)
	}
	if dec.Reason != ReasonEscalation {
		t.Errorf("reason = %v, want escalation", dec.Reason)
	}
}
