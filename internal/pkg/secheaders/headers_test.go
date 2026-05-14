package secheaders

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestMiddleware_SetsAllDefaults(t *testing.T) {
	called := false
	h := Middleware(Defaults())(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		called = true
	}))
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/", nil))

	want := map[string]string{
		"Content-Security-Policy":      "default-src 'none'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'",
		"Strict-Transport-Security":    "max-age=31536000; includeSubDomains; preload",
		"Referrer-Policy":              "no-referrer",
		"X-Content-Type-Options":       "nosniff",
		"X-Frame-Options":              "DENY",
		"Cross-Origin-Opener-Policy":   "same-origin",
		"Cross-Origin-Resource-Policy": "same-origin",
	}
	for k, v := range want {
		if got := rr.Header().Get(k); got != v {
			t.Errorf("%s = %q, want %q", k, got, v)
		}
	}
	if !called {
		t.Error("downstream handler never reached")
	}
}

func TestMiddleware_EmptyOptionsOmitsHeader(t *testing.T) {
	h := Middleware(Options{})(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/", nil))
	if rr.Header().Get("Content-Security-Policy") != "" {
		t.Error("empty option should omit header")
	}
}
