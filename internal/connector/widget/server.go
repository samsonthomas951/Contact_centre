package widget

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/coder/websocket"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

// SubjectPrefix is the NATS subject root for widget events.
// Full subject: ingress.widget.<kind>.
const SubjectPrefix = "ingress.widget"

// Site is the per-site config the server consults during the upgrade.
type Site struct {
	ID        uuid.UUID
	TenantID  uuid.UUID
	Origin    string
	EmbedKey  string
	Welcome   string
}

// SiteLookup resolves the WS upgrade's Origin header to a site row.
type SiteLookup interface {
	ResolveSite(ctx context.Context, origin string) (*Site, bool, error)
}

// pgSiteLookup is the production SiteLookup backed by widget_sites.
type pgSiteLookup struct{ Pool *pgxpool.Pool }

// NewPGSiteLookup is the default lookup.
func NewPGSiteLookup(p *pgxpool.Pool) SiteLookup { return &pgSiteLookup{Pool: p} }

// ResolveSite returns the active site for an origin, or (nil, false, nil)
// when no site is registered for that origin.
func (l *pgSiteLookup) ResolveSite(ctx context.Context, origin string) (*Site, bool, error) {
	var s Site
	err := l.Pool.QueryRow(ctx, `
		SELECT id, tenant_id, origin, embed_key, welcome_message
		FROM widget_sites
		WHERE origin = $1 AND active = TRUE`, origin).
		Scan(&s.ID, &s.TenantID, &s.Origin, &s.EmbedKey, &s.Welcome)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return &s, true, nil
}

// VisitorStore records and updates widget_visitors rows.
type VisitorStore interface {
	Touch(ctx context.Context, visitorID, tenantID, siteID uuid.UUID, ua string) error
	Identify(ctx context.Context, visitorID uuid.UUID, email, phone, name string) (uuid.UUID, error)
}

type pgVisitorStore struct{ Pool *pgxpool.Pool }

// NewPGVisitorStore is the production visitor store.
func NewPGVisitorStore(p *pgxpool.Pool) VisitorStore { return &pgVisitorStore{Pool: p} }

// Touch upserts the visitor row and bumps last_seen_at. Idempotent.
func (s *pgVisitorStore) Touch(ctx context.Context, visitorID, tenantID, siteID uuid.UUID, ua string) error {
	_, err := s.Pool.Exec(ctx, `
		INSERT INTO widget_visitors (id, tenant_id, site_id, user_agent)
		VALUES ($1, $2, $3, NULLIF($4,''))
		ON CONFLICT (id)
		DO UPDATE SET last_seen_at = now(), user_agent = COALESCE(EXCLUDED.user_agent, widget_visitors.user_agent)`,
		visitorID, tenantID, siteID, ua)
	return err
}

// Identify links a visitor to a customers row, creating one when no
// (tenant_id, email|phone) match exists. Returns the resolved customer id.
func (s *pgVisitorStore) Identify(ctx context.Context, visitorID uuid.UUID, email, phone, name string) (uuid.UUID, error) {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return uuid.Nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var tenantID uuid.UUID
	if err := tx.QueryRow(ctx,
		`SELECT tenant_id FROM widget_visitors WHERE id = $1 FOR UPDATE`, visitorID).
		Scan(&tenantID); err != nil {
		return uuid.Nil, err
	}

	// Try to find an existing customer for this tenant by email or phone.
	var customerID uuid.UUID
	row := tx.QueryRow(ctx, `
		SELECT id FROM customers
		WHERE tenant_id = $1
		  AND ((email IS NOT NULL AND email = NULLIF($2,''))
		    OR (phone IS NOT NULL AND phone = NULLIF($3,'')))
		LIMIT 1`,
		tenantID, email, phone)
	if err := row.Scan(&customerID); errors.Is(err, pgx.ErrNoRows) {
		if err := tx.QueryRow(ctx, `
			INSERT INTO customers (tenant_id, display_name, email, phone, external_refs)
			VALUES ($1, NULLIF($2,''), NULLIF($3,''), NULLIF($4,''),
			        jsonb_build_object('widget_visitor_id', $5::text))
			RETURNING id`,
			tenantID, name, email, phone, visitorID).Scan(&customerID); err != nil {
			return uuid.Nil, err
		}
	} else if err != nil {
		return uuid.Nil, err
	}

	if _, err := tx.Exec(ctx,
		`UPDATE widget_visitors SET customer_id = $2 WHERE id = $1`,
		visitorID, customerID); err != nil {
		return uuid.Nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return uuid.Nil, err
	}
	return customerID, nil
}

// WebsocketHandler upgrades a browser visitor to a WebSocket session
// and forwards each frame as an ingress.widget.* JetStream event.
//
// The Origin header validation is the load-bearing CSRF defence: only
// origins registered in widget_sites can connect.
type WebsocketHandler struct {
	Sites    SiteLookup
	Visitors VisitorStore
	JS       jetstream.JetStream
	// MaxMessageBytes caps inbound frame size. Default 16 KiB.
	MaxMessageBytes int64
}

