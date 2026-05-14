// Package httpserver wraps net/http with the lifecycle helpers every
// service in this repo needs: signal-driven graceful shutdown, hardened
// timeouts, and a single Run() entry point.
package httpserver

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os/signal"
	"syscall"
	"time"
)

// Config tunes the underlying http.Server.
type Config struct {
	Addr              string        `env:"HTTP_ADDR" default:":8080"`
	ReadHeaderTimeout time.Duration `env:"HTTP_READ_HEADER_TIMEOUT" default:"5s"`
	ReadTimeout       time.Duration `env:"HTTP_READ_TIMEOUT" default:"30s"`
	WriteTimeout      time.Duration `env:"HTTP_WRITE_TIMEOUT" default:"30s"`
	IdleTimeout       time.Duration `env:"HTTP_IDLE_TIMEOUT" default:"120s"`
	ShutdownTimeout   time.Duration `env:"HTTP_SHUTDOWN_TIMEOUT" default:"15s"`
}

// Run serves handler on cfg.Addr until the process receives SIGINT or
// SIGTERM, then performs a graceful shutdown bounded by ShutdownTimeout.
// Returns nil on a clean shutdown.
func Run(ctx context.Context, cfg Config, handler http.Handler) error {
	srv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           handler,
		ReadHeaderTimeout: cfg.ReadHeaderTimeout,
		ReadTimeout:       cfg.ReadTimeout,
		WriteTimeout:      cfg.WriteTimeout,
		IdleTimeout:       cfg.IdleTimeout,
	}

	ctx, stop := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	errCh := make(chan error, 1)
	go func() {
		slog.InfoContext(ctx, "http: listening", slog.String("addr", cfg.Addr))
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
		close(errCh)
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		slog.InfoContext(ctx, "http: shutdown signal received")
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		return err
	}
	return nil
}
