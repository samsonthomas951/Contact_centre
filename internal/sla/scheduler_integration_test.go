//go:build integration

package sla

import (
	"context"
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
	url := os.Getenv("SLA_TEST_DB_URL")
	if url == "" {
		t.Skip("SLA_TEST_DB_URL not set")
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

func TestScheduler_ScanBumpsPriorityOnBreach(t *testing.T) {
	ctx := context.Background()
	pool := freshPool(t)

	tenant := uuid.New()
	customer := uuid.New()
	conv := uuid.New()
	if _, err := pool.Exec(ctx, `INSERT INTO tenants(id, slug, display_name) VALUES($1,'acme','Acme')`, tenant); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO customers(id, tenant_id, display_name) VALUES($1,$2,'C')`, customer, tenant); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO conversations(id, tenant_id, customer_id, channel) VALUES($1,$2,$3,'fb')`, conv, tenant, customer); err != nil {
		t.Fatal(err)
	}

	// Insert a priority-3 ticket with a first-response deadline 10 min ago
	// (already breached) and a resolution deadline far in the future.
	created := time.Now().Add(-2 * time.Hour)
	frDue := time.Now().Add(-10 * time.Minute)
	resDue := time.Now().Add(48 * time.Hour)
	var ticketID uuid.UUID
	if err := pool.QueryRow(ctx, `
		INSERT INTO tickets(tenant_id, conversation_id, priority, state,
		                    sla_first_response_due, sla_resolution_due, created_at)
		VALUES($1,$2,3,'open',$3,$4,$5) RETURNING id`,
		tenant, conv, frDue, resDue, created).Scan(&ticketID); err != nil {
		t.Fatal(err)
	}

	s := &Scheduler{Pool: pool, JS: nil, Now: time.Now, Lookahead: 1 * time.Hour}
	// JS is nil so handle() will publish-error after the priority bump,
	// but the bump still happened. We tolerate that here -- production
	// always wires JS.
	if err := s.scanOnce(ctx); err != nil {
		t.Fatalf("scan: %v", err)
	}

	var newPriority int16
	if err := pool.QueryRow(ctx, `SELECT priority FROM tickets WHERE id = $1`, ticketID).
		Scan(&newPriority); err != nil {
		t.Fatal(err)
	}
	if newPriority != 2 {
		t.Errorf("priority after first_response breach = %d, want 2 (bumped from 3)", newPriority)
	}
}

func TestScheduler_ScanIgnoresRespondedTickets(t *testing.T) {
	ctx := context.Background()
	pool := freshPool(t)

	tenant, customer, conv := uuid.New(), uuid.New(), uuid.New()
	if _, err := pool.Exec(ctx, `INSERT INTO tenants(id, slug, display_name) VALUES($1,'acme','Acme')`, tenant); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO customers(id, tenant_id, display_name) VALUES($1,$2,'C')`, customer, tenant); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO conversations(id, tenant_id, customer_id, channel) VALUES($1,$2,$3,'fb')`, conv, tenant, customer); err != nil {
		t.Fatal(err)
	}

	frDue := time.Now().Add(-10 * time.Minute)
	resDue := time.Now().Add(48 * time.Hour)
	respondedAt := time.Now().Add(-30 * time.Minute)
	var ticketID uuid.UUID
	if err := pool.QueryRow(ctx, `
		INSERT INTO tickets(tenant_id, conversation_id, priority, state,
		                    sla_first_response_due, sla_resolution_due, first_response_at)
		VALUES($1,$2,3,'open',$3,$4,$5) RETURNING id`,
		tenant, conv, frDue, resDue, respondedAt).Scan(&ticketID); err != nil {
		t.Fatal(err)
	}

	s := &Scheduler{Pool: pool, JS: nil, Now: time.Now, Lookahead: 1 * time.Hour}
	if err := s.scanOnce(ctx); err != nil {
		t.Fatalf("scan: %v", err)
	}

	var p int16
	if err := pool.QueryRow(ctx, `SELECT priority FROM tickets WHERE id = $1`, ticketID).Scan(&p); err != nil {
		t.Fatal(err)
	}
	if p != 3 {
		t.Errorf("responded ticket priority should be unchanged: got %d, want 3", p)
	}
}
