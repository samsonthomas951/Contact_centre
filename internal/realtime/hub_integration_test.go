//go:build integration

package realtime

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

func redisAddr(t *testing.T) string {
	t.Helper()
	a := os.Getenv("REALTIME_TEST_REDIS_ADDR")
	if a == "" {
		t.Skip("REALTIME_TEST_REDIS_ADDR not set")
	}
	return a
}

func newRDB(t *testing.T) *redis.Client {
	t.Helper()
	rdb := redis.NewClient(&redis.Options{Addr: redisAddr(t)})
	if err := rdb.Ping(context.Background()).Err(); err != nil {
		t.Fatalf("ping: %v", err)
	}
	t.Cleanup(func() { _ = rdb.Close() })
	return rdb
}

func TestHub_PublishFanOut(t *testing.T) {
	rdb := newRDB(t)
	hub := NewHub(rdb)
	defer hub.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ch1 := hub.Subscribe(ctx, AgentChannel("a-1"))
	ch2 := hub.Subscribe(ctx, AgentChannel("a-1"))

	// Give the Redis subscribe a moment to be live before we publish.
	time.Sleep(150 * time.Millisecond)

	want := []byte(`{"hello":"world"}`)
	if err := hub.Publish(ctx, AgentChannel("a-1"), want); err != nil {
		t.Fatal(err)
	}

	for _, ch := range []<-chan []byte{ch1, ch2} {
		select {
		case got := <-ch:
			if string(got) != string(want) {
				t.Errorf("got %q want %q", got, want)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("subscriber didn't receive within 2s")
		}
	}
}

func TestHub_UnsubscribeOnContextCancel(t *testing.T) {
	rdb := newRDB(t)
	hub := NewHub(rdb)
	defer hub.Close()

	ctx, cancel := context.WithCancel(context.Background())
	ch := hub.Subscribe(ctx, AgentChannel("ephemeral"))
	cancel()

	// After cancel the channel should close (sender closes it on
	// unsubscribe). We allow up to 500ms for the goroutine to run.
	deadline := time.After(500 * time.Millisecond)
	for {
		select {
		case _, ok := <-ch:
			if !ok {
				return // closed -> success
			}
		case <-deadline:
			t.Fatal("subscriber not closed after context cancel")
		}
	}
}
