//go:build integration

package supervisor

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
	url := os.Getenv("SUPERVISOR_TEST_DB_URL")
	if url == "" {
		t.Skip("SUPERVISOR_TEST_DB_URL not set")
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

func seed(t *testing.T, ctx context.Context, pool *pgxpool.Pool) (uuid.UUID, uuid.UUID) {
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
	return tenant, conv
}

func TestRepo_QueueDepthByState(t *testing.T) {
	ctx := context.Background()
	pool := freshPool(t)
	tenant, conv := seed(t, ctx, pool)

	for state, n := range map[string]int{"new": 2, "open": 3, "closed": 1} {
		for i := 0; i < n; i++ {
			if _, err := pool.Exec(ctx,
				`INSERT INTO tickets(tenant_id, conversation_id, state) VALUES($1,$2,$3::ticket_state)`,
				tenant, conv, state); err != nil {
				t.Fatalf("insert %s[%d]: %v", state, i, err)
			}
		}
	}

	r := NewRepo(pool)
	got, err := r.QueueDepth(ctx, tenant)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]int{"new": 2, "open": 3, "closed": 1}
	if len(got) != len(want) {
		t.Fatalf("rows = %d, want %d (%+v)", len(got), len(want), got)
	}
	for _, q := range got {
		if want[q.State] != q.Count {
			t.Errorf("state=%s got %d want %d", q.State, q.Count, want[q.State])
		}
	}
}

func TestRepo_AgentPresence(t *testing.T) {
	ctx := context.Background()
	pool := freshPool(t)
	tenant, _ := seed(t, ctx, pool)

	id := uuid.New()
	if _, err := pool.Exec(ctx,
		`INSERT INTO agents(id, tenant_id, email, display_name) VALUES($1,$2,'a@x','Alice')`,
		id, tenant); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO agent_status(agent_id, status, current_load) VALUES($1,'online',2)`,
		id); err != nil {
		t.Fatal(err)
	}

	got, err := NewRepo(pool).AgentPresence(ctx, tenant)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("rows = %d, want 1", len(got))
	}
	if got[0].Status != "online" || got[0].CurrentLoad != 2 || got[0].DisplayName != "Alice" {
		t.Errorf("row mis-mapped: %+v", got[0])
	}
}

func TestRepo_AtRiskTickets(t *testing.T) {
	ctx := context.Background()
	pool := freshPool(t)
	tenant, conv := seed(t, ctx, pool)

	now := time.Now()
	cases := []struct {
		name       string
		frDue      *time.Time
		resDue     *time.Time
		shouldList bool
	}{
		{
			name:       "fr breach in 5 min",
			frDue:      ptrTime(now.Add(5 * time.Minute)),
			resDue:     ptrTime(now.Add(24 * time.Hour)),
			shouldList: true,
		},
		{
			name:       "deep within SLA — excluded",
			frDue:      ptrTime(now.Add(20 * time.Hour)),
			resDue:     ptrTime(now.Add(40 * time.Hour)),
			shouldList: false,
		},
	}
	for _, tc := range cases {
		if _, err := pool.Exec(ctx, `
			INSERT INTO tickets(tenant_id, conversation_id, sla_first_response_due, sla_resolution_due)
			VALUES($1,$2,$3,$4)`,
			tenant, conv, tc.frDue, tc.resDue); err != nil {
			t.Fatal(err)
		}
	}

	got, err := NewRepo(pool).AtRiskTickets(ctx, tenant, 30*time.Minute, 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Errorf("rows = %d, want 1 (only the 5-min-out fr breach)", len(got))
	}
}

func ptrTime(t time.Time) *time.Time { return &t }
