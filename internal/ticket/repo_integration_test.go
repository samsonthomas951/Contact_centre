//go:build integration

package ticket

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
	url := os.Getenv("TICKET_TEST_DB_URL")
	if url == "" {
		t.Skip("TICKET_TEST_DB_URL not set")
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
	// 0020_tags is in the list because Repo.List joins ticket_tags
	// in its tag-rollup CTE; without the table the integration tests
	// would all fail with "relation ticket_tags does not exist".
	for _, name := range []string{"0001_init", "0002_agents", "0003_customers", "0004_tickets", "0020_tags"} {
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

func seedTicket(t *testing.T, ctx context.Context, pool *pgxpool.Pool) (tenant, conv uuid.UUID) {
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
		`INSERT INTO conversations(id, tenant_id, customer_id, channel, channel_thread_id)
		 VALUES($1,$2,$3,'fb','thread-1')`,
		conv, tenant, customer); err != nil {
		t.Fatal(err)
	}
	return tenant, conv
}

func TestRepo_TicketLifecycle(t *testing.T) {
	ctx := context.Background()
	pool := freshPool(t)
	tenant, conv := seedTicket(t, ctx, pool)

	r := NewRepo(pool)
	tk, err := r.CreateTicket(ctx, CreateTicketParams{
		TenantID: tenant, ConversationID: conv,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if tk.State != StateNew {
		t.Errorf("initial state = %s, want new", tk.State)
	}

	// new -> closed must fail
	if _, err := r.ChangeState(ctx, tenant, tk.ID, StateClosed); err == nil {
		t.Fatal("new->closed should be rejected")
	}

	// new -> open -> resolved -> closed -> reopened -> open
	want := []State{StateOpen, StateResolved, StateClosed, StateReopened, StateOpen}
	for _, s := range want {
		got, err := r.ChangeState(ctx, tenant, tk.ID, s)
		if err != nil {
			t.Fatalf("transition to %s: %v", s, err)
		}
		if got.State != s {
			t.Fatalf("after transition state=%s, want %s", got.State, s)
		}
	}

	// resolved_at and closed_at should now both be set on the *previous*
	// resolved/closed, even after reopening.
	got, err := r.GetTicket(ctx, tenant, tk.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.ResolvedAt == nil {
		t.Error("resolved_at should remain set after reopen")
	}
	if got.ClosedAt == nil {
		t.Error("closed_at should remain set after reopen")
	}
}

func TestRepo_GetTicketNotFound(t *testing.T) {
	ctx := context.Background()
	pool := freshPool(t)
	r := NewRepo(pool)
	_, err := r.GetTicket(ctx, uuid.New(), uuid.New())
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

func TestRepo_AppendMessageStampsFirstResponse(t *testing.T) {
	ctx := context.Background()
	pool := freshPool(t)
	tenant, conv := seedTicket(t, ctx, pool)
	r := NewRepo(pool)

	agent := uuid.New()
	if _, err := pool.Exec(ctx,
		`INSERT INTO agents(id, tenant_id, email, display_name) VALUES($1,$2,'a@x','A')`,
		agent, tenant); err != nil {
		t.Fatal(err)
	}

	tk, err := r.CreateTicket(ctx, CreateTicketParams{TenantID: tenant, ConversationID: conv})
	if err != nil {
		t.Fatal(err)
	}

	// Inbound message — first_response_at must remain nil.
	if _, err := r.AppendMessage(ctx, AppendMessageParams{
		TenantID: tenant, TicketID: tk.ID, Direction: DirectionIn, Body: "hi",
	}); err != nil {
		t.Fatal(err)
	}
	got, _ := r.GetTicket(ctx, tenant, tk.ID)
	if got.FirstResponseAt != nil {
		t.Error("first_response_at set after inbound message")
	}

	// Agent outbound — should stamp first_response_at.
	if _, err := r.AppendMessage(ctx, AppendMessageParams{
		TenantID: tenant, TicketID: tk.ID, Direction: DirectionOut,
		AgentID: &agent, Body: "thanks",
	}); err != nil {
		t.Fatal(err)
	}
	got, _ = r.GetTicket(ctx, tenant, tk.ID)
	if got.FirstResponseAt == nil {
		t.Fatal("first_response_at not stamped after agent outbound")
	}
	stamped := *got.FirstResponseAt

	// Second outbound — first_response_at should NOT change.
	if _, err := r.AppendMessage(ctx, AppendMessageParams{
		TenantID: tenant, TicketID: tk.ID, Direction: DirectionOut,
		AgentID: &agent, Body: "still here",
	}); err != nil {
		t.Fatal(err)
	}
	got, _ = r.GetTicket(ctx, tenant, tk.ID)
	if !got.FirstResponseAt.Equal(stamped) {
		t.Errorf("first_response_at moved: was %v, now %v", stamped, *got.FirstResponseAt)
	}
}
