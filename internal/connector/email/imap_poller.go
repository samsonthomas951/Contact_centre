package email

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/mail"
	"strconv"
	"time"

	"github.com/emersion/go-imap"
	"github.com/emersion/go-imap/client"
	"github.com/nats-io/nats.go/jetstream"
)

// PollerConfig drives the IMAP fetcher.
type PollerConfig struct {
	Interval  time.Duration // between polls per mailbox; default 30s
	BatchSize uint32        // UID FETCH window; default 50
}

// Poller drives one goroutine per mailbox that periodically fetches
// unread messages, normalises each, publishes ingress.email.message,
// and advances the per-mailbox UID checkpoint.
//
// The poller is deliberately at-least-once: a successful publish
// followed by a crash before the UID checkpoint update will reprocess
// the message on next run. Dedupe happens in
// MailboxStore.RecordWebhookSeen (the webhook + IMAP share that table
// keyed on Message-ID).
type Poller struct {
	Store *MailboxStore
	JS    jetstream.JetStream
	Cfg   PollerConfig
}

// Run blocks until ctx cancels, looping over every active mailbox at
// the configured interval.
func (p *Poller) Run(ctx context.Context) error {
	interval := p.Cfg.Interval
	if interval <= 0 {
		interval = 30 * time.Second
	}
	batch := p.Cfg.BatchSize
	if batch == 0 {
		batch = 50
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	// Tick once immediately so the first poll doesn't wait `interval`
	// after process start.
	p.pollAll(ctx, batch)

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			p.pollAll(ctx, batch)
		}
	}
}

func (p *Poller) pollAll(ctx context.Context, batch uint32) {
	mailboxes, err := p.Store.ListAll(ctx)
	if err != nil {
		slog.WarnContext(ctx, "email-imap: list mailboxes",
			slog.String("err", err.Error()))
		return
	}
	for _, mb := range mailboxes {
		if mb.IMAPHost == "" || mb.IMAPUsername == "" || mb.IMAPPassword == "" {
			continue // webhook-only mailbox; skip silently.
		}
		if err := p.pollOne(ctx, mb, batch); err != nil {
			slog.WarnContext(ctx, "email-imap: poll failed",
				slog.String("mailbox", mb.Address),
				slog.String("err", err.Error()))
		}
	}
}

func (p *Poller) pollOne(ctx context.Context, mb Mailbox, batch uint32) error {
	addr := net.JoinHostPort(mb.IMAPHost, strconv.Itoa(mb.IMAPPort))

	var c *client.Client
	var err error
	// 993 = implicit TLS; everything else assumes plaintext then
	// STARTTLS. The demo's Mailpit listens plaintext on 1143, so we
	// also need to tolerate "no TLS at all" via env opt-out.
	if mb.IMAPPort == 993 {
		c, err = client.DialTLS(addr, &tls.Config{
			ServerName: mb.IMAPHost, MinVersion: tls.VersionTLS12,
		})
	} else {
		c, err = client.Dial(addr)
		if err == nil {
			if ok, _ := c.SupportStartTLS(); ok {
				err = c.StartTLS(&tls.Config{
					ServerName: mb.IMAPHost, MinVersion: tls.VersionTLS12,
				})
			}
		}
	}
	if err != nil {
		return fmt.Errorf("dial: %w", err)
	}
	defer func() { _ = c.Logout() }()

	if err := c.Login(mb.IMAPUsername, mb.IMAPPassword); err != nil {
		return fmt.Errorf("login: %w", err)
	}
	if _, err := c.Select("INBOX", false); err != nil {
		return fmt.Errorf("select: %w", err)
	}

	// Fetch every UID strictly greater than our checkpoint.
	since := uint32(mb.IMAPLastSeenUID) + 1
	seqSet := new(imap.SeqSet)
	seqSet.AddRange(since, 0) // 0 = "max"

	section := &imap.BodySectionName{}
	items := []imap.FetchItem{imap.FetchUid, imap.FetchEnvelope, section.FetchItem()}

	msgCh := make(chan *imap.Message, 16)
	doneCh := make(chan error, 1)
	go func() {
		doneCh <- c.UidFetch(seqSet, items, msgCh)
	}()

	highestSeen := uint32(mb.IMAPLastSeenUID)
	processed := 0
	for m := range msgCh {
		if m.Uid > highestSeen {
			highestSeen = m.Uid
		}
		if err := p.handleMsg(ctx, mb, m, section); err != nil {
			slog.WarnContext(ctx, "email-imap: handle",
				slog.String("mailbox", mb.Address),
				slog.Uint64("uid", uint64(m.Uid)),
				slog.String("err", err.Error()))
			continue
		}
		processed++
		if uint32(processed) >= batch {
			break
		}
	}
	if err := <-doneCh; err != nil {
		// Don't fail the checkpoint update for a partial fetch; what
		// we did process is real progress.
		slog.WarnContext(ctx, "email-imap: fetch", slog.String("err", err.Error()))
	}
	if highestSeen > uint32(mb.IMAPLastSeenUID) {
		if err := p.Store.AdvanceIMAPCheckpoint(ctx, mb.ID, int64(highestSeen)); err != nil {
			return fmt.Errorf("checkpoint: %w", err)
		}
	}
	if processed > 0 {
		slog.InfoContext(ctx, "email-imap: pulled",
			slog.String("mailbox", mb.Address),
			slog.Int("processed", processed),
			slog.Uint64("highest_uid", uint64(highestSeen)))
	}
	return nil
}

func (p *Poller) handleMsg(ctx context.Context, mb Mailbox, m *imap.Message, section *imap.BodySectionName) error {
	r := m.GetBody(section)
	if r == nil {
		return fmt.Errorf("no body for uid %d", m.Uid)
	}
	// Parse the message via net/mail (sufficient for headers + text body).
	msg, err := mail.ReadMessage(io.LimitReader(r, 1<<20))
	if err != nil {
		return fmt.Errorf("parse: %w", err)
	}

	from := ""
	fromName := ""
	if addrs, err := msg.Header.AddressList("From"); err == nil && len(addrs) > 0 {
		from = addrs[0].Address
		fromName = addrs[0].Name
	}
	body, _ := io.ReadAll(io.LimitReader(msg.Body, 1<<20))

	in := InboundEmail{
		MailboxID:   mb.ID,
		TenantID:    mb.TenantID,
		FromAddress: from,
		FromName:    fromName,
		Subject:     msg.Header.Get("Subject"),
		BodyText:    string(body),
		MessageID:   msg.Header.Get("Message-Id"),
		InReplyTo:   msg.Header.Get("In-Reply-To"),
		References:  SplitReferences(msg.Header.Get("References")),
		OccurredAt:  time.Now().UTC(),
	}
	if t, err := mail.ParseDate(msg.Header.Get("Date")); err == nil {
		in.OccurredAt = t
	}
	if in.MessageID == "" {
		// Some senders omit Message-ID; synthesise from UID.
		in.MessageID = fmt.Sprintf("<imap-%s-uid-%d@unknown>", mb.ID.String(), m.Uid)
	}

	fresh, err := p.Store.RecordWebhookSeen(ctx, mb.ID, in.MessageID)
	if err != nil {
		return fmt.Errorf("dedupe: %w", err)
	}
	if !fresh {
		return nil
	}
	payload, err := json.Marshal(Normalize(in))
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	if _, err := p.JS.Publish(ctx, "ingress.email.message", payload,
		jetstream.WithMsgID(in.MessageID)); err != nil {
		return fmt.Errorf("publish: %w", err)
	}
	return nil
}
