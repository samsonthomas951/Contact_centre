// Package natsx is the thin shim every binary uses to connect to NATS
// JetStream. Every connector / consumer / publisher in the platform
// goes through Connect() so the connection options are uniform: 5s
// max reconnect interval, 30 attempts, persistent retry on disconnect
// (we never want a producer goroutine to give up just because NATS
// blipped during a deploy).
package natsx

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

// Config holds the NATS connection knobs every binary loads from env.
type Config struct {
	URL  string `env:"NATS_URL" default:"nats://nats:4222"`
	Name string `env:"NATS_NAME" default:"contactcentre"`
}

// Connect opens one NATS connection and returns it alongside a
// JetStream handle. The caller is responsible for nc.Drain() on
// shutdown -- typically via a `defer nc.Drain()` next to the
// `defer pool.Close()` in main().
func Connect(ctx context.Context, cfg Config) (*nats.Conn, jetstream.JetStream, error) {
	if cfg.URL == "" {
		return nil, nil, errors.New("natsx: URL required")
	}
	nc, err := nats.Connect(cfg.URL,
		nats.Name(cfg.Name),
		nats.MaxReconnects(-1),                   // forever
		nats.ReconnectWait(2*time.Second),
		nats.PingInterval(20*time.Second),
		nats.MaxPingsOutstanding(3),
		nats.DisconnectErrHandler(func(_ *nats.Conn, err error) {
			slog.WarnContext(ctx, "nats: disconnected",
				slog.String("err", fmt.Sprint(err)))
		}),
		nats.ReconnectHandler(func(c *nats.Conn) {
			slog.InfoContext(ctx, "nats: reconnected",
				slog.String("url", c.ConnectedUrl()))
		}),
		nats.ErrorHandler(func(_ *nats.Conn, _ *nats.Subscription, err error) {
			slog.WarnContext(ctx, "nats: async error",
				slog.String("err", err.Error()))
		}),
	)
	if err != nil {
		return nil, nil, fmt.Errorf("natsx: connect: %w", err)
	}
	js, err := jetstream.New(nc)
	if err != nil {
		_ = nc.Drain()
		return nil, nil, fmt.Errorf("natsx: jetstream: %w", err)
	}
	return nc, js, nil
}

// EnsureStreams creates / updates every stream the platform uses.
// Idempotent: re-running on a fresh JetStream is fine and re-running
// on an established one applies any config drift in our code without
// data loss (CreateOrUpdateStream).
//
// Stream layout:
//
//	INGRESS  ingress.>            — normalised inbound events from
//	                                every channel connector. Consumed
//	                                by the Ticket Service.
//	OUTBOUND outbound.>           — outbound replies the connectors
//	                                pick up to ship via Meta/X/WA/IG/
//	                                voice APIs.
//	AUDIT    audit.events.>       — append-only audit log feed.
//	SLA      sla.>                — risk events from the scheduler.
//	WEBHOOKS webhooks.>           — fan-out for tenant outbound webhooks.
func EnsureStreams(ctx context.Context, js jetstream.JetStream) error {
	specs := []jetstream.StreamConfig{
		{
			Name:        "INGRESS",
			Subjects:    []string{"ingress.>"},
			Retention:   jetstream.InterestPolicy,
			Storage:     jetstream.FileStorage,
			MaxAge:      7 * 24 * time.Hour,
			Description: "Normalised inbound events from every channel connector",
		},
		{
			Name:        "OUTBOUND",
			Subjects:    []string{"outbound.>"},
			Retention:   jetstream.WorkQueuePolicy,    // each event consumed once by one connector
			Storage:     jetstream.FileStorage,
			MaxAge:      24 * time.Hour,
			Description: "Outbound replies, picked up by per-channel sender workers",
		},
		{
			Name:        "AUDIT",
			Subjects:    []string{"audit.events.>"},
			Retention:   jetstream.InterestPolicy,
			Storage:     jetstream.FileStorage,
			MaxAge:      7 * 24 * time.Hour,
			Duplicates:  5 * time.Minute,
			Description: "Append-only audit ledger feed",
		},
		{
			Name:        "SLA",
			Subjects:    []string{"sla.>"},
			Retention:   jetstream.InterestPolicy,
			Storage:     jetstream.FileStorage,
			MaxAge:      7 * 24 * time.Hour,
			Description: "SLA risk events emitted by the scheduler",
		},
		{
			Name:        "WEBHOOKS",
			Subjects:    []string{"webhooks.>"},
			Retention:   jetstream.WorkQueuePolicy,
			Storage:     jetstream.FileStorage,
			MaxAge:      72 * time.Hour,            // gives the retry worker room
			Description: "Tenant outbound webhook fan-out + retry queue",
		},
	}
	for _, s := range specs {
		if _, err := js.CreateOrUpdateStream(ctx, s); err != nil {
			return fmt.Errorf("natsx: ensure stream %s: %w", s.Name, err)
		}
	}
	return nil
}
