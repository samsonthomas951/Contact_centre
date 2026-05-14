package ratelimit

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestClientIP_XFF(t *testing.T) {
	cases := map[string]string{
		"203.0.113.5":                   "203.0.113.5",
		"203.0.113.5, 10.0.0.1":         "203.0.113.5",
		" 203.0.113.5 , 10.0.0.1 ":      "203.0.113.5",
	}
	for in, want := range cases {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Header.Set("X-Forwarded-For", in)
		if got := ClientIP(req); got != want {
			t.Errorf("ClientIP(%q) = %q want %q", in, got, want)
		}
	}
}

func TestClientIP_FallsBackToRemoteAddr(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "198.51.100.7:443"
	if got := ClientIP(req); got != "198.51.100.7" {
		t.Errorf("fallback ClientIP = %q want 198.51.100.7", got)
	}
}

func TestMiddleware_AllowsWhenNoRedis(t *testing.T) {
	called := false
	h := Middleware(nil, Bucket{Capacity: 1, RefillPerSec: 1},
		func(*http.Request) string { return "x" })(
		http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
			called = true
		}))
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/", nil))
	if !called {
		t.Error("nil redis should fail-open")
	}
}

func TestTTLMillis(t *testing.T) {
	got := ttlMillis(10, 2.0) // 2x10/2 = 10s -> 10000ms
	if got != 10_000 {
		t.Errorf("ttlMillis(10, 2) = %d want 10000", got)
	}
	if ttlMillis(10, 0) != 60_000 {
		t.Error("zero refill should fall back to 60s")
	}
}
