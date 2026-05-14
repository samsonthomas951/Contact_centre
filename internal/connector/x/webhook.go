package x

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

// SubjectPrefix is the NATS subject root for normalised X events.
// Full subject: `ingress.x.<kind>` where kind is mention/reply/quote/
// dm/favorite/follow/unfollow.
const SubjectPrefix = "ingress.x"

// WebhookHandler serves the Account Activity API webhook. Mount it
// outside the auth-protected /v1 subtree on the gateway — X delivers
// without a bearer.
type WebhookHandler struct {
	ConsumerSecret string
	Resolver       TenantResolver
	JS             jetstream.JetStream
}

// ServeHTTP routes:
//
//   GET  -> CRC (Challenge Response Check). Echo HMAC-SHA256(crc_token,
//           consumer_secret) under "response_token" or X unregisters us.
//   POST -> event delivery. Verify x-twitter-webhooks-signature against
//           the raw body, then normalize + publish to JetStream.
func (h *WebhookHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		h.handleCRC(w, r)
	case http.MethodPost:
		h.handleEvent(w, r)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (h *WebhookHandler) handleCRC(w http.ResponseWriter, r *http.Request) {
	tok := r.URL.Query().Get("crc_token")
	if tok == "" {
		http.Error(w, "missing crc_token", http.StatusBadRequest)
		return
	}
	resp := map[string]string{"response_token": CRCResponseToken(tok, h.ConsumerSecret)}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

func (h *WebhookHandler) handleEvent(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
	if err != nil {
		http.Error(w, "body too large", http.StatusRequestEntityTooLarge)
		return
	}
	if !VerifyWebhookSignature(body, r.Header.Get("x-twitter-webhooks-signature"), h.ConsumerSecret) {
		// 401 — never 5xx (X retries 5xx).
		http.Error(w, "invalid signature", http.StatusUnauthorized)
		return
	}

	var wh AAAWebhook
	if err := json.Unmarshal(body, &wh); err != nil {
		slog.WarnContext(r.Context(), "x: signed but unparseable",
			slog.String("err", err.Error()))
		w.WriteHeader(http.StatusOK)
		return
	}

	for _, e := range Normalize(wh, h.Resolver.ResolveTenant) {
		if err := publish(r.Context(), h.JS, e); err != nil {
			slog.ErrorContext(r.Context(), "x: publish failed",
				slog.String("err", err.Error()),
				slog.String("account_id", e.AccountID),
				slog.String("kind", e.Kind))
		}
	}
	w.WriteHeader(http.StatusOK)
}

// publish wraps a JetStream PublishMsg with a Nats-Msg-Id derived from
// the platform message id so the dedupe window catches X's known
// duplicate-event behaviour (per §17 — when both User A and User B are
// subscribed and A mentions B, the webhook fires twice).
func publish(ctx context.Context, js jetstream.JetStream, e InboundMessage) error {
	body, err := e.MarshalNATS()
	if err != nil {
		return err
	}
	if js == nil {
		return errors.New("x: jetstream not configured")
	}
	subject := fmt.Sprintf("%s.%s", SubjectPrefix, e.Kind)
	hdr := nats.Header{}
	if e.PlatformMsgID != "" {
		// `for_user_id` is included so the same event delivered to two
		// subscribers produces two distinct dedupe keys (each tenant
		// must see its own copy).
		hdr.Set(jetstream.MsgIDHeader, fmt.Sprintf("x:%s:%s", e.AccountID, e.PlatformMsgID))
	}
	_, err = js.PublishMsg(ctx, &nats.Msg{Subject: subject, Header: hdr, Data: body})
	return err
}
