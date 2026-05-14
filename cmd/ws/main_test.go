package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRouter_Healthz(t *testing.T) {
	h := newRouter(nil, nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("got %d", rr.Code)
	}
}

func TestRouter_ReadyzFailsWithoutRedis(t *testing.T) {
	h := newRouter(nil, nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("got %d, want 503 (no hub)", rr.Code)
	}
}

func TestRouter_AgentRequiresAuth(t *testing.T) {
	// Verifier is nil, so the handler short-circuits before calling
	// Verify when no token is supplied — that's a 401.
	h := newRouter(nil, nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/ws/agent", nil))
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("/ws/agent without bearer = %d, want 401", rr.Code)
	}
}
