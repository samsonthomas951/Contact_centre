package auth

import (
	"log/slog"
	"net/http"
	"sync/atomic"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// authFailures counts auth failures per reason. Cardinality is bounded
// (reason ∈ {missing_bearer, malformed, invalid}); a brute-force alert
// rule on this counter fires before an attacker exhausts the verifier.
var authFailures = promauto.NewCounterVec(prometheus.CounterOpts{
	Namespace: "contactcentre",
	Subsystem: "auth",
	Name:      "failures_total",
	Help:      "Authentication failures by reason. Alert when delta > 100 / 5min.",
}, []string{"reason"})

// failuresInWindow counts failures since process start. Used by the
// /metrics endpoint and surfaced verbatim in audit logs so a pentester
// can prove brute-force visibility.
var failuresInWindow atomic.Int64

// recordFailure increments the typed counter, the cumulative atomic,
// and emits an audit-grade log line carrying client IP + correlation
// ID. The audit ledger itself is fed via the audit-events NATS
// pipeline; this log line is the "loud" half so an on-call SRE sees
// failures in Loki immediately.
func recordFailure(r *http.Request, reason string) {
	authFailures.WithLabelValues(reason).Inc()
	failuresInWindow.Add(1)

	slog.WarnContext(r.Context(), "auth: failure",
		slog.String("reason", reason),
		slog.String("client_ip", clientIPFromRequest(r)),
		slog.String("path", r.URL.Path),
	)
}

func clientIPFromRequest(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		for i, c := range xff {
			if c == ',' {
				return xff[:i]
			}
		}
		return xff
	}
	return r.RemoteAddr
}
