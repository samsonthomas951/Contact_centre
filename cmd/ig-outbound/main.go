// Command ig-outbound is the production-shaped Instagram DM outbound
// worker. Mirrors cmd/fb-outbound: subscribes to outbound.ig.text on
// JetStream, looks up the IG Business account + recipient IGSID for
// the ticket, and ships via instagram.Sender.SendText (POST to
// graph.facebook.com/<ig_user_id>/messages).
//
// Path 1 only -- the IG account rides its linked FB Page's access
// token; that's what facebook.OAuthHandler.discoverIGForPage writes
// into ig_accounts during Connect-with-Facebook. Path 2 (own IG token)
// is a follow-up commit.
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
	"github.com/samsonthomas951/contact-centre/internal/connector/instagram"
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

type igCfg struct {
	DemoKey      string `env:"FB_DEMO_KEY"      default:"demo-only-do-not-use-in-prod"`
	GraphHost    string `env:"FB_GRAPH_HOST"    default:"https://graph.facebook.com"`
	GraphVersion string `env:"FB_GRAPH_VERSION" default:"v22.0"`
}

func main() {
	if err := run(); err != nil {
		slog.Error("ig-outbound: fatal", slog.String("err", err.Error()))
		os.Exit(1)
	}
}

func run() error {
	logging.Init(logging.Options{Service: "ig-outbound", Version: version, Level: slog.LevelInfo})

	var (
		dbCfg   postgres.Config
		natsCfg natsx.Config
		cfg     igCfg
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
		return fmt.Errorf("ig-outbound: crypter: %w", err)
	}
	store := instagram.NewPGTokenStore(pool, crypter)
	sender := instagram.NewSender(store)
	sender.GraphHost = cfg.GraphHost
	sender.GraphVersion = cfg.GraphVersion

	cons, err := js.CreateOrUpdateConsumer(ctx, "OUTBOUND", jetstream.ConsumerConfig{
		Durable:       "ig-outbound",
		FilterSubject: "outbound.ig.text",
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

	slog.InfoContext(ctx, "ig-outbound: ready, subscribed to outbound.ig.text")

	for {
		msg, err := iter.Next()
		if errors.Is(err, jetstream.ErrMsgIteratorClosed) {
			return nil
		}
		if err != nil {
			slog.WarnContext(ctx, "ig-outbound: iter next",
				slog.String("err", err.Error()))
			continue
		}
		handle(ctx, pool, sender, msg)
	}
}

type route struct {
	IGUserID       string
	RecipientIGSID string
}

func handle(ctx context.Context, pool *pgxpool.Pool, sender *instagram.Sender, msg jetstream.Msg) {
	var j job
	if err := json.Unmarshal(msg.Data(), &j); err != nil {
		slog.ErrorContext(ctx, "ig-outbound: bad job, terminating",
			slog.String("err", err.Error()),
			slog.String("subject", msg.Subject()))
		_ = msg.Term()
		return
	}

	r, err := routeForTicket(ctx, pool, j.TenantID, j.TicketID)
	if err != nil {
		slog.WarnContext(ctx, "ig-outbound: no route, terminating",
			slog.String("ticket", j.TicketID.String()),
			slog.String("err", err.Error()))
		_, _ = pool.Exec(ctx, `
			INSERT INTO outbound_log (tenant_id, ticket_id, customer_id, channel, kind, body)
			VALUES ($1, $2, $3, 'ig', 'no_route', $4)`,
			j.TenantID, j.TicketID, j.CustomerID,
			fmt.Sprintf("no route for ticket: %s | body=%s", err.Error(), j.Body))
		_ = msg.Term()
		return
	}

	mid, sendErr := sender.SendText(ctx, r.IGUserID, r.RecipientIGSID, j.Body)
	if sendErr != nil {
		var ie *instagram.Error
		if errors.As(sendErr, &ie) && ie.IsTransient() {
			slog.WarnContext(ctx, "ig-outbound: transient send error, will retry",
				slog.String("ticket", j.TicketID.String()),
				slog.String("err", sendErr.Error()))
			_ = msg.Nak()
			return
		}
		slog.ErrorContext(ctx, "ig-outbound: permanent send error, terminating",
			slog.String("ticket", j.TicketID.String()),
			slog.String("err", sendErr.Error()))
		_, _ = pool.Exec(ctx, `
			INSERT INTO outbound_log (tenant_id, ticket_id, customer_id, channel, kind, body)
			VALUES ($1, $2, $3, 'ig', 'failed_ig', $4)`,
			j.TenantID, j.TicketID, j.CustomerID, sendErr.Error())
		_ = msg.Term()
		return
	}

	if _, err := pool.Exec(ctx, `
		INSERT INTO outbound_log (tenant_id, ticket_id, customer_id, channel, kind, body)
		VALUES ($1, $2, $3, 'ig', 'sent_ig', $4)`,
		j.TenantID, j.TicketID, j.CustomerID,
		fmt.Sprintf("mid=%s ig_user=%s igsid=%s body=%s", mid, r.IGUserID, r.RecipientIGSID, j.Body)); err != nil {
		slog.WarnContext(ctx, "ig-outbound: send ok but log insert failed",
			slog.String("ticket", j.TicketID.String()),
			slog.String("err", err.Error()))
	}

	slog.InfoContext(ctx, "ig-outbound: shipped to Meta",
		slog.String("ticket", j.TicketID.String()),
		slog.String("ig_user_id", r.IGUserID),
		slog.String("igsid", r.RecipientIGSID),
		slog.String("message_id", mid))
	_ = msg.Ack()
}

func routeForTicket(ctx context.Context, pool *pgxpool.Pool, tenantID, ticketID uuid.UUID) (route, error) {
	var r route
	err := pool.QueryRow(ctx, `
		SELECT c.channel_thread_id, ig.ig_user_id
		FROM tickets t
		JOIN conversations c ON c.id = t.conversation_id
		JOIN ig_accounts   ig ON ig.tenant_id = t.tenant_id
		WHERE t.id = $1 AND t.tenant_id = $2 AND c.channel = 'ig'
		ORDER BY ig.created_at ASC
		LIMIT 1`,
		ticketID, tenantID).Scan(&r.RecipientIGSID, &r.IGUserID)
	if err != nil {
		return route{}, err
	}
	if r.IGUserID == "" || r.RecipientIGSID == "" {
		return route{}, errors.New("missing ig_user_id or recipient")
	}
	return r, nil
}
