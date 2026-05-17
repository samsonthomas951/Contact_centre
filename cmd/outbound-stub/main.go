// Command outbound-stub is the demo-only NATS worker that subscribes
// to outbound.> and writes each event to the outbound_log table
// instead of calling Meta / X / WhatsApp / Instagram / voice.
//
// Production replaces this binary with per-channel Senders (the
// facebook.Sender already exists; X / WA / IG / voice land in
// follow-up commits). The stub proves the round-trip works end-to-end
// without real platform credentials.
//
// Subjects handled:
//
//	outbound.fb.text
//	outbound.x.text
//	outbound.wa.text
//	outbound.ig.text
//	outbound.widget.text
//	outbound.voice.sms
//
// Job payload mirrors what csat.Dispatcher publishes:
//
//	{
//	  "tenant_id":   "...",
//	  "ticket_id":   "...",
//	  "customer_id": "...",
//	  "channel":     "fb",
//	  "body":        "...",
//	  "kind":        "csat_survey" | "agent_reply"
//	}
package main

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/samsonthomas951/contact-centre/internal/pkg/config"
	"github.com/samsonthomas951/contact-centre/internal/pkg/logging"
	"github.com/samsonthomas951/contact-centre/internal/pkg/natsx"
	"github.com/samsonthomas951/contact-centre/internal/pkg/postgres"
)

// version overridden at build time.
var version = "dev"

// job is the on-wire shape (mirrors csat.Dispatcher.queue, ticket
// outbound producers, etc.).
type job struct {
	TenantID   uuid.UUID `json:"tenant_id"`
	TicketID   uuid.UUID `json:"ticket_id"`
	CustomerID uuid.UUID `json:"customer_id"`
	Channel    string    `json:"channel"`
	Body       string    `json:"body"`
	Kind       string    `json:"kind"`
}

func main() {
	if err := run(); err != nil {
		slog.Error("outbound-stub: fatal", slog.String("err", err.Error()))
		os.Exit(1)
	}
}

func run() error {
	logging.Init(logging.Options{Service: "outbound-stub", Version: version, Level: slog.LevelInfo})

	var (
		dbCfg   postgres.Config
		natsCfg natsx.Config
	)
	if err := config.Load("", &dbCfg); err != nil {
		return err
	}
	if err := config.Load("", &natsCfg); err != nil {
		return err
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

	// Channels handled by real per-channel workers must be EXCLUDED
	// from the stub's filter so JetStream's WorkQueuePolicy doesn't
	// reject overlapping consumers. Right now only fb has a real
	// worker (cmd/fb-outbound); when ig/wa/x/voice get real senders
	// they get carved out the same way.
	cons, err := js.CreateOrUpdateConsumer(ctx, "OUTBOUND", jetstream.ConsumerConfig{
		Durable: "outbound-stub",
		FilterSubjects: []string{
			"outbound.x.text",
			"outbound.wa.text",
			"outbound.ig.text",
			"outbound.widget.text",
			"outbound.voice.sms",
		},
		AckPolicy:     jetstream.AckExplicitPolicy,
		MaxAckPending: 16,
		AckWait:       20 * time.Second,
		MaxDeliver:    5,
		DeliverPolicy: jetstream.DeliverAllPolicy,
	})
	if err != nil {
		return err
	}

	iter, err := cons.Messages(jetstream.PullMaxMessages(8))
	if err != nil {
		return err
	}
	defer iter.Stop()

	go func() { <-ctx.Done(); iter.Stop() }()

	slog.InfoContext(ctx, "outbound-stub: ready, subscribed to outbound.{x,wa,ig,widget,voice}")

	for {
		msg, err := iter.Next()
		if errors.Is(err, jetstream.ErrMsgIteratorClosed) {
			return nil
		}
		if err != nil {
			slog.WarnContext(ctx, "outbound-stub: iter next",
				slog.String("err", err.Error()))
			continue
		}
		handle(ctx, pool, msg)
	}
}

// handle decodes one outbound event and persists it to outbound_log.
// On JSON error -> Term (poison stays out of redelivery). On DB
// error -> Nak so JetStream retries.
func handle(ctx context.Context, pool *pgxpool.Pool, msg jetstream.Msg) {
	var j job
	if err := json.Unmarshal(msg.Data(), &j); err != nil {
		slog.ErrorContext(ctx, "outbound-stub: bad job, terminating",
			slog.String("err", err.Error()),
			slog.String("subject", msg.Subject()))
		_ = msg.Term()
		return
	}
	// kind defaults to "agent_reply" when the producer didn't set it
	// (the ticket service's outbound publisher just sends body+ticket).
	if j.Kind == "" {
		j.Kind = "agent_reply"
	}
	// channel can be derived from the subject if not on the body
	// (outbound.<channel>.text -> <channel>).
	if j.Channel == "" {
		parts := splitSubject(msg.Subject())
		if len(parts) >= 2 {
			j.Channel = parts[1]
		}
	}

	if _, err := pool.Exec(ctx, `
		INSERT INTO outbound_log
		  (tenant_id, ticket_id, customer_id, channel, kind, body)
		VALUES ($1,
		        NULLIF($2, '00000000-0000-0000-0000-000000000000'::uuid),
		        NULLIF($3, '00000000-0000-0000-0000-000000000000'::uuid),
		        $4, $5, $6)`,
		j.TenantID, j.TicketID, j.CustomerID, j.Channel, j.Kind, j.Body); err != nil {
		slog.WarnContext(ctx, "outbound-stub: insert, will redeliver",
			slog.String("err", err.Error()))
		_ = msg.Nak()
		return
	}

	slog.InfoContext(ctx, "outbound-stub: shipped",
		slog.String("channel", j.Channel),
		slog.String("kind", j.Kind),
		slog.String("ticket", j.TicketID.String()))
	_ = msg.Ack()
}

func splitSubject(s string) []string {
	out, cur := []string{}, ""
	for _, c := range s {
		if c == '.' {
			out = append(out, cur)
			cur = ""
			continue
		}
		cur += string(c)
	}
	if cur != "" {
		out = append(out, cur)
	}
	return out
}
