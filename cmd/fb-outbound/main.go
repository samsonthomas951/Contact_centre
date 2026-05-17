// Command fb-outbound is the production-shaped Facebook Messenger
// outbound worker. It subscribes to outbound.fb.text on JetStream,
// loads the tenant's connected Page + the customer PSID for the ticket,
// and ships the reply via facebook.Sender.SendText (which POSTs to
// graph.facebook.com).
//
// Coexists with the outbound-stub binary: each is a separate durable
// consumer on OUTBOUND, so both receive every fb job -- the stub keeps
// writing to outbound_log (proof the round-trip works), this worker
// actually calls Meta.
//
// On success: inserts an outbound_log row with kind=sent_fb and the
// platform message id Meta echoed back.
//
// Retry policy:
//
//	transient (5xx, code 4/17/32/613) -> Nak; JetStream redelivers
//	permanent (token expired, invalid recipient, etc.) -> Term + log
//	JSON-bad payload                                   -> Term + log
//
// Env:
//
//	DATABASE_URL          required
//	NATS_URL              required
//	FB_DEMO_KEY           required for the demo Crypter (production
//	                      would wire a Vault transit Crypter instead)
//	FB_GRAPH_HOST         optional, default https://graph.facebook.com
//	FB_GRAPH_VERSION      optional, default v22.0
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
	"github.com/samsonthomas951/contact-centre/internal/pkg/config"
	"github.com/samsonthomas951/contact-centre/internal/pkg/logging"
	"github.com/samsonthomas951/contact-centre/internal/pkg/natsx"
	"github.com/samsonthomas951/contact-centre/internal/pkg/postgres"
)

var version = "dev"

// job mirrors the envelope OnOutbound publishes (cmd/gateway/main.go).
type job struct {
	TenantID   uuid.UUID `json:"tenant_id"`
	TicketID   uuid.UUID `json:"ticket_id"`
	CustomerID uuid.UUID `json:"customer_id"`
	Channel    string    `json:"channel"`
	Body       string    `json:"body"`
	Kind       string    `json:"kind"`
}

type fbCfg struct {
	DemoKey      string `env:"FB_DEMO_KEY"      default:"demo-only-do-not-use-in-prod"`
	GraphHost    string `env:"FB_GRAPH_HOST"    default:"https://graph.facebook.com"`
	GraphVersion string `env:"FB_GRAPH_VERSION" default:"v22.0"`
}

func main() {
	if err := run(); err != nil {
		slog.Error("fb-outbound: fatal", slog.String("err", err.Error()))
		os.Exit(1)
	}
}

