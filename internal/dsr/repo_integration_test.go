//go:build integration

package dsr

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
	url := os.Getenv("DSR_TEST_DB_URL")
	if url == "" {
		t.Skip("DSR_TEST_DB_URL not set")
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
	mods := []string{"0001_init", "0002_agents", "0003_customers", "0012_dsr"}
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

func seedTenant(t *testing.T, ctx context.Context, pool *pgxpool.Pool) uuid.UUID {
	t.Helper()
	tenant := uuid.New()
	if _, err := pool.Exec(ctx,
		`INSERT INTO tenants(id, slug, display_name) VALUES($1,'acme','Acme')`, tenant); err != nil {
		t.Fatal(err)
	}
	return tenant
}

func TestRepo_OpenSetsDueIn30Days(t *testing.T) {
	ctx := context.Background()
	pool := freshPool(t)
	tenant := seedTenant(t, ctx, pool)
	r := NewRepo(pool)

	req, err := r.OpenRequest(ctx, OpenParams{
		TenantID: tenant, SubjectEmail: "subj@example.co.ke",
		Kind: KindAccess, Reason: "want my data",
	})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if req.State != StateReceived {
		t.Errorf("initial state = %s, want received", req.State)
	}
	delta := req.DueAt.Sub(req.ReceivedAt)
	if delta < 29*24*time.Hour || delta > 31*24*time.Hour {
		t.Errorf("due window = %s, want ~30d", delta)
	}
}

func TestRepo_RejectsRequestWithoutSubject(t *testing.T) {
	ctx := context.Background()
	pool := freshPool(t)
	tenant := seedTenant(t, ctx, pool)
	if _, err := NewRepo(pool).OpenRequest(ctx, OpenParams{
		TenantID: tenant, Kind: KindAccess,
	}); !errors.Is(err, ErrSubjectRequired) {
		t.Fatalf("want ErrSubjectRequired, got %v", err)
	}
}

func TestRepo_LifecycleAssignThenResolve(t *testing.T) {
	ctx := context.Background()
	pool := freshPool(t)
	tenant := seedTenant(t, ctx, pool)

	// Seed an agent so Assign can FK to one.
	agent := uuid.New()
	if _, err := pool.Exec(ctx,
		`INSERT INTO agents(id, tenant_id, email, display_name, role)
		 VALUES($1,$2,'dpo@x','DPO','dpo')`,
		agent, tenant); err != nil {
		t.Fatal(err)
	}

	r := NewRepo(pool)
	req, err := r.OpenRequest(ctx, OpenParams{
		TenantID: tenant, SubjectEmail: "x@y", Kind: KindErasure,
	})
	if err != nil {
		t.Fatal(err)
	}

	assigned, err := r.Assign(ctx, tenant, req.ID, agent)
	if err != nil {
		t.Fatalf("assign: %v", err)
	}
	if assigned.State != StateInProgress {
		t.Errorf("state after assign = %s, want in_progress", assigned.State)
	}
	if assigned.AssignedToAgentID == nil || *assigned.AssignedToAgentID != agent {
		t.Errorf("assigned_to_agent_id = %v want %v", assigned.AssignedToAgentID, agent)
	}

	// Illegal transition: in_progress -> received.
	if _, err := r.Resolve(ctx, ResolveParams{
		TenantID: tenant, ID: req.ID, To: StateReceived,
	}); err == nil {
		t.Error("illegal transition accepted")
	}

	// Legal: in_progress -> fulfilled stamps fulfilled_at.
	final, err := r.Resolve(ctx, ResolveParams{
		TenantID: tenant, ID: req.ID, To: StateFulfilled,
	})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if final.State != StateFulfilled || final.FulfilledAt == nil {
		t.Errorf("final state mis-stamped: %+v", final)
	}

	// Re-resolving a terminal request is illegal.
	if _, err := r.Resolve(ctx, ResolveParams{
		TenantID: tenant, ID: req.ID, To: StateRejected,
		ResolutionNote: "nope",
	}); err == nil {
		t.Error("re-resolve of terminal accepted")
	}
}

func TestRepo_ListSortsByDueAt(t *testing.T) {
	ctx := context.Background()
	pool := freshPool(t)
	tenant := seedTenant(t, ctx, pool)
	r := NewRepo(pool)

	for i := 0; i < 3; i++ {
		if _, err := r.OpenRequest(ctx, OpenParams{
			TenantID: tenant, SubjectEmail: "x@y", Kind: KindAccess,
		}); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := r.List(ctx, tenant, "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 3 {
		t.Fatalf("rows=%d want 3", len(rows))
	}
	for i := 1; i < len(rows); i++ {
		if rows[i].DueAt.Before(rows[i-1].DueAt) {
			t.Errorf("rows not sorted by due_at: [%d]=%s [%d]=%s",
				i-1, rows[i-1].DueAt, i, rows[i].DueAt)
		}
	}
}
