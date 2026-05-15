//go:build integration

package webhooks

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

func envOrSkip(t *testing.T, name string) string {
	t.Helper()
	v := os.Getenv(name)
	if v == "" {
		t.Skipf("%s not set", name)
	}
	return v
}

func freshPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	url := envOrSkip(t, "WEBHOOKS_TEST_DB_URL")
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, url)
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
	for _, m := range []string{"0001_init", "0015_webhooks"} {
		b, err := os.ReadFile(filepath.Join(root, m+".up.sql"))
		if err != nil {
			t.Fatalf("read %s: %v", m, err)
		}
		if _, err := pool.Exec(ctx, string(b)); err != nil {
			t.Fatalf("apply %s: %v", m, err)
		}
	}
	return pool
}

func freshJS(t *testing.T) (*nats.Conn, jetstream.JetStream) {
	t.Helper()
	url := envOrSkip(t, "WEBHOOKS_TEST_NATS_URL")
	nc, err := nats.Connect(url, nats.Name("wh-test"))
	if err != nil {
		t.Fatalf("nats: %v", err)
	}
	t.Cleanup(func() { _ = nc.Drain() })
	js, err := jetstream.New(nc)
	if err != nil {
		t.Fatalf("jetstream: %v", err)
	}
	// Fresh stream per test run; recreate ensures no carry-over.
	ctx := context.Background()
	_ = js.DeleteStream(ctx, "WEBHOOKS")
	if _, err := js.CreateStream(ctx, jetstream.StreamConfig{
		Name:      "WEBHOOKS",
		Subjects:  []string{"webhooks.>"},
		Retention: jetstream.WorkQueuePolicy,
		Storage:   jetstream.FileStorage,
		MaxAge:    72 * time.Hour,
	}); err != nil {
		t.Fatalf("create stream: %v", err)
	}
	return nc, js
}

func seedTenant(t *testing.T, ctx context.Context, pool *pgxpool.Pool) uuid.UUID {
	t.Helper()
	id := uuid.New()
	if _, err := pool.Exec(ctx,
		`INSERT INTO tenants(id, slug, display_name) VALUES($1,'acme','Acme')`, id); err != nil {
		t.Fatal(err)
	}
	return id
}

func seedSub(t *testing.T, ctx context.Context, pool *pgxpool.Pool,
	tenantID uuid.UUID, url, secret string, subjects []string) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	if err := pool.QueryRow(ctx, `
		INSERT INTO webhook_subscriptions
		  (tenant_id, url, secret_ct, dek_id, subjects)
		VALUES ($1, $2, $3, 'test-dek', $4)
		RETURNING id`,
		tenantID, url, []byte(secret), subjects).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func TestDispatcher_FanOutThenWorkerDelivers(t *testing.T) {
	ctx := context.Background()
	pool := freshPool(t)
	_, js := freshJS(t)

	tenant := seedTenant(t, ctx, pool)

	var hits atomic.Int32
	var lastSig string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		lastSig = r.Header.Get(SignatureHeader)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	subID := seedSub(t, ctx, pool, tenant, srv.URL, "shh",
		[]string{"ticket.assigned", "ticket.created"})

	d := &Dispatcher{JS: js, Pool: pool, Decoder: PlaintextDecoder{}}
	w := &Worker{JS: js, Pool: pool, Deliverer: NewDeliverer(pool), Decoder: PlaintextDecoder{}}

	// Worker runs in the background.
	wctx, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan struct{})
	go func() { _ = w.Run(wctx); close(done) }()

	// Fan out one matching event.
	body := []byte(`{"ticket_id":"abc"}`)
	if err := d.FanOut(ctx, tenant, "ticket.assigned", "ev1", body, time.Now()); err != nil {
		t.Fatalf("fan-out: %v", err)
	}

	deadline := time.Now().Add(5 * time.Second)
	for hits.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if hits.Load() != 1 {
		t.Fatalf("expected 1 delivery, got %d", hits.Load())
	}
	if want := Sign(body, "shh"); lastSig != want {
		t.Errorf("X-Signature-256 = %q want %q", lastSig, want)
	}

	// One row in webhook_deliveries.
	var deliveries int
	if err := pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM webhook_deliveries WHERE subscription_id = $1`,
		subID).Scan(&deliveries); err != nil {
		t.Fatal(err)
	}
	if deliveries != 1 {
		t.Errorf("delivery rows = %d, want 1", deliveries)
	}

	cancel()
	<-done
}

func TestDispatcher_NonMatchingSubjectIsNoOp(t *testing.T) {
	ctx := context.Background()
	pool := freshPool(t)
	_, js := freshJS(t)
	tenant := seedTenant(t, ctx, pool)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Errorf("non-matching subject should not deliver")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	_ = seedSub(t, ctx, pool, tenant, srv.URL, "shh", []string{"sla.>"})

	d := &Dispatcher{JS: js, Pool: pool, Decoder: PlaintextDecoder{}}
	if err := d.FanOut(ctx, tenant, "ticket.created", "ev2", []byte(`{}`), time.Now()); err != nil {
		t.Fatal(err)
	}

	// Give it a moment to NOT happen.
	time.Sleep(200 * time.Millisecond)
	// And confirm no delivery row landed.
	var n int
	if err := pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM webhook_deliveries`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("non-matching produced %d delivery rows", n)
	}
}
