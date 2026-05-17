// Command email-outbound subscribes to outbound.email.text on
// JetStream, loads the tenant's mailbox + the customer's email
// address for the ticket, builds a threaded RFC 5322 message, and
// ships it via SMTP using email.Sender. The Message-ID we mint is
// persisted to email_outbound_log so a bounce notification posted
// hours later can be matched back.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/samsonthomas951/contact-centre/internal/connector/email"
	"github.com/samsonthomas951/contact-centre/internal/connector/facebook"
	"github.com/samsonthomas951/contact-centre/internal/pkg/config"
	"github.com/samsonthomas951/contact-centre/internal/pkg/logging"
	"github.com/samsonthomas951/contact-centre/internal/pkg/natsx"
	"github.com/samsonthomas951/contact-centre/internal/pkg/postgres"
)

var version = "dev"

// job mirrors the OnOutbound envelope shape published by the gateway.
type job struct {
	TenantID   uuid.UUID `json:"tenant_id"`
	TicketID   uuid.UUID `json:"ticket_id"`
	CustomerID uuid.UUID `json:"customer_id"`
	Channel    string    `json:"channel"`
	Body       string    `json:"body"`
	Kind       string    `json:"kind"`
}

type cfg struct {
	DemoKey        string `env:"FB_DEMO_KEY"            default:"demo-only-do-not-use-in-prod"`
	AllowPlaintext bool   `env:"EMAIL_ALLOW_PLAINTEXT"  default:"false"`
}

func main() {
	if err := run(); err != nil {
		slog.Error("email-outbound: fatal", slog.String("err", err.Error()))
		os.Exit(1)
	}
}

func run() error {
	logging.Init(logging.Options{Service: "email-outbound", Version: version, Level: slog.LevelInfo})

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
	sender := email.NewSender()
	sender.AllowPlaintext = myCfg.AllowPlaintext

	cons, err := js.CreateOrUpdateConsumer(ctx, "OUTBOUND", jetstream.ConsumerConfig{
		Durable:       "email-outbound",
		FilterSubject: "outbound.email.text",
		AckPolicy:     jetstream.AckExplicitPolicy,
		MaxAckPending: 4,
		AckWait:       60 * time.Second, // SMTP can be slow
		MaxDeliver:    5,
		DeliverPolicy: jetstream.DeliverAllPolicy,
	})
	if err != nil {
		return err
	}
	iter, err := cons.Messages(jetstream.PullMaxMessages(2))
	if err != nil {
		return err
	}
	defer iter.Stop()
	go func() { <-ctx.Done(); iter.Stop() }()

	slog.InfoContext(ctx, "email-outbound: ready, subscribed to outbound.email.text")

	for {
		msg, err := iter.Next()
		if errors.Is(err, jetstream.ErrMsgIteratorClosed) {
			return nil
		}
		if err != nil {
			slog.WarnContext(ctx, "iter next", slog.String("err", err.Error()))
			continue
		}
		handle(ctx, pool, store, sender, msg)
	}
}

// route packages the per-ticket info the worker needs from PG before
// it can call SMTP.
type route struct {
	Mailbox         email.Mailbox
	CustomerAddress string
	CustomerName    string
	LastInboundMID  string   // for In-Reply-To
	ReferencesChain []string // for References
	ConversationKey string
}

