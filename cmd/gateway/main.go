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

	"github.com/samsonthomas951/contact-centre/internal/analytics"
	"github.com/samsonthomas951/contact-centre/internal/auth"
	"github.com/samsonthomas951/contact-centre/internal/connector/facebook"
	"github.com/samsonthomas951/contact-centre/internal/connector/instagram"
	"github.com/samsonthomas951/contact-centre/internal/connector/voice"
	"github.com/samsonthomas951/contact-centre/internal/connector/whatsapp"
	"github.com/samsonthomas951/contact-centre/internal/connector/widget"
	xconn "github.com/samsonthomas951/contact-centre/internal/connector/x"
	"github.com/samsonthomas951/contact-centre/internal/document"
	"github.com/samsonthomas951/contact-centre/internal/dsr"
	"github.com/samsonthomas951/contact-centre/internal/onboarding"
	"github.com/samsonthomas951/contact-centre/internal/pkg/config"
	"github.com/samsonthomas951/contact-centre/internal/supervisor"
	"github.com/samsonthomas951/contact-centre/internal/pkg/correlation"
	"github.com/samsonthomas951/contact-centre/internal/pkg/httpserver"
	"github.com/samsonthomas951/contact-centre/internal/pkg/logging"
	"github.com/samsonthomas951/contact-centre/internal/pkg/postgres"
	"github.com/samsonthomas951/contact-centre/internal/pkg/secheaders"
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
	r := newRouter(routerDeps{
		Verifier:   verifier,
		Tickets:    ticket.NewRepo(pool),
		Pinger:     pool,
		Supervisor: &supervisor.API{Repo: supervisor.NewRepo(pool)},
		Analytics:  &analytics.API{M: analytics.New(pool)},
		Onboarding: &onboarding.API{
			Tenants: onboarding.NewTenantRepo(pool),
			Agents:  onboarding.NewAgentRepo(pool),
		},
		DSR: &dsr.API{Repo: dsr.NewRepo(pool)},
		// JS is left nil so the widget handler 5xx's when JetStream is
		// not wired; production assignment lands when the connector
		// gets its own binary.
		Widget: &widget.WebsocketHandler{
			Sites:    widget.NewPGSiteLookup(pool),
			Visitors: widget.NewPGVisitorStore(pool),
		},
	})
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

// routerDeps groups everything newRouter wires together. Nullable
// fields skip the corresponding mount; that keeps tests trivial and
// lets the binary boot without optional dependencies (FB credentials,
// MinIO, etc.) wired up.
type routerDeps struct {
	Verifier   *auth.Verifier
	Tickets    *ticket.Repo
	Pinger     pinger
	FB         *facebook.WebhookHandler
	X          *xconn.WebhookHandler
	WA         *whatsapp.WebhookHandler
	IG         *instagram.WebhookHandler
	Widget     *widget.WebsocketHandler
	Voice      *voice.WebhookHandler
	Docs       *document.API
	Supervisor *supervisor.API
	Analytics  *analytics.API
	Onboarding *onboarding.API
	DSR        *dsr.API
}

// newRouter builds the chi tree. Pulled out of run() so it can be
// exercised in tests without touching the network.
func newRouter(d routerDeps) http.Handler {
	v, tr, p, fb, x, wa, ig, wg, vc, docs, sup, ana, onb, ds :=
		d.Verifier, d.Tickets, d.Pinger, d.FB, d.X, d.WA, d.IG, d.Widget,
		d.Voice, d.Docs, d.Supervisor, d.Analytics, d.Onboarding, d.DSR
	r := chi.NewRouter()

	// Universal middleware: panic recovery, request id, correlation,
	// hardened security headers (HSTS, CSP, COOP, etc.). Per the
	// docs/security/pentest-readiness.md checklist.
	r.Use(middleware.Recoverer)
	r.Use(middleware.RequestID)
	r.Use(correlation.Middleware)
	r.Use(secheaders.Middleware(secheaders.Defaults()))

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
	if wa != nil {
		r.Method(http.MethodGet, "/v1/wa/webhook", wa)
		r.Method(http.MethodPost, "/v1/wa/webhook", wa)
	}
	if ig != nil {
		r.Method(http.MethodGet, "/v1/ig/webhook", ig)
		r.Method(http.MethodPost, "/v1/ig/webhook", ig)
	}
	if wg != nil {
		// Widget WS lives at /ws/widget (no /v1 prefix) so the JS
		// snippet can hardcode the path. The connector validates the
		// Origin header against widget_sites itself; no bearer.
		r.Method(http.MethodGet, "/ws/widget", wg)
	}
	if vc != nil {
		// Carrier callback for the voice connector. The {secret} path
		// param is the per-tenant rotating value stored on
		// voice_numbers. Africa's Talking posts form-encoded.
		r.Method(http.MethodPost, "/v1/voice/at/{secret}", vc)
	}

	// Authenticated v1 surface.
	r.Route("/v1", func(r chi.Router) {
		r.Use(auth.Middleware(v))
		r.Get("/me", auth.MeHandler)
		r.Mount("/tickets", (&ticket.API{Repo: tr}).Routes())
		if docs != nil {
			r.Mount("/documents", docs.Routes())
		}
		if sup != nil {
			r.Mount("/supervisor", sup.Routes())
		}
		if ana != nil {
			r.Mount("/analytics", ana.Routes())
		}
		if onb != nil {
			r.Mount("/onboarding", onb.Routes())
		}
		if ds != nil {
			r.Mount("/dsr", ds.Routes())
		}
	})

	return r
}
