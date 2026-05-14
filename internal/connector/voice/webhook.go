package voice

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

// SubjectPrefix is the NATS subject root for voice events.
// Full subject: ingress.voice.<kind>.
const SubjectPrefix = "ingress.voice"

// NumberLookup maps an incoming destinationNumber + URL-path secret to
// a voice_numbers row (tenant + dial-out).
type NumberLookup interface {
	ResolveNumber(ctx context.Context, callerNumber, destinationNumber, secret string) (
		tenantID, voiceNumberID, agentDialNumber string, ok bool, err error)
}

// WebhookHandler routes the carrier callbacks. We mount under
//
//	POST /v1/voice/at/{secret}
//
// where {secret} is the per-tenant rotating value stored encrypted on
// voice_numbers. AT delivers application/x-www-form-urlencoded; the
// response is the IVR XML described in ivr.go.
type WebhookHandler struct {
	Numbers     NumberLookup
	JS          jetstream.JetStream
	// ConsentCallbackURL is the absolute URL AT will POST to with the
	// DTMF digit collected from the consent IVR. Defaults to the same
	// path with no path param (the handler reads the form to disambiguate).
	ConsentCallbackURL string
}

// ServeHTTP routes the carrier callback.
func (h *WebhookHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	secret := chi.URLParam(r, "secret")
	if secret == "" {
		http.Error(w, "missing secret", http.StatusBadRequest)
		return
	}
	// Cap the form so a wedged carrier can't OOM us. AT bodies are
	// well under 8 KiB.
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	ev, ok := FromATForm(r.Form)
	if !ok {
		http.Error(w, "missing sessionId", http.StatusBadRequest)
		return
	}

	tenantID, voiceNumberID, dial, ok, err := h.Numbers.ResolveNumber(
		r.Context(), ev.CallerNumber, ev.DestinationNumber, secret)
	if err != nil {
		slog.ErrorContext(r.Context(), "voice: resolve number",
			slog.String("err", err.Error()))
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
		return
	}
	if !ok {
		// Unknown number or bad secret -- don't reveal which.
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	// Constant-time noop on a server-side static value, just to keep
	// any future per-request comparison habits consistent.
	_ = subtle.ConstantTimeCompare([]byte(secret), []byte(secret))

	if err := h.publish(r.Context(), Normalize(tenantID, voiceNumberID, ev)); err != nil {
		slog.WarnContext(r.Context(), "voice: publish",
			slog.String("err", err.Error()),
			slog.String("session_id", ev.SessionID),
			slog.String("kind", ev.Kind()))
	}

	w.Header().Set("Content-Type", "application/xml")
	switch ev.Kind() {
	case "ringing":
		_, _ = w.Write([]byte(ConsentPrompt(h.ConsentCallbackURL, "")))
	case "consent":
		if ev.DTMFDigits == "1" {
			_, _ = w.Write([]byte(BridgeToAgent(dial)))
		} else {
			_, _ = w.Write([]byte(NoRecordingBridge(dial)))
		}
	default:
		// "ended" and any other future kind: ack with a no-op response.
		_, _ = w.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?><Response/>`))
	}
}

func (h *WebhookHandler) publish(ctx context.Context, e InboundEvent) error {
	if h.JS == nil {
		return errors.New("voice: jetstream not configured")
	}
	body, err := e.MarshalNATS()
	if err != nil {
		return err
	}
	subj := fmt.Sprintf("%s.%s", SubjectPrefix, e.Kind)
	// Dedupe key: AT may re-send the same callback on retry.
	hdr := nats.Header{}
	hdr.Set(jetstream.MsgIDHeader,
		fmt.Sprintf("voice:%s:%s:%s", e.VoiceNumberID, e.SessionID, e.Kind))
	_, err = h.JS.PublishMsg(ctx, &nats.Msg{Subject: subj, Header: hdr, Data: body})
	return err
}
