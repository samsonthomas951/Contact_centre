package webhooks

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// MaxAttempts caps retries for one event per subscription. With the
// 1s/4s/16s/64s schedule that's 5 attempts over ~85s wall-clock.
const MaxAttempts = 5

// AutoDisableThreshold disables a subscription after this many
// consecutive failed events (any cause). Tenants re-enable manually.
const AutoDisableThreshold = 50

// DeliveryTimeout is the per-attempt HTTP timeout.
const DeliveryTimeout = 10 * time.Second

// Subscription is the in-memory view of webhook_subscriptions.
type Subscription struct {
	ID        uuid.UUID
	TenantID  uuid.UUID
	URL       string
	Secret    string // decrypted
	Subjects  []string
	Active    bool
}

// Event is one matched event being delivered.
type Event struct {
	ID      string          // NATS msg id (correlation_id-derived)
	Subject string
	Ts      time.Time
	Body    []byte          // already JSON
}

// Deliverer wraps the HTTP client + pgxpool for the delivery worker.
type Deliverer struct {
	HTTP *http.Client
	Pool *pgxpool.Pool
}

// NewDeliverer constructs a Deliverer with sane defaults.
func NewDeliverer(pool *pgxpool.Pool) *Deliverer {
	return &Deliverer{
		HTTP: &http.Client{
			Timeout: DeliveryTimeout,
			// Defaults for an outbound that lives in front of public
			// internet -- bound idle conns per host so a misbehaving
			// brand doesn't pin our descriptors.
			Transport: &http.Transport{
				MaxIdleConnsPerHost:   4,
				MaxConnsPerHost:       8,
				IdleConnTimeout:       90 * time.Second,
				ResponseHeaderTimeout: DeliveryTimeout,
			},
		},
		Pool: pool,
	}
}

// Deliver attempts one POST. The retry loop is the caller's
// responsibility -- River, the NATS consumer, or a unit test all
// supply their own scheduling. This keeps Deliver pure: one attempt,
// one row appended to webhook_deliveries, one classification of the
// outcome.
//
// Outcome categories drive the caller's retry decision:
//
//   delivered=true                     -> done.
//   classify == ClassifyTransient      -> retry with backoff.
//   classify == ClassifyPermanent      -> stop; brand can re-subscribe.
type DeliveryOutcome struct {
	Delivered  bool
	Class      Classification
	StatusCode int
	Err        string
	DurationMS int
}

// Classification tells the caller what to do next.
type Classification int

const (
	ClassifyDelivered Classification = iota
	ClassifyTransient
	ClassifyPermanent
)

// Deliver POSTs body to sub.URL with the signature headers and
// records the attempt in webhook_deliveries.
func (d *Deliverer) Deliver(ctx context.Context, sub Subscription, ev Event, attempt int) (DeliveryOutcome, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, sub.URL, bytes.NewReader(ev.Body))
	if err != nil {
		return d.recordOutcome(ctx, sub, ev, attempt, DeliveryOutcome{
			Class: ClassifyPermanent, Err: err.Error(),
		})
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(SignatureHeader, Sign(ev.Body, sub.Secret))
	req.Header.Set("X-Webhook-Id", sub.ID.String())
	req.Header.Set("X-Event-Id", ev.ID)
	req.Header.Set("X-Event-Subject", ev.Subject)
	req.Header.Set("X-Event-Ts", ev.Ts.UTC().Format(time.RFC3339))
	req.Header.Set("X-Attempt", fmt.Sprintf("%d", attempt))
	req.Header.Set("User-Agent", "contactcentre-webhooks/1.0")

	start := time.Now()
	resp, err := d.HTTP.Do(req)
	dur := int(time.Since(start) / time.Millisecond)
	if err != nil {
		return d.recordOutcome(ctx, sub, ev, attempt, DeliveryOutcome{
			Class: ClassifyTransient, Err: err.Error(), DurationMS: dur,
		})
	}
	defer func() {
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}()

	out := DeliveryOutcome{StatusCode: resp.StatusCode, DurationMS: dur}
	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		out.Delivered = true
		out.Class = ClassifyDelivered
	case resp.StatusCode == 410:
		// Brand says "this endpoint is gone" -- disable the subscription.
		out.Class = ClassifyPermanent
		out.Err = "410 Gone"
	case resp.StatusCode == 408 || resp.StatusCode == 429 || resp.StatusCode >= 500:
		out.Class = ClassifyTransient
		out.Err = fmt.Sprintf("HTTP %d", resp.StatusCode)
	default:
		out.Class = ClassifyPermanent
		out.Err = fmt.Sprintf("HTTP %d", resp.StatusCode)
	}
	return d.recordOutcome(ctx, sub, ev, attempt, out)
}

// recordOutcome inserts the delivery row and updates the subscription's
// failure counters. Best-effort: failing to record is logged but does
// not flip the outcome the caller acts on.
func (d *Deliverer) recordOutcome(ctx context.Context, sub Subscription, ev Event, attempt int, out DeliveryOutcome) (DeliveryOutcome, error) {
	if d.Pool == nil {
		return out, nil
	}
	if _, err := d.Pool.Exec(ctx, `
		INSERT INTO webhook_deliveries
		  (subscription_id, event_id, subject, attempt, status_code, error,
		   delivered, duration_ms)
		VALUES ($1, $2, $3, $4, NULLIF($5, 0), NULLIF($6, ''), $7, $8)`,
		sub.ID, ev.ID, ev.Subject, attempt, out.StatusCode, out.Err,
		out.Delivered, out.DurationMS); err != nil {
		slog.WarnContext(ctx, "webhooks: record delivery",
			slog.String("err", err.Error()))
	}

	switch out.Class {
	case ClassifyDelivered:
		_, _ = d.Pool.Exec(ctx,
			`UPDATE webhook_subscriptions SET consecutive_failures = 0
			 WHERE id = $1`, sub.ID)
	case ClassifyPermanent:
		_, _ = d.Pool.Exec(ctx, `
			UPDATE webhook_subscriptions
			SET active = FALSE, disabled_at = now(),
			    disabled_reason = COALESCE(disabled_reason, $2)
			WHERE id = $1`, sub.ID, "permanent failure: "+out.Err)
	case ClassifyTransient:
		// Bump consecutive_failures; auto-disable past the threshold.
		_, _ = d.Pool.Exec(ctx, `
			UPDATE webhook_subscriptions
			SET consecutive_failures = consecutive_failures + 1,
			    active      = active AND consecutive_failures + 1 < $2,
			    disabled_at = CASE WHEN consecutive_failures + 1 >= $2 THEN now() ELSE disabled_at END,
			    disabled_reason = CASE WHEN consecutive_failures + 1 >= $2
			                           THEN 'auto-disabled: too many consecutive failures'
			                           ELSE disabled_reason END
			WHERE id = $1`, sub.ID, AutoDisableThreshold)
	}
	return out, nil
}

// BackoffFor returns the wait time before attempt N (1-indexed). The
// schedule is 1s, 4s, 16s, 64s, then permanent failure -- short
// enough that a flaky brand still gets prompt delivery, long enough
// that a sustained outage doesn't burn our worker budget.
func BackoffFor(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	d := time.Second
	for i := 1; i < attempt; i++ {
		d *= 4
		if d > 5*time.Minute {
			d = 5 * time.Minute
			break
		}
	}
	return d
}

// RandomEventID is a helper for callers that publish to NATS without a
// preset id; produces a 16-byte hex.
func RandomEventID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// ErrDisabled is returned by callers when they short-circuit a fan-out
// for an inactive subscription.
var ErrDisabled = errors.New("webhooks: subscription disabled")
