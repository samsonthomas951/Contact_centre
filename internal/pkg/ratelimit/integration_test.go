//go:build integration

package ratelimit

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

func redisAddr(t *testing.T) string {
	t.Helper()
	a := os.Getenv("RATELIMIT_TEST_REDIS_ADDR")
	if a == "" {
		t.Skip("RATELIMIT_TEST_REDIS_ADDR not set")
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

func TestAllow_BurstThenRefill(t *testing.T) {
	rdb := newRDB(t)
	ctx := context.Background()
	key := "rl_test:burst"
	_ = rdb.Del(ctx, key).Err()

	b := Bucket{Capacity: 3, RefillPerSec: 10}

	// Drain the bucket immediately.
	for i := 0; i < 3; i++ {
		ok, _, err := Allow(ctx, rdb, key, b)
		if err != nil || !ok {
			t.Fatalf("Allow iter %d: ok=%v err=%v", i, ok, err)
		}
	}
	// Fourth call within the same millisecond should be rejected.
	ok, wait, err := Allow(ctx, rdb, key, b)
	if err != nil || ok {
		t.Fatalf("4th call should reject: ok=%v err=%v", ok, err)
	}
	if wait < 50*time.Millisecond || wait > 200*time.Millisecond {
		t.Errorf("Retry-After window = %s, want ~100ms for refill=10/s", wait)
	}

	// After 200ms (~2 tokens refilled), one call should succeed.
	time.Sleep(250 * time.Millisecond)
	ok, _, err = Allow(ctx, rdb, key, b)
	if err != nil || !ok {
		t.Fatalf("after refill: ok=%v err=%v", ok, err)
	}
}

func TestMiddleware_429WithRetryAfter(t *testing.T) {
	rdb := newRDB(t)
	ctx := context.Background()
	_ = rdb.Del(ctx, "rl:t:1.2.3.4").Err()

	called := 0
	mw := Middleware(rdb, Bucket{Capacity: 1, RefillPerSec: 0.1},
		func(*http.Request) string { return "rl:t:1.2.3.4" })
	h := mw(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called++ }))

	rr1 := httptest.NewRecorder()
	h.ServeHTTP(rr1, httptest.NewRequest(http.MethodGet, "/", nil))
	if rr1.Code != http.StatusOK {
		t.Fatalf("first req: code=%d body=%q", rr1.Code, rr1.Body.String())
	}

	rr2 := httptest.NewRecorder()
	h.ServeHTTP(rr2, httptest.NewRequest(http.MethodGet, "/", nil))
	if rr2.Code != http.StatusTooManyRequests {
		t.Fatalf("second req: code=%d want 429", rr2.Code)
	}
	if rr2.Header().Get("Retry-After") == "" {
		t.Error("missing Retry-After header on 429")
	}
	if called != 1 {
		t.Errorf("downstream called %d times, want 1", called)
	}
}