// ServeHTTP performs the upgrade after validating Origin.
func (h *WebsocketHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	origin := r.Header.Get("Origin")
	if origin == "" {
		http.Error(w, "missing Origin", http.StatusBadRequest)
		return
	}
	site, ok, err := h.Sites.ResolveSite(r.Context(), origin)
	if err != nil {
		slog.ErrorContext(r.Context(), "widget: resolve site",
			slog.String("err", err.Error()))
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
		return
	}
	if !ok {
		// Origin not registered. Reject without revealing why.
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		OriginPatterns: []string{site.Origin},
	})
	if err != nil {
		return
	}
	defer func() { _ = conn.CloseNow() }()

	maxBytes := h.MaxMessageBytes
	if maxBytes == 0 {
		maxBytes = 16 << 10
	}
	conn.SetReadLimit(maxBytes)

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	h.pump(ctx, conn, site, r.UserAgent())
}

// pump reads frames from the browser and routes them. Sends an initial
// `hello_ack` with the welcome message.
func (h *WebsocketHandler) pump(ctx context.Context, conn *websocket.Conn, site *Site, ua string) {
	// Server greeting -- the client receives this before sending its
	// first hello, so the UI can render an opening message instantly.
	helloAck, _ := json.Marshal(ServerFrame{
		Type: "hello_ack", Body: site.Welcome, Ts: time.Now().UTC(),
	})
	_ = conn.Write(ctx, websocket.MessageText, helloAck)

	var visitorID uuid.UUID
	for {
		_, data, err := conn.Read(ctx)
		if err != nil {
			return
		}

		var frame ClientFrame
		if err := json.Unmarshal(data, &frame); err != nil {
			_ = writeError(ctx, conn, "invalid frame")
			continue
		}

		switch frame.Type {
		case "hello":
			var p HelloPayload
			if err := json.Unmarshal(frame.Payload, &p); err != nil || p.VisitorID == uuid.Nil {
				_ = writeError(ctx, conn, "hello: visitor_id required")
				continue
			}
			if p.SiteEmbedKey != "" && p.SiteEmbedKey != site.EmbedKey {
				_ = writeError(ctx, conn, "hello: bad embed_key")
				return
			}
			visitorID = p.VisitorID
			if err := h.Visitors.Touch(ctx, visitorID, site.TenantID, site.ID, ua); err != nil {
				slog.WarnContext(ctx, "widget: visitor touch", slog.String("err", err.Error()))
			}

		case "message":
			if visitorID == uuid.Nil {
				_ = writeError(ctx, conn, "send hello first")
				continue
			}
			if err := h.publish(ctx, InboundMessage{
				TenantID:         site.TenantID.String(),
				SiteID:           site.ID.String(),
				Channel:          "widget",
				Kind:             "message",
				CustomerExternal: visitorID.String(),
				ConversationKey:  visitorID.String(),
				Body:             frame.Body,
				OccurredAt:       time.Now().UTC(),
			}); err != nil {
				slog.WarnContext(ctx, "widget: publish", slog.String("err", err.Error()))
			}

		case "identify":
			if visitorID == uuid.Nil {
				_ = writeError(ctx, conn, "send hello first")
				continue
			}
			if frame.Email == "" && frame.Phone == "" {
				_ = writeError(ctx, conn, "identify: email or phone required")
				continue
			}
			if _, err := h.Visitors.Identify(ctx, visitorID, frame.Email, frame.Phone, frame.Name); err != nil {
				slog.WarnContext(ctx, "widget: identify", slog.String("err", err.Error()))
				_ = writeError(ctx, conn, "identify failed")
				continue
			}
			_ = h.publish(ctx, InboundMessage{
				TenantID:         site.TenantID.String(),
				SiteID:           site.ID.String(),
				Channel:          "widget",
				Kind:             "identified",
				CustomerExternal: visitorID.String(),
				CustomerEmail:    frame.Email,
				CustomerPhone:    frame.Phone,
				CustomerName:     frame.Name,
				ConversationKey:  visitorID.String(),
				OccurredAt:       time.Now().UTC(),
			})

		case "typing":
			if visitorID == uuid.Nil {
				continue
			}
			_ = h.publish(ctx, InboundMessage{
				TenantID:         site.TenantID.String(),
				SiteID:           site.ID.String(),
				Channel:          "widget",
				Kind:             "typing",
				CustomerExternal: visitorID.String(),
				ConversationKey:  visitorID.String(),
				OccurredAt:       time.Now().UTC(),
			})
		}
	}
}

// publish writes the event to JetStream. No Nats-Msg-Id dedupe -- the
// widget delivers exactly-once over a single WS so dedupe isn't needed
// (the visitor's browser doesn't retry like Meta does).
func (h *WebsocketHandler) publish(ctx context.Context, e InboundMessage) error {
	if h.JS == nil {
		return errors.New("widget: jetstream not configured")
	}
	body, err := e.MarshalNATS()
	if err != nil {
		return err
	}
	subj := fmt.Sprintf("%s.%s", SubjectPrefix, e.Kind)
	_, err = h.JS.PublishMsg(ctx, &nats.Msg{Subject: subj, Data: body})
	return err
}

func writeError(ctx context.Context, conn *websocket.Conn, msg string) error {
	body, _ := json.Marshal(ServerFrame{Type: "error", Body: msg, Ts: time.Now().UTC()})
	return conn.Write(ctx, websocket.MessageText, body)
}