func handle(ctx context.Context, pool *pgxpool.Pool, store *email.MailboxStore,
	sender *email.Sender, msg jetstream.Msg,
) {
	var j job
	if err := json.Unmarshal(msg.Data(), &j); err != nil {
		slog.ErrorContext(ctx, "bad job, terminating",
			slog.String("err", err.Error()))
		_ = msg.Term()
		return
	}

	r, err := routeForTicket(ctx, pool, store, j.TenantID, j.TicketID)
	if err != nil {
		slog.WarnContext(ctx, "no route, terminating",
			slog.String("ticket", j.TicketID.String()),
			slog.String("err", err.Error()))
		_, _ = pool.Exec(ctx, `
			INSERT INTO outbound_log (tenant_id, ticket_id, customer_id, channel, kind, body)
			VALUES ($1, $2, $3, 'email', 'no_route', $4)`,
			j.TenantID, j.TicketID, j.CustomerID,
			fmt.Sprintf("no route: %s | body=%s", err.Error(), j.Body))
		_ = msg.Term()
		return
	}

	out := email.OutboundEmail{
		MailboxID:       r.Mailbox.ID,
		ToAddress:       r.CustomerAddress,
		ToName:          r.CustomerName,
		Subject:         "Re: your message",
		BodyText:        j.Body,
		InReplyTo:       r.LastInboundMID,
		ReferencesChain: r.ReferencesChain,
	}
	mid, sendErr := sender.SendText(ctx, r.Mailbox, out)
	if sendErr != nil {
		var se *email.SendErr
		if errors.As(sendErr, &se) && se.IsTransient() {
			slog.WarnContext(ctx, "transient send error, will retry",
				slog.String("ticket", j.TicketID.String()),
				slog.String("err", sendErr.Error()))
			_ = msg.Nak()
			return
		}
		slog.ErrorContext(ctx, "permanent send error, terminating",
			slog.String("ticket", j.TicketID.String()),
			slog.String("err", sendErr.Error()))
		_, _ = pool.Exec(ctx, `
			INSERT INTO outbound_log (tenant_id, ticket_id, customer_id, channel, kind, body)
			VALUES ($1, $2, $3, 'email', 'failed_email', $4)`,
			j.TenantID, j.TicketID, j.CustomerID, sendErr.Error())
		_ = msg.Term()
		return
	}

	tid := j.TicketID
	if err := store.LogOutbound(ctx, j.TenantID, r.Mailbox.ID, &tid, mid); err != nil {
		slog.WarnContext(ctx, "outbound log write failed (already sent)",
			slog.String("err", err.Error()))
	}
	_, _ = pool.Exec(ctx, `
		INSERT INTO outbound_log (tenant_id, ticket_id, customer_id, channel, kind, body)
		VALUES ($1, $2, $3, 'email', 'sent_email', $4)`,
		j.TenantID, j.TicketID, j.CustomerID,
		fmt.Sprintf("message_id=%s from=%s to=%s body=%s",
			mid, r.Mailbox.Address, r.CustomerAddress, j.Body))

	slog.InfoContext(ctx, "email-outbound: shipped",
		slog.String("ticket", j.TicketID.String()),
		slog.String("from", r.Mailbox.Address),
		slog.String("to", r.CustomerAddress),
		slog.String("message_id", mid))
	_ = msg.Ack()
}

// routeForTicket looks up everything the worker needs in one shot:
//
//   * the mailbox the tenant uses for outbound (pick the most-recent
//     active one — multi-mailbox routing per ticket is a follow-up);
//   * the customer's email address from the conversation thread key;
//   * the most-recent inbound message's platform_message_id (the
//     Message-ID we should reference in In-Reply-To).
func routeForTicket(ctx context.Context, pool *pgxpool.Pool, store *email.MailboxStore,
	tenantID, ticketID uuid.UUID,
) (route, error) {
	var r route

	// Customer + conversation key.
	err := pool.QueryRow(ctx, `
		SELECT c.channel_thread_id, cu.display_name
		FROM tickets t
		JOIN conversations c ON c.id = t.conversation_id
		JOIN customers     cu ON cu.id = c.customer_id
		WHERE t.id = $1 AND t.tenant_id = $2 AND c.channel = 'email'`,
		ticketID, tenantID).Scan(&r.CustomerAddress, &r.CustomerName)
	if err != nil {
		return r, fmt.Errorf("conversation: %w", err)
	}
	r.ConversationKey = r.CustomerAddress

	// Last inbound message's platform id for threading. nil-safe; if
	// the ticket has no inbound yet, we still send (just unthreaded).
	var lastMID *string
	err = pool.QueryRow(ctx, `
		SELECT platform_message_id
		FROM messages
		WHERE tenant_id = $1 AND ticket_id = $2 AND direction = 'in'
		  AND platform_message_id IS NOT NULL AND platform_message_id <> ''
		ORDER BY created_at DESC LIMIT 1`,
		tenantID, ticketID).Scan(&lastMID)
	if err == nil && lastMID != nil && *lastMID != "" {
		r.LastInboundMID = *lastMID
		r.ReferencesChain = []string{*lastMID}
	}

	// Pick the tenant's mailbox: most recently active. Multi-mailbox
	// routing (e.g. "the To: address" being the tie-breaker) is a
	// follow-up; for now we ship from the only active mailbox.
	var mbAddr string
	err = pool.QueryRow(ctx, `
		SELECT address FROM email_mailboxes
		WHERE tenant_id = $1 AND active
		ORDER BY created_at DESC LIMIT 1`,
		tenantID).Scan(&mbAddr)
	if err != nil {
		return r, fmt.Errorf("mailbox: %w", err)
	}
	mb, err := store.GetByAddress(ctx, strings.ToLower(mbAddr))
	if err != nil {
		return r, fmt.Errorf("mailbox load: %w", err)
	}
	r.Mailbox = *mb
	return r, nil
}
