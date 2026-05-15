//go:build integration

package csat

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

func freshJS(t *testing.T) (*nats.Conn, jetstream.JetStream) {
	t.Helper()
	url := os.Getenv("CSAT_TEST_NATS_URL")
	if url == "" {
		t.Skip("CSAT_TEST_NATS_URL not set")
	}
	nc, err := nats.Connect(url, nats.Name("csat-test"))
	if err != nil {
		t.Fatalf("nats: %v", err)
	}
	t.Cleanup(func() { _ = nc.Drain() })
	js, err := jetstream.New(nc)
	if err != nil {
		t.Fatalf("jetstream: %v", err)
	}
	ctx := context.Background()
	_ = js.DeleteStream(ctx, "OUTBOUND")
	if _, err := js.CreateStream(ctx, jetstream.StreamConfig{
		Name:      "OUTBOUND",
		Subjects:  []string{"outbound.>"},
		Retention: jetstream.WorkQueuePolicy,
		Storage:   jetstream.FileStorage,
		MaxAge:    24 * time.Hour,
	}); err != nil {
		t.Fatalf("create OUTBOUND: %v", err)
	}
	return nc, js
}

func TestDispatcher_OnResolvedCreatesSurveyAndQueuesOutbound(t *testing.T) {
	ctx := context.Background()
	pool := freshPool(t)
	tenant, ticket, _ := seedTicket(t, ctx, pool)
	_, js := freshJS(t)

	d := NewDispatcher(js, pool, NewRepo(pool), "https://app.example/csat")

	// Subscribe to the outbound subject *before* OnResolved so the
	// expected message lands in our local subscription.
	consumer, err := js.CreateConsumer(ctx, "OUTBOUND", jetstream.ConsumerConfig{
		Durable:       "test-collector",
		FilterSubject: "outbound.fb.text",
		AckPolicy:     jetstream.AckExplicitPolicy,
	})
	if err != nil {
		t.Fatalf("test consumer: %v", err)
	}

	if err := d.OnResolved(ctx, tenant, ticket); err != nil {
		t.Fatalf("on resolved: %v", err)
	}

	// Survey row exists.
	var surveys int
	if err := pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM csat_surveys WHERE tenant_id = $1 AND ticket_id = $2`,
		tenant, ticket).Scan(&surveys); err != nil {
		t.Fatal(err)
	}
	if surveys != 1 {
		t.Fatalf("survey rows = %d, want 1", surveys)
	}

	// Outbound message exists.
	msgs, err := consumer.Fetch(1, jetstream.FetchMaxWait(2*time.Second))
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	collected := 0
	for m := range msgs.Messages() {
		var job struct {
			Kind    string `json:"kind"`
			Channel string `json:"channel"`
			Body    string `json:"body"`
		}
		if err := json.Unmarshal(m.Data(), &job); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if job.Kind != "csat_survey" || job.Channel != "fb" {
			t.Errorf("job = %+v, want kind=csat_survey channel=fb", job)
		}
		if job.Body == "" {
			t.Error("empty body")
		}
		_ = m.Ack()
		collected++
	}
	if err := msgs.Error(); err != nil && !errors.Is(err, jetstream.ErrNoMessages) {
		t.Errorf("msgs.Error: %v", err)
	}
	if collected != 1 {
		t.Errorf("collected %d outbound jobs, want 1", collected)
	}

	// Idempotent: a second OnResolved doesn't double-create or
	// double-publish.
	if err := d.OnResolved(ctx, tenant, ticket); err != nil {
		t.Fatalf("on resolved (2): %v", err)
	}
	if err := pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM csat_surveys WHERE tenant_id = $1 AND ticket_id = $2`,
		tenant, ticket).Scan(&surveys); err != nil {
		t.Fatal(err)
	}
	if surveys != 1 {
		t.Errorf("after second call survey rows = %d, want 1 (idempotent)", surveys)
	}
}
