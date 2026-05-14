//go:build integration

package audit

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// dbURL returns the Postgres URL for integration tests. Tests skip when
// the env var is unset so unit `go test ./...` stays hermetic.
func dbURL(t *testing.T) string {
	t.Helper()
	url := os.Getenv("AUDIT_TEST_DB_URL")
	if url == "" {
		t.Skip("AUDIT_TEST_DB_URL not set")
	}
	return url
}

func setupSchema(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	_, file, _, _ := runtime.Caller(0)
	root := filepath.Join(filepath.Dir(file), "..", "..", "migrations")
	for _, name := range []string{"0001_init", "0002_agents", "0003_customers",
		"0004_tickets", "0005_audit", "0006_connectors"} {
		b, err := os.ReadFile(filepath.Join(root, name+".up.sql"))
		if err != nil {
			t.Fatalf("read migration %s: %v", name, err)
		}
		if _, err := pool.Exec(ctx, string(b)); err != nil {
			t.Fatalf("apply %s: %v", name, err)
		}
	}
}

func freshPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dbURL(t))
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	t.Cleanup(pool.Close)

	// Wipe any prior state from the audit tables — keep migrations idempotent
	// across test runs.
	if _, err := pool.Exec(ctx, `DROP SCHEMA IF EXISTS public CASCADE; CREATE SCHEMA public;`); err != nil {
		t.Fatalf("reset schema: %v", err)
	}
	setupSchema(t, ctx, pool)
	return pool
}

func TestWriter_AppendChainsAndVerifies(t *testing.T) {
	ctx := context.Background()
	pool := freshPool(t)

	tenant := uuid.New()
	if _, err := pool.Exec(ctx, `INSERT INTO tenants(id, slug, display_name) VALUES($1,'acme','Acme')`, tenant); err != nil {
		t.Fatalf("seed tenant: %v", err)
	}

	w := NewWriter(pool)
	if err := w.Bootstrap(ctx); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}

	for i := 0; i < 5; i++ {
		e := Event{
			TenantID:      tenant,
			ActorType:     "system",
			Action:        "test.tick",
			CorrelationID: uuid.New(),
			Payload:       json.RawMessage(`{"i":` + string(rune('0'+i)) + `}`),
		}
		if err := w.Append(ctx, &e); err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
	}

	if err := w.Verify(ctx); err != nil {
		t.Fatalf("verify clean chain: %v", err)
	}

	// Tamper with one row's payload — Verify must catch it.
	if _, err := pool.Exec(ctx,
		`UPDATE audit_events SET payload = '{"i":"hacked"}'::bytea WHERE seq = 3`); err != nil {
		t.Fatalf("tamper: %v", err)
	}
	err := w.Verify(ctx)
	if !errors.Is(err, ErrChainBroken) {
		t.Fatalf("Verify after tamper: want ErrChainBroken, got %v", err)
	}
	if !strings.Contains(err.Error(), "seq=3") {
		t.Errorf("error should name offending seq, got %v", err)
	}
}

func TestWriter_ValidationRejected(t *testing.T) {
	ctx := context.Background()
	pool := freshPool(t)

	w := NewWriter(pool)
	if err := w.Bootstrap(ctx); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	err := w.Append(ctx, &Event{ActorType: "system", Action: "x", CorrelationID: uuid.New()})
	if err == nil || !strings.Contains(err.Error(), "tenant_id") {
		t.Fatalf("want tenant_id validation error, got %v", err)
	}
}
