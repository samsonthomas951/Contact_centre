package facebook

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

// SubjectPrefix is the NATS subject root for normalised inbound events.
// The full subject is `ingress.fb.<kind>` (message, postback, read,
// delivery).
const SubjectPrefix = "ingress.fb"

// IngressStream is the JetStream stream the connector publishes onto.
// The Ticket Service consumes it.
const IngressStream = "INGRESS"

// EnsureStream creates the INGRESS stream if it does not exist. The
// stream covers all channels (`ingress.>`) so adding the X / WhatsApp
// connectors later doesn't need a separate stream.
func EnsureStream(ctx context.Context, js jetstream.JetStream) error {
	_, err := js.CreateOrUpdateStream(ctx, jetstream.StreamConfig{
		Name:        IngressStream,
		Subjects:    []string{"ingress.>"},
		Retention:   jetstream.InterestPolicy,
		Storage:     jetstream.FileStorage,
		Description: "Normalised inbound events from every channel connector.",
	})
	return err
}

// TenantResolver maps a platform Page ID to the internal tenant ID. The
// connector queries fb_pages on cold start and caches in process.
type TenantResolver interface {
	ResolveTenant(pageID string) (tenantID string, ok bool)
}

// Webhook is the chi-compatible HTTP handler for the Meta callback.
// Mount it BEFORE the auth middleware on the gateway: Meta does not
// send a bearer.
type WebhookHandler struct {
	AppSecret string
	VerifyTok string // GET-time verify token Meta sends on subscription
	Resolver  TenantResolver
	JS        jetstream.JetStream
}

// ServeHTTP routes GET (subscription verification) and POST (event
// delivery) on the same path, matching Meta's spec.
func (h *WebhookHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		h.handleVerify(w, r)
	case http.MethodPost:
		h.handleEvent(w, r)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// handleVerify implements the one-time GET handshake Meta uses when an
// admin subscribes a Page. We echo `hub.challenge` only when the token
// matches, otherwise 403.
func (h *WebhookHandler) handleVerify(w http.ResponseWriter, r *http.Request) {
	if r.URL.Query().Get("hub.mode") != "subscribe" {
		http.Error(w, "bad mode", http.StatusBadRequest)
		return
	}
	if r.URL.Query().Get("hub.verify_token") != h.VerifyTok {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	_, _ = io.WriteString(w, r.URL.Query().Get("hub.challenge"))
}

// handleEvent verifies the signature against the *raw* body, then
// normalises and publishes each event onto JetStream. Always returns
// 200 once the signature is good — Meta retries 5xx, and we don't want
// poison events to throttle Page deliveries.
func (h *WebhookHandler) handleEvent(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
	if err != nil {
		http.Error(w, "body too large", http.StatusRequestEntityTooLarge)
		return
	}

	if !VerifySignature(body, r.Header.Get(SignatureHeader), h.AppSecret) {
		// 401 (not 5xx) so Meta does not retry an attacker's payload.
		http.Error(w, "invalid signature", http.StatusUnauthorized)
		return
	}

	var wh Webhook
	if err := json.Unmarshal(body, &wh); err != nil {
		// Bad JSON from a signed sender is suspicious; log and 200 so
		// Meta doesn't retry, but never publish.
		slog.WarnContext(r.Context(), "fb: signed but unparseable",
			slog.String("err", err.Error()))
		w.WriteHeader(http.StatusOK)
		return
	}

	events := Normalize(wh, h.Resolver.ResolveTenant)
	for _, e := range events {
		if err := publish(r.Context(), h.JS, e); err != nil {
			// Log + 200. The next inbound event from this user will
			// re-establish the conversation; better than a Meta retry
			// storm during a JetStream blip.
			slog.ErrorContext(r.Context(), "fb: publish failed",
				slog.String("err", err.Error()),
				slog.String("page_id", e.PageID))
		}
	}
	w.WriteHeader(http.StatusOK)
}

// publish wraps a JetStream PublishMsg with a Nats-Msg-Id derived from
// the platform message id so the 5-minute dedupe window catches Meta's
// at-least-once retries.
func publish(ctx context.Context, js jetstream.JetStream, e InboundMessage) error {
	body, err := e.MarshalNATS()
	if err != nil {
		return err
	}
	subject := fmt.Sprintf("%s.%s", SubjectPrefix, e.Kind)

	hdr := nats.Header{}
	if e.PlatformMsgID != "" {
		hdr.Set(jetstream.MsgIDHeader, fmt.Sprintf("fb:%s", e.PlatformMsgID))
	}

	if js == nil {
		// Allow tests / dev to short-circuit when no NATS is wired.
		return errors.New("fb: jetstream not configured")
	}
	_, err = js.PublishMsg(ctx, &nats.Msg{
		Subject: subject, Header: hdr, Data: body,
	})
	return err
}
