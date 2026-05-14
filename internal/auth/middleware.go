package auth

import (
	"log/slog"
	"net/http"
	"strings"
)

// Middleware extracts the bearer token, verifies it with v, and stows
// the resulting Identity on the request context. Unauthenticated
// requests are rejected with 401 — public endpoints (webhook intake,
// health probes) must be registered *before* this middleware in the
// router stack.
func Middleware(v *Verifier) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			raw := bearerToken(r)
			if raw == "" {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			id, err := v.Verify(r.Context(), raw)
			if err != nil {
				slog.WarnContext(r.Context(), "auth: token rejected",
					slog.String("err", err.Error()))
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
