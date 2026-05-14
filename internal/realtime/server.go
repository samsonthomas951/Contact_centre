package realtime

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/coder/websocket"

	"github.com/samsonthomas951/contact-centre/internal/auth"
)

// HeartbeatInterval is how often the server pings the client. Per §10
// the agent UI heartbeats every 15s; we mirror that here so a missed
// pong removes the socket within ~30s.
const HeartbeatInterval = 15 * time.Second

// AgentSocketHandler upgrades the request to a WebSocket bound to the
// authenticated agent's identity. Subscribes the socket to that
// agent's Redis channel, plus the tenant's supervisor channel when
// the identity has the supervisor role.
//
// Auth flow: the JWT is read from the `Sec-WebSocket-Protocol` header
// per the plan ("JWT in subprotocol header: bearer.<jwt>"). This sits
// behind the same auth.Verifier the rest of the API uses, so token
// rotation rules are uniform.
type AgentSocketHandler struct {
	Hub      *Hub
	Verifier *auth.Verifier
}

// ServeHTTP performs the upgrade. Connections that fail auth get a 401
// without ever opening the WS, so a single curl probe with bad creds
// can't tie up server file descriptors.
func (h *AgentSocketHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	tok := bearerFromSubprotocol(r.Header.Get("Sec-WebSocket-Protocol"))
	if tok == "" {
		// Fall back to Authorization header for tools that don't use
		// the subprotocol (curl, k6).
		ah := r.Header.Get("Authorization")
		if strings.HasPrefix(ah, "Bearer ") {
			tok = strings.TrimSpace(ah[len("Bearer "):])
		}
	}
	if tok == "" {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	id, err := h.Verifier.Verify(r.Context(), tok)
	if err != nil {
		slog.WarnContext(r.Context(), "realtime: token rejected",
			slog.String("err", err.Error()))
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		Subprotocols:   []string{"bearer." + tok},
		OriginPatterns: []string{"*"}, // gateway is the only public ingress
	})
	if err != nil {
		return // websocket.Accept already wrote a response
	}
	defer conn.CloseNow()

	// Per-socket context lives only as long as the connection. Cancelling
	// it tears down every Hub subscription we registered.
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	channels := []string{AgentChannel(id.AgentID.String())}
	if id.HasRole(auth.RoleSupervisor, auth.RoleAdmin) {
		channels = append(channels, SupervisorChannel(id.TenantID.String()))
	}

	out := make(chan []byte, 32)
	for _, ch := range channels {
		sub := h.Hub.Subscribe(ctx, ch)
		go forwardToSocket(sub, out)
	}

	// Heartbeat goroutine: write pings on a ticker, cancel on first error.
	pingErr := make(chan error, 1)
	go func() {
		t := time.NewTicker(HeartbeatInterval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				if err := conn.Ping(ctx); err != nil {
					pingErr <- err
					return
				}
			}
		}
	}()

	for {
		select {
		case <-ctx.Done():
			return
		case err := <-pingErr:
			slog.InfoContext(ctx, "realtime: heartbeat failed, closing",
				slog.String("err", err.Error()),
				slog.String("agent_id", id.AgentID.String()))
			return
		case msg, ok := <-out:
			if !ok {
				return
			}
			wctx, wcancel := context.WithTimeout(ctx, 5*time.Second)
			err := conn.Write(wctx, websocket.MessageText, msg)
			wcancel()
			if err != nil {
				if !errors.Is(err, context.Canceled) {
					slog.InfoContext(ctx, "realtime: write failed",
						slog.String("err", err.Error()),
						slog.String("agent_id", id.AgentID.String()))
				}
				return
			}
		}
	}
}

// forwardToSocket merges a Hub subscription onto the per-socket out
// channel. We need the merge so a single-channel select{} on out is
// enough -- otherwise multiple subscriptions would each need their own
// select arm.
func forwardToSocket(in <-chan []byte, out chan<- []byte) {
	for msg := range in {
		select {
		case out <- msg:
		default:
			// Drop frames when the socket buffer is full; matches the
			// behaviour of Hub.pump() for slow consumers.
		}
	}
}

// bearerFromSubprotocol picks the "bearer.<jwt>" token out of the
// Sec-WebSocket-Protocol header. Per §10 the client sends
// `bearer.<jwt>` as a subprotocol; we reflect that back in
// AcceptOptions.Subprotocols so the browser handshake completes.
func bearerFromSubprotocol(h string) string {
	if h == "" {
		return ""
	}
	for part := range strings.SplitSeq(h, ",") {
		p := strings.TrimSpace(part)
		const prefix = "bearer."
		if strings.HasPrefix(p, prefix) {
			return p[len(prefix):]
		}
	}
	return ""
}
