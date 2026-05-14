//go:build integration

package onboarding

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func dbURL(t *testing.T) string {
	t.Helper()
	url := os.Getenv("ONBOARDING_TEST_DB_URL")
	if url == "" {
		t.Skip("ONBOARDING_TEST_DB_URL not set")
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
	for _, name := range []string{"0001_init", "0002_agents"} {
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

func TestTenant_CreateAndUpdateRetention(t *testing.T) {
	ctx := context.Background()
	pool := freshPool(t)
	repo := NewTenantRepo(pool)

	tenant, err := repo.Create(ctx, CreateTenantParams{Slug: "acme", DisplayName: "Acme Ltd"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if tenant.RetentionMessagesDays != 730 {
		t.Errorf("default messages retention = %d, want 730", tenant.RetentionMessagesDays)
	}

	dup := *tenant
	if _, err := repo.Create(ctx, CreateTenantParams{Slug: "acme", DisplayName: "Dup"}); !errors.Is(err, ErrSlugTaken) {
		t.Errorf("dup slug: want ErrSlugTaken, got %v", err)
	}
	_ = dup

	twoYears := 730
	threeYears := 1095
	updated, err := repo.UpdateRetention(ctx, UpdateRetentionParams{
		TenantID:     tenant.ID,
		MessagesDays: &twoYears,
		AuditDays:    &threeYears,
	})
	if err != nil {
		t.Fatalf("update retention: %v", err)
	}
	if updated.RetentionMessagesDays != 730 || updated.RetentionAuditDays != 1095 {
		t.Errorf("retention not updated: %+v", updated)
	}
	if updated.RetentionDocumentsDays != tenant.RetentionDocumentsDays {
		t.Errorf("untouched field changed: was %d now %d",
			tenant.RetentionDocumentsDays, updated.RetentionDocumentsDays)
	}
}

func TestAgent_InviteIdempotentAndDeactivate(t *testing.T) {
	ctx := context.Background()
	pool := freshPool(t)

	tr := NewTenantRepo(pool)
	tenant, err := tr.Create(ctx, CreateTenantParams{Slug: "acme", DisplayName: "Acme"})
	if err != nil {
		t.Fatal(err)
	}

	ar := NewAgentRepo(pool)
	agentID := uuid.New()
	first, err := ar.Invite(ctx, InviteParams{
		TenantID: tenant.ID, AgentID: agentID,
		Email: "ada@x", DisplayName: "Ada", Role: "agent",
		MaxConcurrent: 5, Skills: []string{"swahili"},
	})
	if err != nil {
		t.Fatalf("invite: %v", err)
	}
	if first.Role != "agent" || !first.Active {
		t.Errorf("first invite: %+v", first)
	}

	// Re-invite same email -> upsert, skills replaced.
	second, err := ar.Invite(ctx, InviteParams{
		TenantID: tenant.ID, AgentID: agentID,
		Email: "ada@x", DisplayName: "Ada R", Role: "senior_agent",
		MaxConcurrent: 8, Skills: []string{"english"},
	})
	if err != nil {
		t.Fatalf("re-invite: %v", err)
	}
	if second.Role != "senior_agent" || second.MaxConcurrent != 8 || second.DisplayName != "Ada R" {
		t.Errorf("upsert didn't update fields: %+v", second)
	}
	var skillCount int
	if err := pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM agent_skills WHERE agent_id = $1`, agentID).Scan(&skillCount); err != nil {
		t.Fatal(err)
	}
	if skillCount != 1 {
		t.Errorf("skills not replaced: count=%d (want 1)", skillCount)
	}

	// Invalid role.
	if _, err := ar.Invite(ctx, InviteParams{
		TenantID: tenant.ID, AgentID: uuid.New(), Email: "x@x",
		DisplayName: "X", Role: "wizard",
	}); !errors.Is(err, ErrInvalidRole) {
		t.Errorf("bad role: want ErrInvalidRole, got %v", err)
	}

	// Deactivate -> active=false, status='offline'.
	if err := ar.Deactivate(ctx, tenant.ID, agentID); err != nil {
		t.Fatal(err)
	}
	got, _ := ar.Get(ctx, agentID)
	if got.Active {
		t.Error("expected active=false after deactivate")
	}
	var status string
	if err := pool.QueryRow(ctx,
		`SELECT status FROM agent_status WHERE agent_id = $1`, agentID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "offline" {
		t.Errorf("agent_status after deactivate = %q, want offline", status)
	}
}
