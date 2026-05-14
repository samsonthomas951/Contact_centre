// Command gateway is the public HTTP entry point for the contact centre.
// It owns TLS termination (delegated to Traefik in front), JWT
// verification, request-correlation, and routing to the service
// packages.
//
// Phase 0 wiring:
//
//	GET   /healthz                       liveness probe (no deps)
//	GET   /readyz                        readiness probe (DB reachable)
//	GET   /metrics                       Prometheus exposition
//	GET   /v1/me                         identity from the bearer token
//	POST  /v1/tickets                    create a ticket
//	GET   /v1/tickets/{id}               load a ticket
//	PATCH /v1/tickets/{id}/state         move the ticket to a new state
//	GET   /v1/tickets/{id}/messages      list a ticket's messages
//	POST  /v1/tickets/{id}/messages      append an outbound/note message
//
// Connector webhook intake (e.g. /v1/fb/webhook) registers BEFORE the
// auth middleware in the router stack so the public Meta callback is
// reachable without a bearer token.
package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/samsonthomas951/contact-centre/internal/auth"
	"github.com/samsonthomas951/contact-centre/internal/connector/facebook"
	xconn "github.com/samsonthomas951/contact-centre/internal/connector/x"
	"github.com/samsonthomas951/contact-centre/internal/pkg/config"
	"github.com/samsonthomas951/contact-centre/internal/pkg/correlation"
	"github.com/samsonthomas951/contact-centre/internal/pkg/httpserver"
	"github.com/samsonthomas951/contact-centre/internal/pkg/logging"
	"github.com/samsonthomas951/contact-centre/internal/pkg/postgres"
	"github.com/samsonthomas951/contact-centre/internal/ticket"
)

// version is overridden at build time via -ldflags '-X main.version=...'.
var version = "dev"

func main() {
	if err := run(); err != nil {
		slog.Error("gateway: fatal", slog.String("err", err.Error()))
		os.Exit(1)
	}
}

func run() error {
	logging.Init(logging.Options{
		Service: "gateway",
		Version: version,
		Level:   slog.LevelInfo,
	})

	var (
		httpCfg httpserver.Config
		dbCfg   postgres.Config
		oidcCfg auth.Config
	)
	if err := config.Load("", &httpCfg); err != nil {
		return err
	}
	if err := config.Load("", &dbCfg); err != nil {
		return err
	}
	if err := config.Load("", &oidcCfg); err != nil {
		return err
	}

	ctx := context.Background()
	pool, err := postgres.Connect(ctx, dbCfg)
	if err != nil {
		return err
	}
	defer pool.Close()

	verifier, err := auth.NewVerifier(ctx, oidcCfg)
	if err != nil {
		return err
	}

	// FB webhook is wired up only when the operator supplies the
	// secrets; without them we leave it unmounted and the gateway still
	// serves the rest. Real wiring (NATS, tenant resolver) lands when
	// the dedicated FB connector binary is extracted in a later phase.
	r := newRouter(verifier, ticket.NewRepo(pool), pool, nil, nil)
	return httpserver.Run(ctx, httpCfg, r)
}

// pinger is the readyz contract; pgxpool.Pool satisfies it. Defined as
// an interface so tests can substitute a fake.
type pinger interface {
	Ping(context.Context) error
}

// readyzTimeout caps how long the readiness probe will wait on the DB
// before returning 503.
const readyzTimeout = 2 * time.Second

// newRouter builds the chi tree. Pulled out of run() so it can be
// exercised in tests without touching the network.
func newRouter(v *auth.Verifier, tr *ticket.Repo, p pinger,
	fb *facebook.WebhookHandler, x *xconn.WebhookHandler,
) http.Handler {
	r := chi.NewRouter()

	// Universal middleware: panic recovery, request id, correlation,
	// and a per-request access log entry.
	r.Use(middleware.Recoverer)
	r.Use(middleware.RequestID)
	r.Use(correlation.Middleware)

	// Public probes — reachable without a bearer.
	r.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok\n"))
	})
	r.Get("/readyz", func(w http.ResponseWriter, req *http.Request) {
		ctx, cancel := context.WithTimeout(req.Context(), readyzTimeout)
		defer cancel()
		if err := p.Ping(ctx); err != nil {
			http.Error(w, "db unavailable", http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ready\n"))
	})
	r.Method(http.MethodGet, "/metrics", promhttp.Handler())

	// Public connector webhooks — Meta does not send a bearer; this
	// MUST sit outside the auth-protected /v1 subtree. The handler does
	// its own HMAC verification.
	if fb != nil {
		r.Method(http.MethodGet, "/v1/fb/webhook", fb)
		r.Method(http.MethodPost, "/v1/fb/webhook", fb)
	}
	if x != nil {
		r.Method(http.MethodGet, "/v1/x/webhook", x)
		r.Method(http.MethodPost, "/v1/x/webhook", x)
	}

	// Authenticated v1 surface.
	r.Route("/v1", func(r chi.Router) {
		r.Use(auth.Middleware(v))
		r.Get("/me", auth.MeHandler)
		r.Mount("/tickets", (&ticket.API{Repo: tr}).Routes())
	})

	return r
}
