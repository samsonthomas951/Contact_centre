// Command wa-outbound is the production-shaped WhatsApp Cloud API
// outbound worker. Subscribes to outbound.wa.text on JetStream, loads
// the WA phone_number_id + recipient WAID for the ticket, and ships
// via whatsapp.Sender.SendText.
//
// Only handles text replies inside the 24h customer window for now.
// Template sends (required outside the window) land in a follow-up
// commit -- the agent UI's composer needs to surface template
// selection first.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/samsonthomas951/contact-centre/internal/connector/facebook"
	"github.com/samsonthomas951/contact-centre/internal/connector/whatsapp"
	"github.com/samsonthomas951/contact-centre/internal/pkg/config"
	"github.com/samsonthomas951/contact-centre/internal/pkg/logging"
	"github.com/samsonthomas951/contact-centre/internal/pkg/natsx"
	"github.com/samsonthomas951/contact-centre/internal/pkg/postgres"
)

var version = "dev"

type job struct {
	TenantID   uuid.UUID `json:"tenant_id"`
	TicketID   uuid.UUID `json:"ticket_id"`
	CustomerID uuid.UUID `json:"customer_id"`
	Channel    string    `json:"channel"`
	Body       string    `json:"body"`
	Kind       string    `json:"kind"`
}

type waCfg struct {
	DemoKey      string `env:"FB_DEMO_KEY"      default:"demo-only-do-not-use-in-prod"`
	GraphHost    string `env:"FB_GRAPH_HOST"    default:"https://graph.facebook.com"`
	GraphVersion string `env:"FB_GRAPH_VERSION" default:"v22.0"`
}

func main() {
	if err := run(); err != nil {
		slog.Error("wa-outbound: fatal", slog.String("err", err.Error()))
		os.Exit(1)
	}
}

func run() error {
	logging.Init(logging.Options{Service: "wa-outbound", Version: version, Level: slog.LevelInfo})

	var (
		dbCfg   postgres.Config
		natsCfg natsx.Config
		cfg     waCfg
	)
	for _, dst := range []any{&dbCfg, &natsCfg, &cfg} {
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

	crypter, err := facebook.NewDemoCrypter(cfg.DemoKey)
	if err != nil {
		return fmt.Errorf("wa-outbound: crypter: %w", err)
	}
	store := whatsapp.NewPGNumberStore(pool, crypter)
	sender := whatsapp.NewSender(store)
	sender.GraphHost = cfg.GraphHost
	sender.GraphVersion = cfg.GraphVersion

	cons, err := js.CreateOrUpdateConsumer(ctx, "OUTBOUND", jetstream.ConsumerConfig{
		Durable:       "wa-outbound",
		FilterSubject: "outbound.wa.text",
		AckPolicy:     jetstream.AckExplicitPolicy,
		MaxAckPending: 8,
		AckWait:       30 * time.Second,
		MaxDeliver:    5,
		DeliverPolicy: jetstream.DeliverAllPolicy,
	})
	if err != nil {
		return err
	}
	iter, err := cons.Messages(jetstream.PullMaxMessages(4))
	if err != nil {
		return err
	}
	defer iter.Stop()
	go func() { <-ctx.Done(); iter.Stop() }()

	slog.InfoContext(ctx, "wa-outbound: ready, subscribed to outbound.wa.text")

	for {
		msg, err := iter.Next()
		if errors.Is(err, jetstream.ErrMsgIteratorClosed) {
			return nil
		}
		if err != nil {
			slog.WarnContext(ctx, "wa-outbound: iter next",
				slog.String("err", err.Error()))
			continue
		}
		handle(ctx, pool, sender, msg)
	}
}

type route struct {
	PhoneNumberID string
	RecipientWAID string
}

func handle(ctx context.Context, pool *pgxpool.Pool, sender *whatsapp.Sender, msg jetstream.Msg) {
	var j job
	if err := json.Unmarshal(msg.Data(), &j); err != nil {
		slog.ErrorContext(ctx, "wa-outbound: bad job, terminating",
			slog.String("err", err.Error()),
			slog.String("subject", msg.Subject()))
		_ = msg.Term()
		return
	}

	r, err := routeForTicket(ctx, pool, j.TenantID, j.TicketID)
	if err != nil {
		slog.WarnContext(ctx, "wa-outbound: no route, terminating",
			slog.String("ticket", j.TicketID.String()),
			slog.String("err", err.Error()))
		_, _ = pool.Exec(ctx, `
			INSERT INTO outbound_log (tenant_id, ticket_id, customer_id, channel, kind, body)
			VALUES ($1, $2, $3, 'wa', 'no_route', $4)`,
			j.TenantID, j.TicketID, j.CustomerID,
			fmt.Sprintf("no route for ticket: %s | body=%s", err.Error(), j.Body))
		_ = msg.Term()
		return
	}

	mid, sendErr := sender.SendText(ctx, r.PhoneNumberID, r.RecipientWAID, j.Body)
	if sendErr != nil {
		var we *whatsapp.Error
		if errors.As(sendErr, &we) && we.IsTransient() {
			slog.WarnContext(ctx, "wa-outbound: transient send error, will retry",
				slog.String("ticket", j.TicketID.String()),
				slog.String("err", sendErr.Error()))
			_ = msg.Nak()
			return
		}
		slog.ErrorContext(ctx, "wa-outbound: permanent send error, terminating",
			slog.String("ticket", j.TicketID.String()),
			slog.String("err", sendErr.Error()))
		_, _ = pool.Exec(ctx, `
			INSERT INTO outbound_log (tenant_id, ticket_id, customer_id, channel, kind, body)
			VALUES ($1, $2, $3, 'wa', 'failed_wa', $4)`,
			j.TenantID, j.TicketID, j.CustomerID, sendErr.Error())
		_ = msg.Term()
		return
	}

	if _, err := pool.Exec(ctx, `
		INSERT INTO outbound_log (tenant_id, ticket_id, customer_id, channel, kind, body)
		VALUES ($1, $2, $3, 'wa', 'sent_wa', $4)`,
		j.TenantID, j.TicketID, j.CustomerID,
		fmt.Sprintf("wamid=%s phone=%s to=%s body=%s", mid, r.PhoneNumberID, r.RecipientWAID, j.Body)); err != nil {
		slog.WarnContext(ctx, "wa-outbound: send ok but log insert failed",
			slog.String("ticket", j.TicketID.String()),
			slog.String("err", err.Error()))
	}

	slog.InfoContext(ctx, "wa-outbound: shipped to Meta",
		slog.String("ticket", j.TicketID.String()),
		slog.String("phone_number_id", r.PhoneNumberID),
		slog.String("waid", r.RecipientWAID),
		slog.String("wamid", mid))
	_ = msg.Ack()
}

func routeForTicket(ctx context.Context, pool *pgxpool.Pool, tenantID, ticketID uuid.UUID) (route, error) {
	var r route
	err := pool.QueryRow(ctx, `
		SELECT c.channel_thread_id, w.phone_number_id
		FROM tickets t
		JOIN conversations    c ON c.id = t.conversation_id
		JOIN wa_phone_numbers w ON w.tenant_id = t.tenant_id
		WHERE t.id = $1 AND t.tenant_id = $2 AND c.channel = 'wa'
		ORDER BY w.created_at ASC
		LIMIT 1`,
		ticketID, tenantID).Scan(&r.RecipientWAID, &r.PhoneNumberID)
	if err != nil {
		return route{}, err
	}
	if r.PhoneNumberID == "" || r.RecipientWAID == "" {
		return route{}, errors.New("missing phone_number_id or recipient")
	}
	return r, nil
}
