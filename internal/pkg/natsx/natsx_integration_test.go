//go:build integration

package natsx

import (
	"context"
	"os"
	"testing"
)

func TestEnsureStreams_Idempotent(t *testing.T) {
	url := os.Getenv("NATSX_TEST_URL")
	if url == "" {
		t.Skip("NATSX_TEST_URL not set")
	}
	ctx := context.Background()

	nc, js, err := Connect(ctx, Config{URL: url, Name: "natsx-test"})
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { _ = nc.Drain() })

	// First call creates everything.
	if err := EnsureStreams(ctx, js); err != nil {
		t.Fatalf("ensure (1): %v", err)
	}
	// Second call must be a no-op (CreateOrUpdateStream).
	if err := EnsureStreams(ctx, js); err != nil {
		t.Fatalf("ensure (2): %v", err)
	}

	for _, name := range []string{"INGRESS", "OUTBOUND", "AUDIT", "SLA", "WEBHOOKS"} {
		if _, err := js.Stream(ctx, name); err != nil {
			t.Errorf("stream %s missing after ensure: %v", name, err)
		}
	}
}
