package email

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/mail"
	"strings"
	"time"

	"github.com/nats-io/nats.go/jetstream"
)

// WebhookHandler accepts inbound email POSTs and publishes one
// IngressEvent per (deduped) message onto ingress.email.message.
//
// Two body shapes are supported:
//
//   1. Mailgun-style multipart form (the default for "Store and Notify"
//      routes). The form carries timestamp/token/signature fields and a
//      handful of MIME headers; we verify signature = HMAC-SHA256(
//      timestamp+token, mailbox.WebhookSigningKey).
//
//   2. Generic JSON: a minimal envelope a custom integration can post,
//      authenticated by the mailbox's signing key in the X-CC-Signature
//      header (HMAC-SHA256 hex of the raw body). The JSON shape is
//      documented in docs/demo/postman/collection.json under
//      "Email > Inbound webhook (JSON)".
//
// Mailbox selection happens by parsing the "recipient" (Mailgun) or
// "to" (JSON) field and looking it up in email_mailboxes. The matched
// mailbox supplies the signing key; if no mailbox is registered for
// the address, the request is 404'd before any work happens.
type WebhookHandler struct {
	Store *MailboxStore
	JS    jetstream.JetStream
}

// ServeHTTP routes by Content-Type. Mailgun uses
// multipart/form-data; the JSON path is the explicit fallback for
// hand-rolled callers.
func (h *WebhookHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	// 1 MiB cap covers a reasonable email; anything bigger is rejected
	// to keep a hostile provider from streaming forever.
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)

	ct := r.Header.Get("Content-Type")
	switch {
	case strings.HasPrefix(ct, "multipart/form-data"):
		h.handleMailgun(w, r)
	case strings.HasPrefix(ct, "application/json"):
		h.handleJSON(w, r)
	default:
		http.Error(w, "unsupported content-type", http.StatusUnsupportedMediaType)
	}
}

// handleMailgun parses + verifies the Mailgun "Store and Notify"
// payload. Signature scheme is documented in
// https://documentation.mailgun.com/en/latest/user_manual.html#securing-webhooks
//
// Reject when:
//   * signature missing/empty
//   * HMAC mismatch
//   * timestamp older than 15 minutes (replay window)
//   * recipient doesn't match a known mailbox
func (h *WebhookHandler) handleMailgun(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseMultipartForm(1 << 20); err != nil {
		http.Error(w, "bad multipart", http.StatusBadRequest)
		return
	}
	form := r.MultipartForm.Value
	first := func(k string) string {
		v := form[k]
		if len(v) == 0 {
			return ""
		}
		return v[0]
	}

	recipient := strings.ToLower(strings.TrimSpace(first("recipient")))
	if recipient == "" {
		http.Error(w, "recipient missing", http.StatusBadRequest)
		return
	}
	mb, err := h.Store.GetByAddress(r.Context(), recipient)
	if errors.Is(err, ErrMailboxNotFound) {
		http.Error(w, "no mailbox for "+recipient, http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, "lookup", http.StatusInternalServerError)
		return
	}
	if mb.WebhookSigningKey == "" {
		http.Error(w, "webhook disabled for this mailbox", http.StatusForbidden)
		return
	}

	ts := first("timestamp")
	tok := first("token")
	sig := first("signature")
	if ts == "" || tok == "" || sig == "" {
		http.Error(w, "timestamp/token/signature missing", http.StatusBadRequest)
		return
	}
	if !verifyMailgunSig(mb.WebhookSigningKey, ts, tok, sig) {
		slog.WarnContext(r.Context(), "email: webhook hmac mismatch",
			slog.String("address", recipient))
		http.Error(w, "bad signature", http.StatusUnauthorized)
		return
	}
	if !withinWindow(ts, 15*time.Minute) {
		http.Error(w, "stale timestamp", http.StatusUnauthorized)
		return
	}

	in := InboundEmail{
		MailboxID:   mb.ID,
		TenantID:    mb.TenantID,
		FromAddress: pickFromMailgun(first("from"), first("sender")),
		FromName:    extractName(first("from")),
		Subject:     first("subject"),
		BodyText:    first("body-plain"),
		MessageID:   first("Message-Id"),
		InReplyTo:   first("In-Reply-To"),
		References:  SplitReferences(first("References")),
		OccurredAt:  parseTS(ts),
	}
	if in.MessageID == "" {
		// Mailgun routes sometimes drop the angle brackets in the
		// header copy; synthesise one keyed on token so dedupe works.
		in.MessageID = "<mailgun-" + tok + "@unknown>"
	}
	h.publish(w, r.Context(), in)
}

