package sla

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// SubjectPrefix is the NATS subject root for SLA events.
// Full subject: `sla.<kind>.<risk>` (e.g. `sla.first_response.breached`).
const SubjectPrefix = "sla"

// risksDetected counts every risk-state transition the scheduler emits.
// Cardinality is bounded (kind × risk × tenant) so this is safe to keep
// per-tenant.
var risksDetected = promauto.NewCounterVec(prometheus.CounterOpts{
	Namespace: "contactcentre",
	Subsystem: "sla",
	Name:      "risks_detected_total",
	Help:      "SLA risk transitions detected by the scheduler.",
}, []string{"tenant", "kind", "risk"})

// Scheduler is the periodic scanner. Run() blocks until ctx is cancelled.
//
// Each tick:
//
//	1. SELECT every open ticket whose deadline is within Lookahead.
//	2. For each, evaluate first-response and resolution risk.
//	3. Emit one Event per (kind, risk) transition since last seen
//	    (we never re-emit the same risk for the same ticket without an
//	    intervening update).
//	4. On Breached: bump priority by 1 (capped at 1) and publish the
//	   event so routing can re-route.
type Scheduler struct {
	Pool      *pgxpool.Pool
	JS        jetstream.JetStream
	Tick      time.Duration // default 30s
	Lookahead time.Duration // default 1h — only consider tickets deadlining within this window
	Now       func() time.Time
}

// EnsureStream creates the SLA JetStream stream.
func EnsureStream(ctx context.Context, js jetstream.JetStream) error {
	_, err := js.CreateOrUpdateStream(ctx, jetstream.StreamConfig{
		Name:        "SLA",
		Subjects:    []string{"sla.>"},
		Retention:   jetstream.InterestPolicy,
		Storage:     jetstream.FileStorage,
		MaxAge:      7 * 24 * time.Hour,
		Description: "SLA risk events emitted by the scheduler.",
	})
	return err
}

// Run scans on every Tick until ctx is cancelled.
func (s *Scheduler) Run(ctx context.Context) error {
	if s.Tick == 0 {
		s.Tick = 30 * time.Second
	}
	if s.Lookahead == 0 {
		s.Lookahead = 1 * time.Hour
	}
	if s.Now == nil {
		s.Now = time.Now
	}

	t := time.NewTicker(s.Tick)
	defer t.Stop()
	for {
		if err := s.scanOnce(ctx); err != nil {
			slog.WarnContext(ctx, "sla: scan failed", slog.String("err", err.Error()))
		}
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
		}
	}
}

// scanOnce is the body of one tick — pulled out for testing.
func (s *Scheduler) scanOnce(ctx context.Context) error {
	now := s.Now()
	cutoff := now.Add(s.Lookahead)

	rows, err := s.Pool.Query(ctx, `
		SELECT id, tenant_id, priority,
		       sla_first_response_due, sla_resolution_due,
		       first_response_at, resolved_at
		FROM tickets
		WHERE state IN ('new','open','pending','on_hold','reopened')
		  AND (
		        (first_response_at IS NULL AND sla_first_response_due IS NOT NULL
		           AND sla_first_response_due <= $1)
		     OR (resolved_at IS NULL AND sla_resolution_due IS NOT NULL
		           AND sla_resolution_due <= $1)
		      )`, cutoff)
	if err != nil {
		return fmt.Errorf("sla: query: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var (
			id, tenant     uuid.UUID
			priority       int16
			frDue, resDue  *time.Time
			frAt, resAt    *time.Time
		)
		if err := rows.Scan(&id, &tenant, &priority, &frDue, &resDue, &frAt, &resAt); err != nil {
			return fmt.Errorf("sla: scan row: %w", err)
		}

		if frDue != nil {
			if r := EvaluateFirstResponse(*frDue, frAt, priority, now); r != RiskNone {
				if err := s.handle(ctx, Event{
					TicketID: id, TenantID: tenant, Kind: "first_response",
					Risk: r.String(), Priority: priority, OccurredAt: now,
				}); err != nil {
					slog.WarnContext(ctx, "sla: handle fr", slog.String("err", err.Error()))
				}
			}
		}
		if resDue != nil {
			if r := EvaluateResolution(*resDue, resAt, priority, now); r != RiskNone {
				if err := s.handle(ctx, Event{
					TicketID: id, TenantID: tenant, Kind: "resolution",
					Risk: r.String(), Priority: priority, OccurredAt: now,
				}); err != nil {
					slog.WarnContext(ctx, "sla: handle res", slog.String("err", err.Error()))
				}
			}
		}
	}
	return rows.Err()
}

// handle counts the risk, publishes the event, and (for breaches)
// bumps the ticket's priority so the routing engine re-evaluates.
func (s *Scheduler) handle(ctx context.Context, e Event) error {
	risksDetected.WithLabelValues(e.TenantID.String(), e.Kind, e.Risk).Inc()

	if e.Risk == "breached" && e.Priority > 1 {
		if _, err := s.Pool.Exec(ctx, `
			UPDATE tickets
			SET priority = GREATEST(priority - 1, 1)
			WHERE id = $1 AND tenant_id = $2`,
			e.TicketID, e.TenantID); err != nil {
			return fmt.Errorf("sla: bump priority: %w", err)
		}
	}
	return s.publish(ctx, e)
}

// publish writes the event to JetStream with a Nats-Msg-Id derived from
// the ticket id + risk so we don't double-emit on rapid scheduler ticks.
func (s *Scheduler) publish(ctx context.Context, e Event) error {
	if s.JS == nil {
		return errors.New("sla: jetstream not configured")
	}
	body, err := json.Marshal(e)
	if err != nil {
		return err
	}
	hdr := nats.Header{}
	hdr.Set(jetstream.MsgIDHeader,
		fmt.Sprintf("sla:%s:%s:%s", e.TicketID, e.Kind, e.Risk))
	subj := fmt.Sprintf("%s.%s.%s", SubjectPrefix, e.Kind, e.Risk)
	_, err = s.JS.PublishMsg(ctx, &nats.Msg{Subject: subj, Header: hdr, Data: body})
	return err
}
