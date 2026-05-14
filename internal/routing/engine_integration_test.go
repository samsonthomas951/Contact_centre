//go:build integration

package routing

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func dbURL(t *testing.T) string {
	t.Helper()
	url := os.Getenv("ROUTING_TEST_DB_URL")
	if url == "" {
		t.Skip("ROUTING_TEST_DB_URL not set")
	}
	return url
}

func freshPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dbURL(t))
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	t.Cleanup(pool.Close)

	if _, err := pool.Exec(ctx, `DROP SCHEMA IF EXISTS public CASCADE; CREATE SCHEMA public;`); err != nil {
		t.Fatalf("reset: %v", err)
	}
	_, file, _, _ := runtime.Caller(0)
	root := filepath.Join(filepath.Dir(file), "..", "..", "migrations")
	for _, name := range []string{"0001_init", "0002_agents", "0003_customers", "0004_tickets"} {
		b, err := os.ReadFile(filepath.Join(root, name+".up.sql"))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		if _, err := pool.Exec(ctx, string(b)); err != nil {
			t.Fatalf("apply %s: %v", name, err)
		}
	}
	return pool
}

func seed(t *testing.T, ctx context.Context, pool *pgxpool.Pool) (tenant uuid.UUID, conv uuid.UUID) {
	t.Helper()
	tenant = uuid.New()
	customer := uuid.New()
	conv = uuid.New()
	if _, err := pool.Exec(ctx,
		`INSERT INTO tenants(id, slug, display_name) VALUES($1,'acme','Acme')`, tenant); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO customers(id, tenant_id, display_name) VALUES($1,$2,'Jane')`,
		customer, tenant); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO conversations(id, tenant_id, customer_id, channel) VALUES($1,$2,$3,'fb')`,
		conv, tenant, customer); err != nil {
		t.Fatal(err)
	}
	return tenant, conv
}

func addAgent(t *testing.T, ctx context.Context, pool *pgxpool.Pool, tenant uuid.UUID,
	email, status string, load int, skills []string) uuid.UUID {
	t.Helper()
	id := uuid.New()
	if _, err := pool.Exec(ctx,
		`INSERT INTO agents(id, tenant_id, email, display_name) VALUES($1,$2,$3,$4)`,
		id, tenant, email, email); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO agent_status(agent_id, status, current_load) VALUES($1,$2,$3)`,
		id, status, load); err != nil {
		t.Fatal(err)
	}
	for _, sk := range skills {
		if _, err := pool.Exec(ctx,
			`INSERT INTO agent_skills(agent_id, skill) VALUES($1,$2)`, id, sk); err != nil {
			t.Fatal(err)
		}
	}
	return id
}

func TestEngine_PicksOnlyEligibleAgent(t *testing.T) {
	ctx := context.Background()
	pool := freshPool(t)
	tenant, conv := seed(t, ctx, pool)

	// One online with the right skill, one offline, one missing skill.
	want := addAgent(t, ctx, pool, tenant, "ok@x", "online", 0, []string{"swahili"})
	_ = addAgent(t, ctx, pool, tenant, "off@x", "offline", 0, []string{"swahili"})
	_ = addAgent(t, ctx, pool, tenant, "noskill@x", "online", 0, []string{"english"})

	var ticketID uuid.UUID
	if err := pool.QueryRow(ctx,
		`INSERT INTO tickets(tenant_id, conversation_id, required_skills, priority)
		 VALUES($1,$2,$3::text[],3) RETURNING id`,
		tenant, conv, []string{"swahili"}).Scan(&ticketID); err != nil {
		t.Fatal(err)
	}

	eng := New(pool)
	dec, err := eng.Route(ctx, tenant, ticketID, ReasonAutoRoute)
	if err != nil {
		t.Fatalf("route: %v", err)
	}
	if dec.AgentID != want {
		t.Errorf("picked %v, want %v", dec.AgentID, want)
	}

	// Side effects: ticket assigned, state moved to open, load incremented,
	// assignment row written.
	var assignedTo uuid.UUID
	var state string
	if err := pool.QueryRow(ctx,
		`SELECT assigned_agent_id, state FROM tickets WHERE id = $1`, ticketID).
		Scan(&assignedTo, &state); err != nil {
		t.Fatal(err)
	}
	if assignedTo != want || state != "open" {
		t.Errorf("ticket state: assigned=%v state=%s", assignedTo, state)
	}

	var load int
	if err := pool.QueryRow(ctx,
		`SELECT current_load FROM agent_status WHERE agent_id = $1`, want).Scan(&load); err != nil {
		t.Fatal(err)
	}
	if load != 1 {
		t.Errorf("current_load = %d, want 1", load)
	}

	var assignmentCount int
	if err := pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM assignments WHERE ticket_id = $1`, ticketID).Scan(&assignmentCount); err != nil {
		t.Fatal(err)
	}
	if assignmentCount != 1 {
		t.Errorf("assignment rows = %d, want 1", assignmentCount)
	}
}

func TestEngine_NoEligibleAgentReturnsErr(t *testing.T) {
	ctx := context.Background()
	pool := freshPool(t)
	tenant, conv := seed(t, ctx, pool)
	_ = addAgent(t, ctx, pool, tenant, "off@x", "offline", 0, nil)

	var ticketID uuid.UUID
	if err := pool.QueryRow(ctx,
		`INSERT INTO tickets(tenant_id, conversation_id) VALUES($1,$2) RETURNING id`,
		tenant, conv).Scan(&ticketID); err != nil {
		t.Fatal(err)
	}

	eng := New(pool)
	if _, err := eng.Route(ctx, tenant, ticketID, ReasonAutoRoute); !errors.Is(err, ErrNoEligibleAgent) {
		t.Fatalf("err = %v, want ErrNoEligibleAgent", err)
	}
}

func TestEngine_LoadAndFreshnessRouting(t *testing.T) {
	ctx := context.Background()
	pool := freshPool(t)
	tenant, conv := seed(t, ctx, pool)
	eng := New(pool)
	eng.Now = func() time.Time { return time.Now() }

	// Three agents -- A and B have spare capacity; C is at capacity.
	a := addAgent(t, ctx, pool, tenant, "a@x", "online", 0, nil)
	_ = addAgent(t, ctx, pool, tenant, "b@x", "online", 4, nil)
	_ = addAgent(t, ctx, pool, tenant, "c@x", "online", 5, nil)
	_ = a

	var ticketID uuid.UUID
	if err := pool.QueryRow(ctx,
		`INSERT INTO tickets(tenant_id, conversation_id) VALUES($1,$2) RETURNING id`,
		tenant, conv).Scan(&ticketID); err != nil {
		t.Fatal(err)
	}

	dec, err := eng.Route(ctx, tenant, ticketID, ReasonAutoRoute)
	if err != nil {
		t.Fatalf("route: %v", err)
	}
	if dec.AgentID != a {
		t.Errorf("least-busy: picked %v, want %v (A)", dec.AgentID, a)
	}
}
