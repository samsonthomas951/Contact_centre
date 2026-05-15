//go:build integration

package ticket

import (
	"context"
	"testing"

	"github.com/google/uuid"
)

func TestRepo_List_DefaultsToOpenWork(t *testing.T) {
	ctx := context.Background()
	pool := freshPool(t)
	tenant, conv := seedTicket(t, ctx, pool)
	r := NewRepo(pool)

	// Three tickets in different states.
	tk1, err := r.CreateTicket(ctx, CreateTicketParams{TenantID: tenant, ConversationID: conv, Priority: 1})
	if err != nil {
		t.Fatal(err)
	}
	tk2, err := r.CreateTicket(ctx, CreateTicketParams{TenantID: tenant, ConversationID: conv, Priority: 3})
	if err != nil {
		t.Fatal(err)
	}
	// Move one to closed.
	if _, err := r.ChangeState(ctx, tenant, tk1.ID, StateOpen); err != nil {
		t.Fatal(err)
	}
	if _, err := r.ChangeState(ctx, tenant, tk1.ID, StateResolved); err != nil {
		t.Fatal(err)
	}
	if _, err := r.ChangeState(ctx, tenant, tk1.ID, StateClosed); err != nil {
		t.Fatal(err)
	}

	got, err := r.List(ctx, ListParams{TenantID: tenant})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("default List should return only open work; got %d (%+v)", len(got), got)
	}
	if got[0].ID != tk2.ID {
		t.Errorf("returned wrong ticket: %v want %v", got[0].ID, tk2.ID)
	}
}

func TestRepo_List_PrioritySort(t *testing.T) {
	ctx := context.Background()
	pool := freshPool(t)
	tenant, conv := seedTicket(t, ctx, pool)
	r := NewRepo(pool)

	// Create low (5), urgent (1), normal (3) -- expect [1,3,5] order.
	for _, p := range []int16{5, 1, 3} {
		if _, err := r.CreateTicket(ctx, CreateTicketParams{
			TenantID: tenant, ConversationID: conv, Priority: p,
		}); err != nil {
			t.Fatal(err)
		}
	}
	got, err := r.List(ctx, ListParams{TenantID: tenant})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("len=%d want 3", len(got))
	}
	if got[0].Priority != 1 || got[1].Priority != 3 || got[2].Priority != 5 {
		t.Errorf("priority order: %d, %d, %d (want 1,3,5)",
			got[0].Priority, got[1].Priority, got[2].Priority)
	}
}

func TestRepo_List_AssignedAgentFilter(t *testing.T) {
	ctx := context.Background()
	pool := freshPool(t)
	tenant, conv := seedTicket(t, ctx, pool)
	r := NewRepo(pool)

	// Add two agents.
	ada := uuid.New()
	bob := uuid.New()
	for _, id := range []uuid.UUID{ada, bob} {
		if _, err := pool.Exec(ctx,
			`INSERT INTO agents(id, tenant_id, email, display_name) VALUES($1,$2,$3,'A')`,
			id, tenant, "a"+id.String()[:6]+"@x"); err != nil {
			t.Fatal(err)
		}
	}

	// One ticket assigned to ada, one to bob, one unassigned.
	tk1, _ := r.CreateTicket(ctx, CreateTicketParams{TenantID: tenant, ConversationID: conv})
	tk2, _ := r.CreateTicket(ctx, CreateTicketParams{TenantID: tenant, ConversationID: conv})
	_, _ = r.CreateTicket(ctx, CreateTicketParams{TenantID: tenant, ConversationID: conv})

	if _, err := pool.Exec(ctx,
		`UPDATE tickets SET assigned_agent_id = $1, state = 'open' WHERE id = $2`,
		ada, tk1.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx,
		`UPDATE tickets SET assigned_agent_id = $1, state = 'open' WHERE id = $2`,
		bob, tk2.ID); err != nil {
		t.Fatal(err)
	}

	got, err := r.List(ctx, ListParams{TenantID: tenant, AssignedAgentID: &ada})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != tk1.ID {
		t.Errorf("ada filter returned %+v", got)
	}
}
