// Command ws is the standalone realtime WebSocket service. Per §10 of
// the technical plan this can scale horizontally without sticky
// sessions because session state lives in Redis (the plan quotes
// WebSocket.org: "any server can handle any reconnecting client").
//
// Routes:
//
//	GET /healthz           liveness
//	GET /readyz            readiness (Redis ping)
//	GET /ws/agent          authenticated WebSocket upgrade
//	GET /metrics           Prometheus exposition
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
	"github.com/redis/go-redis/v9"

	"github.com/samsonthomas951/contact-centre/internal/auth"
	"github.com/samsonthomas951/contact-centre/internal/pkg/config"
	"github.com/samsonthomas951/contact-centre/internal/pkg/correlation"
	"github.com/samsonthomas951/contact-centre/internal/pkg/httpserver"
	"github.com/samsonthomas951/contact-centre/internal/pkg/logging"
	"github.com/samsonthomas951/contact-centre/internal/realtime"
)

var version = "dev"

type wsConfig struct {
	HTTP  httpserver.Config
	OIDC  auth.Config
	Redis redisCfg
}

type redisCfg struct {
	Addr     string `env:"REDIS_ADDR" default:"redis:6379"`
	Password string `env:"REDIS_PASSWORD"`
	DB       int    `env:"REDIS_DB" default:"0"`
}

const readyzTimeout = 1 * time.Second

func main() {
	if err := run(); err != nil {
		slog.Error("ws: fatal", slog.String("err", err.Error()))
		os.Exit(1)
	}
}

func run() error {
	logging.Init(logging.Options{Service: "ws", Version: version, Level: slog.LevelInfo})

	var cfg wsConfig
	if err := config.Load("", &cfg.HTTP); err != nil {
		return err
	}
	if err := config.Load("", &cfg.OIDC); err != nil {
		return err
	}
	if err := config.Load("", &cfg.Redis); err != nil {
		return err
	}

	ctx := context.Background()
	rdb := redis.NewClient(&redis.Options{
		Addr: cfg.Redis.Addr, Password: cfg.Redis.Password, DB: cfg.Redis.DB,
	})
	defer rdb.Close()

	verifier, err := auth.NewVerifier(ctx, cfg.OIDC)
	if err != nil {
		return err
	}

	hub := realtime.NewHub(rdb)
	defer hub.Close()

	r := newRouter(verifier, hub)
	return httpserver.Run(ctx, cfg.HTTP, r)
}

// newRouter wires the WS service's chi tree. Pulled out for testability.
func newRouter(v *auth.Verifier, hub *realtime.Hub) http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.Recoverer, middleware.RequestID, correlation.Middleware)

	r.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok\n"))
	})
	r.Get("/readyz", func(w http.ResponseWriter, req *http.Request) {
		ctx, cancel := context.WithTimeout(req.Context(), readyzTimeout)
		defer cancel()
		if err := hub.HealthCheck(ctx); err != nil {
			http.Error(w, "redis unavailable", http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte("ready\n"))
	})
	r.Method(http.MethodGet, "/metrics", promhttp.Handler())

	r.Method(http.MethodGet, "/ws/agent", &realtime.AgentSocketHandler{
		Hub: hub, Verifier: v,
	})

	return r
}
