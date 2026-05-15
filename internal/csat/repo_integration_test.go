//go:build integration

package csat

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
	url := os.Getenv("CSAT_TEST_DB_URL")
	if url == "" {
		t.Skip("CSAT_TEST_DB_URL not set")
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
	if _, err := pool.Exec(ctx,
		`DROP SCHEMA IF EXISTS public CASCADE; CREATE SCHEMA public;`); err != nil {
		t.Fatalf("reset: %v", err)
	}
	_, file, _, _ := runtime.Caller(0)
	root := filepath.Join(filepath.Dir(file), "..", "..", "migrations")
	mods := []string{"0001_init", "0002_agents", "0003_customers", "0004_tickets",
		"0011_analytics", "0016_csat"}
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

func seedTicket(t *testing.T, ctx context.Context, pool *pgxpool.Pool) (
	tenantID, ticketID, customerID uuid.UUID,
) {
	t.Helper()
	tenantID = uuid.New()
	customerID = uuid.New()
	convID := uuid.New()
	ticketID = uuid.New()
	if _, err := pool.Exec(ctx,
		`INSERT INTO tenants(id, slug, display_name) VALUES($1,'acme','Acme')`, tenantID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO customers(id, tenant_id, display_name, email)
		 VALUES($1,$2,'C','c@x')`, customerID, tenantID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO conversations(id, tenant_id, customer_id, channel)
		 VALUES($1,$2,$3,'fb')`, convID, tenantID, customerID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO tickets(id, tenant_id, conversation_id, state, resolved_at)
		 VALUES($1,$2,$3,'resolved'::ticket_state, now())`,
		ticketID, tenantID, convID); err != nil {
		t.Fatal(err)
	}
	return
}

func TestRepo_CreateAndSubmit_Lifecycle(t *testing.T) {
	ctx := context.Background()
	pool := freshPool(t)
	tenant, ticket, customer := seedTicket(t, ctx, pool)

	r := NewRepo(pool)
	survey, token, err := r.Create(ctx, CreateParams{
		TenantID: tenant, TicketID: ticket, CustomerID: customer, Channel: "fb",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if token == "" || survey.Token == "" {
		t.Fatal("token not returned")
	}
	if survey.ExpiresAt.Sub(survey.SentAt) < 6*24*time.Hour {
		t.Errorf("expiry too short: %s", survey.ExpiresAt.Sub(survey.SentAt))
	}

	// Find by token works.
	found, err := r.FindByToken(ctx, token)
	if err != nil {
		t.Fatalf("find: %v", err)
	}
	if found.ID != survey.ID {
		t.Errorf("find returned different survey")
	}

	// Submit a 4-star score with a comment.
	out, err := r.Submit(ctx, SubmitParams{Token: token, Score: 4, Comment: "Quick reply, thanks"})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if out.Score == nil || *out.Score != 4 {
		t.Errorf("score round-trip: %v", out.Score)
	}
	if out.RespondedAt == nil {
		t.Error("responded_at not stamped")
	}

	// Re-submit should return ErrAlreadyResponded.
	_, err = r.Submit(ctx, SubmitParams{Token: token, Score: 5})
	if !errors.Is(err, ErrAlreadyResponded) {
		t.Fatalf("re-submit: want ErrAlreadyResponded, got %v", err)
	}

	// Out-of-range score rejected.
	_, _, _ = r.Create(ctx, CreateParams{
		TenantID: tenant, TicketID: ticket, CustomerID: customer, Channel: "fb",
	})
	if _, err := r.Submit(ctx, SubmitParams{Token: "anything", Score: 6}); err == nil {
		t.Error("score=6 should reject")
	}
}

func TestRepo_TokenNotFoundAndExpired(t *testing.T) {
	ctx := context.Background()
	pool := freshPool(t)
	tenant, ticket, customer := seedTicket(t, ctx, pool)
	r := NewRepo(pool)

	if _, err := r.FindByToken(ctx, "not-a-real-token-xxxx"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}

	// Force-create an expired survey.
	tok := NewToken()
	if _, err := pool.Exec(ctx, `
		INSERT INTO csat_surveys
		  (tenant_id, ticket_id, customer_id, channel, token, expires_at, sent_at)
		VALUES ($1,$2,$3,'fb',$4, now() - interval '1 hour', now() - interval '8 days')`,
		tenant, ticket, customer, tok); err != nil {
		t.Fatal(err)
	}
	if _, err := r.FindByToken(ctx, tok); !errors.Is(err, ErrExpired) {
		t.Fatalf("want ErrExpired, got %v", err)
	}
}