// handleJSON consumes the documented hand-rolled shape. Body is
// signed with HMAC-SHA256 (hex) in X-CC-Signature; the body itself is
// the signed bytes (no canonicalisation, just the raw request body).
func (h *WebhookHandler) handleJSON(w http.ResponseWriter, r *http.Request) {
	var body struct {
		To         string   `json:"to"`
		From       string   `json:"from"`
		FromName   string   `json:"from_name,omitempty"`
		Subject    string   `json:"subject"`
		Text       string   `json:"text"`
		MessageID  string   `json:"message_id"`
		InReplyTo  string   `json:"in_reply_to,omitempty"`
		References []string `json:"references,omitempty"`
		Timestamp  int64    `json:"timestamp"` // unix seconds
	}
	// Read once so we can both decode and verify.
	const maxBody = 1 << 20
	buf := make([]byte, 0, 4096)
	for {
		chunk := make([]byte, 32<<10)
		n, err := r.Body.Read(chunk)
		if n > 0 {
			buf = append(buf, chunk[:n]...)
			if len(buf) > maxBody {
				http.Error(w, "body too large", http.StatusRequestEntityTooLarge)
				return
			}
		}
		if err != nil {
			break
		}
	}
	if err := json.Unmarshal(buf, &body); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}

	to := strings.ToLower(strings.TrimSpace(body.To))
	mb, err := h.Store.GetByAddress(r.Context(), to)
	if errors.Is(err, ErrMailboxNotFound) {
		http.Error(w, "no mailbox for "+to, http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, "lookup", http.StatusInternalServerError)
		return
	}
	if mb.WebhookSigningKey == "" {
		http.Error(w, "webhook disabled for this mailbox", http.StatusForbidden)
		return
	}

	got := r.Header.Get("X-CC-Signature")
	if got == "" {
		http.Error(w, "X-CC-Signature missing", http.StatusUnauthorized)
		return
	}
	mac := hmac.New(sha256.New, []byte(mb.WebhookSigningKey))
	mac.Write(buf)
	want := hex.EncodeToString(mac.Sum(nil))
	if !hmac.Equal([]byte(got), []byte(want)) {
		slog.WarnContext(r.Context(), "email: json webhook hmac mismatch",
			slog.String("address", to))
		http.Error(w, "bad signature", http.StatusUnauthorized)
		return
	}
	if body.Timestamp != 0 && !withinWindow(fmt.Sprint(body.Timestamp), 15*time.Minute) {
		http.Error(w, "stale timestamp", http.StatusUnauthorized)
		return
	}

	occ := time.Now().UTC()
	if body.Timestamp != 0 {
		occ = time.Unix(body.Timestamp, 0).UTC()
	}
	if body.MessageID == "" {
		http.Error(w, "message_id required", http.StatusBadRequest)
		return
	}
	in := InboundEmail{
		MailboxID:   mb.ID,
		TenantID:    mb.TenantID,
		FromAddress: strings.ToLower(strings.TrimSpace(body.From)),
		FromName:    body.FromName,
		Subject:     body.Subject,
		BodyText:    body.Text,
		MessageID:   body.MessageID,
		InReplyTo:   body.InReplyTo,
		References:  body.References,
		OccurredAt:  occ,
	}
	h.publish(w, r.Context(), in)
}

func (h *WebhookHandler) publish(w http.ResponseWriter, ctx context.Context, in InboundEmail) {
	fresh, err := h.Store.RecordWebhookSeen(ctx, in.MailboxID, in.MessageID)
	if err != nil {
		http.Error(w, "dedupe", http.StatusInternalServerError)
		return
	}
	if !fresh {
		// Provider retry; we've already ingested. 200 + no-op so the
		// provider stops retrying.
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"deduped":true}`))
		return
	}

	payload, err := json.Marshal(Normalize(in))
	if err != nil {
		http.Error(w, "marshal", http.StatusInternalServerError)
		return
	}
	if _, err := h.JS.Publish(ctx, "ingress.email.message", payload,
		jetstream.WithMsgID(in.MessageID)); err != nil {
		slog.ErrorContext(ctx, "email: ingress publish",
			slog.String("err", err.Error()))
		http.Error(w, "publish", http.StatusBadGateway)
		return
	}
	w.WriteHeader(http.StatusAccepted)
	_, _ = w.Write([]byte(`{"queued":true}`))
}

// verifyMailgunSig recomputes HMAC-SHA256(timestamp+token, key)
// against the hex-encoded signature the provider posted.
func verifyMailgunSig(key, ts, token, sig string) bool {
	mac := hmac.New(sha256.New, []byte(key))
	mac.Write([]byte(ts + token))
	want := hex.EncodeToString(mac.Sum(nil))
	return hmac.Equal([]byte(sig), []byte(want))
}

// withinWindow returns true when ts (unix seconds, as string) is
// within tolerance of now. Catches replays.
func withinWindow(ts string, tolerance time.Duration) bool {
	var sec int64
	if _, err := fmt.Sscanf(ts, "%d", &sec); err != nil {
		return false
	}
	t := time.Unix(sec, 0)
	delta := time.Since(t)
	if delta < 0 {
		delta = -delta
	}
	return delta <= tolerance
}

func parseTS(ts string) time.Time {
	var sec int64
	if _, err := fmt.Sscanf(ts, "%d", &sec); err != nil {
		return time.Now().UTC()
	}
	return time.Unix(sec, 0).UTC()
}

// pickFromMailgun prefers the "from" header (which carries the
// display name) and falls back to "sender" (envelope From). We
// return the bare address; FromName is parsed separately.
func pickFromMailgun(from, sender string) string {
	src := from
	if src == "" {
		src = sender
	}
	if a, err := parseAddr(src); err == nil {
		return strings.ToLower(a.Address)
	}
	return strings.ToLower(strings.TrimSpace(src))
}

func extractName(rawFrom string) string {
	if a, err := parseAddr(rawFrom); err == nil {
		return strings.TrimSpace(a.Name)
	}
	return ""
}

// parseAddr is a tiny wrapper to keep the imports tidy.
func parseAddr(s string) (*addr, error) {
	a, err := mail.ParseAddress(s)
	if err != nil {
		return nil, err
	}
	return &addr{Address: a.Address, Name: a.Name}, nil
}

type addr struct{ Address, Name string }
