package auth

import (
	"net/http"
	"strings"
)

// Middleware extracts the bearer token, verifies it with v, and stows
// the resulting Identity on the request context. Unauthenticated
// requests are rejected with 401 — public endpoints (webhook intake,
// health probes) must be registered *before* this middleware in the
// router stack.
//
// Every failure path records a typed Prometheus counter and a
// structured warn log with the client IP via recordFailure() in
// audit.go -- this is the "loud half" of the auth-failure trail. The
// durable half goes through the audit-events NATS pipeline.
func Middleware(v *Verifier) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			raw := bearerToken(r)
			if raw == "" {
				recordFailure(r, "missing_bearer")
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			id, err := v.Verify(r.Context(), raw)
			if err != nil {
				recordFailure(r, "invalid")
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			next.ServeHTTP(w, r.WithContext(withIdentity(r.Context(), id)))
		})
	}
}

func bearerToken(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if h == "" {
		return ""
	}
	const prefix = "Bearer "
	if !strings.HasPrefix(h, prefix) {
		return ""
	}
	return strings.TrimSpace(h[len(prefix):])
}
