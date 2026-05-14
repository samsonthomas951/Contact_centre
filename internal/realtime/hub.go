package realtime

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
)

// Hub multiplexes Redis pub/sub onto local WebSocket connections.
// One Hub per process; one entry in the agent map per active socket.
//
// When a server-side caller (Ticket Svc, Routing, SLA) wants to push
// to agent X, it Publish()es onto AgentChannel(X). Every WS process
// running anywhere receives the publication via Redis pub/sub but
// only the process holding agent X's socket forwards it. There is no
// sticky-session requirement.
type Hub struct {
	rdb *redis.Client

	mu      sync.RWMutex
	subs    map[string][]chan []byte // channel -> sockets subscribed
	cancels map[string]context.CancelFunc
	pubsubs map[string]*redis.PubSub
	closed  bool
}

// NewHub constructs a Hub bound to a Redis client.
func NewHub(rdb *redis.Client) *Hub {
	return &Hub{
		rdb:     rdb,
		subs:    make(map[string][]chan []byte),
		cancels: make(map[string]context.CancelFunc),
		pubsubs: make(map[string]*redis.PubSub),
	}
}

// Subscribe returns a buffered channel that receives every message
// published to `channel`. The supplied ctx controls the subscription
// lifetime; cancelling it removes the subscription and (if no others
// remain) tears down the Redis pub/sub.
//
// Buffer size is 16 -- enough for normal bursts. Slow consumers drop
// messages rather than back up the entire fan-out (see warn log in
// pump()).
func (h *Hub) Subscribe(ctx context.Context, channel string) <-chan []byte {
	ch := make(chan []byte, 16)

	h.mu.Lock()
	h.subs[channel] = append(h.subs[channel], ch)
	if _, ok := h.pubsubs[channel]; !ok {
		ps := h.rdb.Subscribe(context.Background(), channel)
		h.pubsubs[channel] = ps
		pumpCtx, cancel := context.WithCancel(context.Background())
		h.cancels[channel] = cancel
		go h.pump(pumpCtx, channel, ps)
	}
	h.mu.Unlock()

	go func() {
		<-ctx.Done()
		h.unsubscribe(channel, ch)
	}()
	return ch
}

// Publish sends payload to every Hub on the cluster subscribed to channel.
func (h *Hub) Publish(ctx context.Context, channel string, payload []byte) error {
	if h == nil || h.rdb == nil {
		return errors.New("realtime: hub not configured")
	}
	return h.rdb.Publish(ctx, channel, payload).Err()
}

// pump pulls from one Redis pub/sub and fan-outs to every local
// subscriber. Uses a non-blocking send so a stuck WS doesn't take the
// rest down.
func (h *Hub) pump(ctx context.Context, channel string, ps *redis.PubSub) {
	defer ps.Close()
	msgs := ps.Channel(redis.WithChannelSize(64))
	for {
		select {
		case <-ctx.Done():
			return
		case m, ok := <-msgs:
			if !ok {
				return
			}
			h.mu.RLock()
			subs := append([]chan []byte(nil), h.subs[channel]...)
			h.mu.RUnlock()
			data := []byte(m.Payload)
			for _, c := range subs {
				select {
				case c <- data:
				default:
					slog.WarnContext(ctx, "realtime: slow subscriber dropped frame",
						slog.String("channel", channel))
				}
			}
		}
	}
}

// unsubscribe removes one local channel; if it was the last one the
// Redis subscription is torn down. Safe against races with Close():
// when the Hub is already closed, the channel was already closed by
// Close() and we leave it alone.
func (h *Hub) unsubscribe(channel string, c chan []byte) {
	h.mu.Lock()
	defer h.mu.Unlock()

	if h.closed {
		return
	}

	subs := h.subs[channel]
	found := false
	for i, existing := range subs {
		if existing == c {
			h.subs[channel] = append(subs[:i], subs[i+1:]...)
			found = true
			break
		}
	}
	if !found {
		return
	}
	close(c)

	if len(h.subs[channel]) == 0 {
		if cancel, ok := h.cancels[channel]; ok {
			cancel()
			delete(h.cancels, channel)
		}
		delete(h.pubsubs, channel)
		delete(h.subs, channel)
	}
}

// Close tears down every active subscription. Safe to call on shutdown
// and idempotent under concurrent unsubscribes (the closed flag guards
// the channel-close once).
func (h *Hub) Close() {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return
	}
	h.closed = true
	for _, cancel := range h.cancels {
		cancel()
	}
	for _, ps := range h.pubsubs {
		_ = ps.Close()
	}
	for _, subs := range h.subs {
		for _, c := range subs {
			close(c)
		}
	}
	h.subs = nil
	h.cancels = nil
	h.pubsubs = nil
}

// HealthCheck pings Redis with a short timeout so /readyz can call it.
func (h *Hub) HealthCheck(ctx context.Context) error {
	if h == nil || h.rdb == nil {
		return errors.New("realtime: hub not configured")
	}
	cctx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
	defer cancel()
	return h.rdb.Ping(cctx).Err()
}