func run() error {
	logging.Init(logging.Options{Service: "fb-outbound", Version: version, Level: slog.LevelInfo})

	var (
		dbCfg   postgres.Config
		natsCfg natsx.Config
		fbc     fbCfg
	)
	for _, dst := range []any{&dbCfg, &natsCfg, &fbc} {
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

	crypter, err := facebook.NewDemoCrypter(fbc.DemoKey)
	if err != nil {
		return fmt.Errorf("fb-outbound: crypter: %w", err)
	}
	store := facebook.NewPGPageStore(pool, crypter)
	sender := facebook.NewSender(store, nil)
	sender.GraphHost = fbc.GraphHost
	sender.GraphVersion = fbc.GraphVersion

	cons, err := js.CreateOrUpdateConsumer(ctx, "OUTBOUND", jetstream.ConsumerConfig{
		Durable:       "fb-outbound",
		FilterSubject: "outbound.fb.text",
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

	// iter.Next() doesn't honour ctx; spawn a watcher.
	go func() { <-ctx.Done(); iter.Stop() }()

	slog.InfoContext(ctx, "fb-outbound: ready, subscribed to outbound.fb.text")

	for {
		msg, err := iter.Next()
		if errors.Is(err, jetstream.ErrMsgIteratorClosed) {
			return nil
		}
		if err != nil {
			slog.WarnContext(ctx, "fb-outbound: iter next",
				slog.String("err", err.Error()))
			continue
		}
		handle(ctx, pool, sender, msg)
	}
}

// route holds the per-ticket fields the worker needs from PG before
// it can call Meta. Loaded by routeForTicket.
type route struct {
	PageID         string
	RecipientPSID  string
}

func handle(ctx context.Context, pool *pgxpool.Pool, sender *facebook.Sender, msg jetstream.Msg) {
	var j job
	if err := json.Unmarshal(msg.Data(), &j); err != nil {
		slog.ErrorContext(ctx, "fb-outbound: bad job, terminating",
			slog.String("err", err.Error()),
			slog.String("subject", msg.Subject()))
		_ = msg.Term()
		return
	}

	r, err := routeForTicket(ctx, pool, j.TenantID, j.TicketID)
	if err != nil {
		// No route means either no fb_pages connected for this tenant
		// (operator hasn't run the OAuth yet) or the conversation isn't
		// actually fb. Either way the job is permanently un-shippable;
		// record it so the agent UI can surface "this reply needs a
		// connected Page" instead of silently swallowing.
		slog.WarnContext(ctx, "fb-outbound: no route, terminating",
			slog.String("ticket", j.TicketID.String()),
			slog.String("err", err.Error()))
		_, _ = pool.Exec(ctx, `
			INSERT INTO outbound_log (tenant_id, ticket_id, customer_id, channel, kind, body)
			VALUES ($1, $2, $3, 'fb', 'no_route', $4)`,
			j.TenantID, j.TicketID, j.CustomerID,
			fmt.Sprintf("no route for ticket: %s | body=%s", err.Error(), j.Body))
		_ = msg.Term()
		return
	}

	mid, sendErr := sender.SendText(ctx, r.PageID, r.RecipientPSID, j.Body)
	if sendErr != nil {
		var fe *facebook.Error
		if errors.As(sendErr, &fe) && fe.IsTransient() {
			slog.WarnContext(ctx, "fb-outbound: transient send error, will retry",
				slog.String("ticket", j.TicketID.String()),
				slog.String("err", sendErr.Error()))
			_ = msg.Nak()
			return
		}
		slog.ErrorContext(ctx, "fb-outbound: permanent send error, terminating",
			slog.String("ticket", j.TicketID.String()),
			slog.String("err", sendErr.Error()))
		// Persist the failure so the agent UI can surface "this reply
		// didn't go out" in a follow-up; the outbound_log table accepts
		// kind=failed_fb so the dashboard can filter.
		_, _ = pool.Exec(ctx, `
			INSERT INTO outbound_log (tenant_id, ticket_id, customer_id, channel, kind, body)
			VALUES ($1, $2, $3, 'fb', 'failed_fb', $4)`,
			j.TenantID, j.TicketID, j.CustomerID, sendErr.Error())
		_ = msg.Term()
		return
	}

	if _, err := pool.Exec(ctx, `
		INSERT INTO outbound_log
		  (tenant_id, ticket_id, customer_id, channel, kind, body)
		VALUES ($1, $2, $3, 'fb', 'sent_fb', $4)`,
		j.TenantID, j.TicketID, j.CustomerID,
		fmt.Sprintf("mid=%s page=%s psid=%s body=%s", mid, r.PageID, r.RecipientPSID, j.Body)); err != nil {
		// We've already sent to Meta -- log but still Ack, else
		// JetStream redelivers and the customer gets the message twice.
		slog.WarnContext(ctx, "fb-outbound: send ok but log insert failed",
			slog.String("ticket", j.TicketID.String()),
			slog.String("err", err.Error()))
	}

	slog.InfoContext(ctx, "fb-outbound: shipped to Meta",
		slog.String("ticket", j.TicketID.String()),
		slog.String("page_id", r.PageID),
		slog.String("psid", r.RecipientPSID),
		slog.String("message_id", mid))
	_ = msg.Ack()
}

// routeForTicket loads the customer PSID + the tenant's connected Page.
//
// The PSID is read off conversations.channel_thread_id -- that's how
// every fb message ingress writes it (see internal/ticket/ingress.go).
//
// The page_id is read off fb_pages WHERE tenant_id = ...; for the demo
// path we pick the first connected Page when there are multiple. A
// future enhancement stores page_id on the conversation row directly
// so multi-Page tenants don't drift.
func routeForTicket(ctx context.Context, pool *pgxpool.Pool, tenantID, ticketID uuid.UUID) (route, error) {
	var r route
	err := pool.QueryRow(ctx, `
		SELECT c.channel_thread_id, p.page_id
		FROM tickets t
		JOIN conversations c ON c.id = t.conversation_id
		JOIN fb_pages      p ON p.tenant_id = t.tenant_id
		WHERE t.id = $1 AND t.tenant_id = $2 AND c.channel = 'fb'
		ORDER BY p.created_at ASC
		LIMIT 1`,
		ticketID, tenantID).Scan(&r.RecipientPSID, &r.PageID)
	if err != nil {
		return route{}, err
	}
	if r.PageID == "" || r.RecipientPSID == "" {
		return route{}, errors.New("missing page_id or recipient")
	}
	return r, nil
}
