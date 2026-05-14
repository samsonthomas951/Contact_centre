package correlation

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestNew_FormatAndUniqueness(t *testing.T) {
	a, b := New(), New()
	if len(a) != 32 {
		t.Fatalf("New() length = %d, want 32", len(a))
	}
	if a == b {
		t.Fatalf("New() returned the same value twice: %s", a)
	}
}

func TestContextRoundtrip(t *testing.T) {
	ctx := WithID(context.Background(), "abc123")
	if got := FromContext(ctx); got != "abc123" {
		t.Fatalf("FromContext = %q, want abc123", got)
	}
	if got := FromContext(context.Background()); got != "" {
		t.Fatalf("FromContext on bare ctx = %q, want empty", got)
	}
}

func TestMiddleware_EchoesInbound(t *testing.T) {
	var seen string
	h := Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = FromContext(r.Context())
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set(Header, "inbound-id")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if seen != "inbound-id" {
		t.Errorf("context id = %q, want inbound-id", seen)
	}
	if got := rr.Header().Get(Header); got != "inbound-id" {
		t.Errorf("response header = %q, want inbound-id", got)
	}
}

func TestMiddleware_GeneratesWhenAbsent(t *testing.T) {
	h := Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if got := rr.Header().Get(Header); len(got) != 32 {
		t.Errorf("response header = %q, want a 32-char ID", got)
	}
}
