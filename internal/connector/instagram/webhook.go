package instagram

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

	"github.com/samsonthomas951/contact-centre/internal/connector/meta"
)

// SubjectPrefix is the NATS subject root for IG events. Full subject:
// ingress.ig.<kind>, kind in {dm, read, comment, mention, other}.
const SubjectPrefix = "ingress.ig"

// WebhookHandler routes the IG Graph API webhook -- GET (subscription
// verify) and POST (event delivery). Mount outside /v1 auth subtree;
// Meta delivers without a bearer.
type WebhookHandler struct {
	AppSecret string
	VerifyTok string
	Resolver  TenantResolver
	JS        jetstream.JetStream
}

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

func (h *WebhookHandler) handleEvent(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
	if err != nil {
		http.Error(w, "body too large", http.StatusRequestEntityTooLarge)
		return
	}
	if !meta.VerifySignature(body, r.Header.Get(meta.SignatureHeader), h.AppSecret) {
		http.Error(w, "invalid signature", http.StatusUnauthorized)
		return
	}

	var wh Webhook
	if err := json.Unmarshal(body, &wh); err != nil {
		slog.WarnContext(r.Context(), "ig: signed but unparseable",
			slog.String("err", err.Error()))
		w.WriteHeader(http.StatusOK)
		return
	}

	for _, e := range Normalize(wh, h.Resolver.ResolveTenant) {
		if err := publish(r.Context(), h.JS, e); err != nil {
			slog.ErrorContext(r.Context(), "ig: publish failed",
				slog.String("err", err.Error()),
				slog.String("ig_user_id", e.IGUserID),
				slog.String("kind", e.Kind))
		}
	}
	w.WriteHeader(http.StatusOK)
}

func publish(ctx context.Context, js jetstream.JetStream, e InboundMessage) error {
	if js == nil {
		return errors.New("ig: jetstream not configured")
	}
	body, err := e.MarshalNATS()
	if err != nil {
		return err
	}
	subj := fmt.Sprintf("%s.%s", SubjectPrefix, e.Kind)
	hdr := nats.Header{}
	if e.PlatformMsgID != "" {
		hdr.Set(jetstream.MsgIDHeader,
			fmt.Sprintf("ig:%s:%s", e.IGUserID, e.PlatformMsgID))
	}
	_, err = js.PublishMsg(ctx, &nats.Msg{Subject: subj, Header: hdr, Data: body})
	return err
}
