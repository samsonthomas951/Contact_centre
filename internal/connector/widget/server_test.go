package widget

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

type stubSites map[string]*Site

func (s stubSites) ResolveSite(_ context.Context, origin string) (*Site, bool, error) {
	v, ok := s[origin]
	return v, ok, nil
}

func TestServeHTTP_RejectsMissingOrigin(t *testing.T) {
	h := &WebsocketHandler{Sites: stubSites{}}
	req := httptest.NewRequest(http.MethodGet, "/ws/widget", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("missing Origin: code=%d want 400", rr.Code)
	}
}

func TestServeHTTP_RejectsUnknownOrigin(t *testing.T) {
	h := &WebsocketHandler{Sites: stubSites{}}
	req := httptest.NewRequest(http.MethodGet, "/ws/widget", nil)
	req.Header.Set("Origin", "https://attacker.example")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("unknown origin: code=%d want 403", rr.Code)
	}
}
