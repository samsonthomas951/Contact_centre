// Package ratelimit is a per-key token-bucket limiter backed by Redis.
//
// The bucket is sized in (capacity, refill-per-second) and the script
// is atomic via EVAL so two replicas can't collude to over-spend. Per
// §9 of the technical plan we cite token-bucket via redis_rate as the
// production pick; this file is the equivalent without dragging in
// the third-party library so the pkg layer stays small.
//
// Keys are caller-supplied: typically "rl:<endpoint>:<ip>" for
// unauthenticated surfaces and "rl:<endpoint>:<agent_id>" for
// authenticated ones.
package ratelimit

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

// Bucket configures one limiter instance.
type Bucket struct {
	// Capacity is the burst size. Requests beyond capacity wait or
	// reject (depending on the caller).
	Capacity int
	// Refill is the steady-state rate (tokens per second).
	RefillPerSec float64
}

// Allow returns (allowed, retryAfter). When allowed is false, the
// caller should write 429 with Retry-After: retryAfter.
//
// The Lua script keeps both fields under a single key:
//
//	HSET <key> tokens <count> updated <unix_ms>
//
// On each call it (a) refills based on elapsed time, (b) clamps to
// capacity, (c) deducts one token if available, (d) sets a TTL on the
// key so idle buckets evict themselves.
func Allow(ctx context.Context, rdb *redis.Client, key string, b Bucket) (bool, time.Duration, error) {
	if rdb == nil {
		return true, 0, nil // no limiter configured -> allow
	}
	now := time.Now().UnixMilli()
	res, err := allowScript.Run(ctx, rdb, []string{key},
		b.Capacity, b.RefillPerSec, now,
		ttlMillis(b.Capacity, b.RefillPerSec),
	).Result()
	if err != nil {
		return false, 0, err
	}
	arr, ok := res.([]any)
	if !ok || len(arr) < 2 {
		return false, 0, errors.New("ratelimit: bad reply shape")
	}
	allowed, _ := arr[0].(int64)
	waitMs, _ := arr[1].(int64)
	return allowed == 1, time.Duration(waitMs) * time.Millisecond, nil
}

// ttlMillis returns the lifetime we set on an idle bucket key. Two
// times the time to fully refill -- after that the bucket is at
// capacity and a fresh init is functionally the same as the saved one.
func ttlMillis(capacity int, refill float64) int64 {
	if refill <= 0 {
		return 60_000
	}
	return int64(2.0 * float64(capacity) / refill * 1000.0)
}

var allowScript = redis.NewScript(`
local key       = KEYS[1]
local capacity  = tonumber(ARGV[1])
local refill    = tonumber(ARGV[2])  -- tokens per second
local now_ms    = tonumber(ARGV[3])
local ttl_ms    = tonumber(ARGV[4])

local data = redis.call("HMGET", key, "tokens", "updated")
local tokens = tonumber(data[1])
local updated = tonumber(data[2])

if tokens == nil then
  tokens = capacity
  updated = now_ms
end

local delta_ms = now_ms - updated
if delta_ms < 0 then delta_ms = 0 end
tokens = math.min(capacity, tokens + (delta_ms / 1000.0) * refill)

local allowed = 0
local wait_ms = 0
if tokens >= 1 then
  tokens = tokens - 1
  allowed = 1
else
  -- Time to accumulate one full token.
  wait_ms = math.ceil((1 - tokens) / refill * 1000.0)
end

redis.call("HMSET", key, "tokens", tokens, "updated", now_ms)
redis.call("PEXPIRE", key, ttl_ms)
return {allowed, wait_ms}
`)

// MiddlewareKeyFunc derives the rate-limit key from a request.
type MiddlewareKeyFunc func(*http.Request) string

// Middleware returns an HTTP middleware that limits requests per the
// supplied bucket. The key function controls grouping; common forms:
//
//	rl:<route>:<client_ip>            // anonymous
//	rl:<route>:<agent_id>             // authenticated
//
// On rejection we set Retry-After (seconds, rounded up) and respond
// with 429.
func Middleware(rdb *redis.Client, b Bucket, keyFunc MiddlewareKeyFunc) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ok, wait, err := Allow(r.Context(), rdb, keyFunc(r), b)
			if err != nil {
				// Fail-open on Redis blip: log via the caller's
				// middleware. The downstream handler still runs.
				next.ServeHTTP(w, r)
				return
			}
			if !ok {
				secs := max(int(wait.Seconds()), 1)
				w.Header().Set("Retry-After", strconv.Itoa(secs))
				http.Error(w, "rate limited", http.StatusTooManyRequests)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// ClientIP is a default keyFunc helper that pulls the first IP from
// X-Forwarded-For (set by Traefik) and falls back to RemoteAddr.
func ClientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		if i := strings.IndexByte(xff, ','); i > 0 {
			return strings.TrimSpace(xff[:i])
		}
		return strings.TrimSpace(xff)
	}
	host := r.RemoteAddr
	if i := strings.LastIndexByte(host, ':'); i > 0 {
		return host[:i]
	}
	return host
}
