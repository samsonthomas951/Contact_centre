package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

// stubPinger lets us control readyz behaviour without a real DB.
type stubPinger struct{ err error }

func (s stubPinger) Ping(context.Context) error { return s.err }

func TestRouter_HealthAndReady(t *testing.T) {
	cases := []struct {
		name   string
		path   string
		ping   error
		status int
	}{
		{"healthz always 200", "/healthz", nil, http.StatusOK},
		{"healthz ignores DB", "/healthz", errors.New("db down"), http.StatusOK},
		{"readyz 200 when DB up", "/readyz", nil, http.StatusOK},
		{"readyz 503 when DB down", "/readyz", errors.New("db down"), http.StatusServiceUnavailable},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newRouter(nil, nil, stubPinger{err: tc.ping}, nil, nil)
			rr := httptest.NewRecorder()
			h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, tc.path, nil))
			if rr.Code != tc.status {
				t.Errorf("%s -> %d, want %d (body=%q)", tc.path, rr.Code, tc.status, rr.Body.String())
			}
		})
	}
}

func TestRouter_V1RequiresAuth(t *testing.T) {
	// Passing a nil Verifier is fine — the middleware short-circuits on
	// the missing Authorization header before calling Verify.
	h := newRouter(nil, nil, stubPinger{}, nil, nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/v1/me", nil))
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("/v1/me without bearer = %d, want 401", rr.Code)
	}
}

func TestRouter_MetricsExposed(t *testing.T) {
	h := newRouter(nil, nil, stubPinger{}, nil, nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("/metrics = %d, want 200", rr.Code)
	}
}
