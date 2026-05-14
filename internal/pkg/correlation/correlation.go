// Package correlation provides correlation-ID generation and propagation.
//
// Every request entering the gateway gets a ULID-based correlation ID
// (lexicographically sortable by timestamp). Internal calls and audit
// rows carry the same ID so a customer interaction reconstructs as one
// timeline per §8 of the technical plan.
package correlation

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"time"

	"github.com/samsonthomas951/contact-centre/internal/pkg/ctxkeys"
)

// Header is the HTTP header used to ferry the correlation ID across hops.
const Header = "X-Correlation-Id"

// New mints a fresh correlation ID. The format is the upper-hex
// encoding of an 8-byte timestamp followed by 8 bytes of entropy, giving
// 32 hex characters that sort lexicographically by time.
func New() string {
	var b [16]byte
	ms := uint64(time.Now().UnixMilli())
	for i := range 8 {
		b[i] = byte(ms >> (56 - 8*i))
	}
	_, _ = rand.Read(b[8:])
	return hex.EncodeToString(b[:])
}

// FromContext returns the correlation ID stored in ctx, or "" if absent.
func FromContext(ctx context.Context) string {
	if v, ok := ctx.Value(ctxkeys.CorrelationID).(string); ok {
		return v
	}
	return ""
}

// WithID returns ctx with the given correlation ID attached.
func WithID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, ctxkeys.CorrelationID, id)
}

// Middleware reads the inbound X-Correlation-Id header (or mints one),
// echoes it on the response, and stores it on the request context.
func Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get(Header)
		if id == "" {
			id = New()
		}
		w.Header().Set(Header, id)
		next.ServeHTTP(w, r.WithContext(WithID(r.Context(), id)))
	})
}
