//go:build integration

package analytics

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
	url := os.Getenv("ANALYTICS_TEST_DB_URL")
	if url == "" {
		t.Skip("ANALYTICS_TEST_DB_URL not set")
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
	mods := []string{
		"0001_init", "0002_agents", "0003_customers", "0004_tickets",
		"0011_analytics",
	}
	for _, name := range mods {
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

func seed(t *testing.T, ctx context.Context, pool *pgxpool.Pool) (uuid.UUID, uuid.UUID, uuid.UUID) {
	t.Helper()
	tenant, customer, conv := uuid.New(), uuid.New(), uuid.New()
	if _, err := pool.Exec(ctx,
		`INSERT INTO tenants(id, slug, display_name) VALUES($1,'acme','Acme')`, tenant); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO customers(id, tenant_id, display_name) VALUES($1,$2,'C')`,
		customer, tenant); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO conversations(id, tenant_id, customer_id, channel) VALUES($1,$2,$3,'fb')`,
		conv, tenant, customer); err != nil {
		t.Fatal(err)
	}
	return tenant, customer, conv
}

func TestMaterialise_TicketsDailyRollup(t *testing.T) {
	ctx := context.Background()
	pool := freshPool(t)
	tenant, _, conv := seed(t, ctx, pool)

	// Yesterday: 5 tickets created, 4 resolved within SLA, 1 missed.
	yesterday := time.Now().UTC().AddDate(0, 0, -1).Truncate(24 * time.Hour)
	createTime := yesterday.Add(8 * time.Hour)              // 08:00 yest
	frDue := createTime.Add(1 * time.Hour)                  // 09:00 yest
	resDue := createTime.Add(24 * time.Hour)                // 08:00 today (still yesterday-date for resolved check below — we'll mark earlier)

	// Four on-time tickets.
	for i := 0; i < 4; i++ {
		respondedAt := createTime.Add(30 * time.Minute)
		resolvedAt := createTime.Add(4 * time.Hour)
		if _, err := pool.Exec(ctx, `
			INSERT INTO tickets(tenant_id, conversation_id, state, priority,
			                    created_at, sla_first_response_due, sla_resolution_due,
			                    first_response_at, resolved_at)
			VALUES($1,$2,'resolved'::ticket_state,3,$3,$4,$5,$6,$7)`,
			tenant, conv, createTime, frDue, resDue, respondedAt, resolvedAt); err != nil {
			t.Fatal(err)
		}
	}
	// One missed SLA: replied after due.
	missedFR := createTime.Add(90 * time.Minute) // past 1h FR due
	missedRes := createTime.Add(4 * time.Hour)
	if _, err := pool.Exec(ctx, `
		INSERT INTO tickets(tenant_id, conversation_id, state, priority,
		                    created_at, sla_first_response_due, sla_resolution_due,
		                    first_response_at, resolved_at)
		VALUES($1,$2,'resolved'::ticket_state,3,$3,$4,$5,$6,$7)`,
		tenant, conv, createTime, frDue, resDue, missedFR, missedRes); err != nil {
		t.Fatal(err)
	}

	m := New(pool)
	m.Now = func() time.Time { return time.Now().UTC() } // today
	if err := m.RefreshTenant(ctx, tenant); err != nil {
		t.Fatalf("refresh: %v", err)
	}

	rows, err := m.TicketsDaily(ctx, tenant, yesterday, yesterday)
	if err != nil {
		t.Fatal(err)
	}
	// We expect at least an 'fb' row and an 'all' row.
	var fb, all *TicketsDailyRow
	for i := range rows {
		switch rows[i].Channel {
		case "fb":
			fb = &rows[i]
		case "all":
			all = &rows[i]
		}
	}
	if fb == nil {
		t.Fatalf("missing fb row; got %+v", rows)
	}
	if fb.CreatedCount != 5 || fb.ResolvedCount != 5 {
		t.Errorf("fb counts: created=%d resolved=%d, want 5/5", fb.CreatedCount, fb.ResolvedCount)
	}
	if fb.FirstResponseSLAMet == nil || *fb.FirstResponseSLAMet > 0.81 || *fb.FirstResponseSLAMet < 0.79 {
		t.Errorf("first_response_sla_met = %v, want ~0.8", fb.FirstResponseSLAMet)
	}
	if all == nil {
		t.Errorf("missing 'all' rollup row")
	}
}
