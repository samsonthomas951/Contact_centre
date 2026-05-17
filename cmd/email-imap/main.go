// Command email-imap is the periodic IMAP poller. Iterates every
// active mailbox that has IMAP credentials, fetches messages with
// UID > the per-mailbox checkpoint, normalises each, and publishes
// ingress.email.message. Dedupe is by Message-ID (shared with the
// webhook path) so the same message arriving twice is harmless.
//
// Per-mailbox cadence is controlled by EMAIL_IMAP_INTERVAL_SECS
// (default 30s); batch size by EMAIL_IMAP_BATCH (default 50).
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/samsonthomas951/contact-centre/internal/connector/email"
	"github.com/samsonthomas951/contact-centre/internal/connector/facebook"
	"github.com/samsonthomas951/contact-centre/internal/pkg/config"
	"github.com/samsonthomas951/contact-centre/internal/pkg/logging"
	"github.com/samsonthomas951/contact-centre/internal/pkg/natsx"
	"github.com/samsonthomas951/contact-centre/internal/pkg/postgres"
)

var version = "dev"

type cfg struct {
	DemoKey      string `env:"FB_DEMO_KEY"             default:"demo-only-do-not-use-in-prod"`
	IntervalSecs int    `env:"EMAIL_IMAP_INTERVAL_SECS" default:"30"`
	BatchSize    int    `env:"EMAIL_IMAP_BATCH"         default:"50"`
}

func main() {
	if err := run(); err != nil {
		slog.Error("email-imap: fatal", slog.String("err", err.Error()))
		os.Exit(1)
	}
}

func run() error {
	logging.Init(logging.Options{Service: "email-imap", Version: version, Level: slog.LevelInfo})

	var (
		dbCfg   postgres.Config
		natsCfg natsx.Config
		myCfg   cfg
	)
	for _, dst := range []any{&dbCfg, &natsCfg, &myCfg} {
		if err := config.Load("", dst); err != nil {
			return err
		}
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	pool, err := postgres.Connect(ctx, dbCfg)
	if err != nil {
		return err
	}
	defer pool.Close()

	nc, js, err := natsx.Connect(ctx, natsCfg)
	if err != nil {
		return err
	}
	defer func() { _ = nc.Drain() }()
	if err := natsx.EnsureStreams(ctx, js); err != nil {
		return err
	}

	crypter, err := facebook.NewDemoCrypter(myCfg.DemoKey)
	if err != nil {
		return fmt.Errorf("crypter: %w", err)
	}
	store := email.NewMailboxStore(pool, crypter)

	p := &email.Poller{
		Store: store,
		JS:    js,
		Cfg: email.PollerConfig{
			Interval:  time.Duration(myCfg.IntervalSecs) * time.Second,
			BatchSize: uint32(myCfg.BatchSize),
		},
	}
	slog.InfoContext(ctx, "email-imap: starting",
		slog.Int("interval_secs", myCfg.IntervalSecs),
		slog.Int("batch_size", myCfg.BatchSize))
	return p.Run(ctx)
}
