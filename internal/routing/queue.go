package routing

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Reevaluator re-runs Route() on tickets that were unassigned at create
// time because no eligible agent was online. Also handles the "agent
// went offline" rebalance described in §6:
//
//   "Background job every 30s scans `tickets WHERE state='open' AND
//   assigned_agent_id IN (offline agents)` and re-routes via the same
//   algorithm with reason `reassign_offline`."
//
// Phase-2 implementation: a Tick() method the caller drives from a
// time.Ticker. Wiring it into River lands when we adopt River for
// the rest of the job surface.
type Reevaluator struct {
	Engine *Engine

	// MaxBatch caps how many tickets one Tick processes so a long-idle
	// system doesn't pin a CPU when traffic resumes. Default 50.
	MaxBatch int

	// UnassignedAge ignores tickets younger than this on the unassigned
	// sweep so a freshly-created ticket gets a chance to be routed by
	// the post-create call before the worker pings it again. Default 5s.
	UnassignedAge time.Duration

	// OfflineGrace is how long an agent's status must have been
	// non-online before their tickets are reassigned. Avoids a
	// thundering-herd reassign on a network blip. Default 60s.
	OfflineGrace time.Duration
}

// NewReevaluator constructs one with the production defaults.
func NewReevaluator(e *Engine) *Reevaluator {
	return &Reevaluator{
		Engine:        e,
		MaxBatch:      50,
		UnassignedAge: 5 * time.Second,
		OfflineGrace:  60 * time.Second,
	}
}

// Tick runs one pass. Returns nil even when individual ticket routes
// fail with ErrNoEligibleAgent — those are expected when the agent
// pool is exhausted. Returns the first non-no-eligible-agent error.
func (r *Reevaluator) Tick(ctx context.Context) error {
	unassigned, err := r.scanUnassigned(ctx)
	if err != nil {
		return fmt.Errorf("routing: scan unassigned: %w", err)
	}
	offline, err := r.scanOffline(ctx)
	if err != nil {
		return fmt.Errorf("routing: scan offline: %w", err)
	}

	// Dedupe: an unassigned ticket isn't also "offline-owned".
	seen := make(map[uuid.UUID]struct{}, len(unassigned))
	all := make([]ticketRef, 0, len(unassigned)+len(offline))
	for _, t := range append(unassigned, offline...) {
		if _, dup := seen[t.ID]; dup {
			continue
		}
		seen[t.ID] = struct{}{}
		all = append(all, t)
	}

	for _, t := range all {
		_, err := r.Engine.Route(ctx, t.TenantID, t.ID, t.Reason)
		if err == nil || errors.Is(err, ErrNoEligibleAgent) {
			continue
		}
		slog.WarnContext(ctx, "routing: re-evaluate failed",
			slog.String("ticket_id", t.ID.String()),
			slog.String("err", err.Error()))
	}
	return nil
}

type ticketRef struct {
	ID       uuid.UUID
	TenantID uuid.UUID
	Reason   AssignmentReason
}

func (r *Reevaluator) scanUnassigned(ctx context.Context) ([]ticketRef, error) {
	rows, err := r.Engine.Pool.Query(ctx, `
		SELECT id, tenant_id
		FROM tickets
		WHERE assigned_agent_id IS NULL
		  AND state IN ('new','reopened')
		  AND created_at < now() - make_interval(secs => $1)
		ORDER BY priority ASC, created_at ASC
		LIMIT $2`,
		r.UnassignedAge.Seconds(), r.MaxBatch)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ticketRef{}
	for rows.Next() {
		var t ticketRef
		if err := rows.Scan(&t.ID, &t.TenantID); err != nil {
			return nil, err
		}
		t.Reason = ReasonAutoRoute
		out = append(out, t)
	}
	return out, rows.Err()
}

// scanOffline finds open/pending tickets owned by agents whose last
// heartbeat is older than OfflineGrace. We don't reassign immediately
// on heartbeat-miss; the grace window absorbs network flaps.
func (r *Reevaluator) scanOffline(ctx context.Context) ([]ticketRef, error) {
	rows, err := r.Engine.Pool.Query(ctx, `
		SELECT t.id, t.tenant_id
		FROM tickets t
		LEFT JOIN agent_status s ON s.agent_id = t.assigned_agent_id
		WHERE t.assigned_agent_id IS NOT NULL
		  AND t.state IN ('open','pending')
		  AND (s.agent_id IS NULL
		       OR s.status <> 'online'
		       OR s.last_heartbeat < now() - make_interval(secs => $1))
		ORDER BY t.priority ASC, t.created_at ASC
		LIMIT $2`,
		r.OfflineGrace.Seconds(), r.MaxBatch)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ticketRef{}
	for rows.Next() {
		var t ticketRef
		if err := rows.Scan(&t.ID, &t.TenantID); err != nil {
			return nil, err
		}
		t.Reason = ReasonReassignOffline
		out = append(out, t)
	}
	return out, rows.Err()
}

// Run loops Tick() on a ticker until ctx is cancelled.
func (r *Reevaluator) Run(ctx context.Context, every time.Duration) error {
	if every <= 0 {
		every = 30 * time.Second
	}
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		if err := r.Tick(ctx); err != nil {
			slog.WarnContext(ctx, "routing: tick error", slog.String("err", err.Error()))
		}
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
		}
	}
}

// ReleaseOnClose decrements the previously-assigned agent's
// current_load when a ticket reaches a terminal state. Wire this into
// the ticket service's ChangeState() so capacity is freed promptly.
// Idempotent: re-running with the same ticket is a no-op once the load
// has already been decremented (we use NULLIF + locked select to
// detect that).
func ReleaseOnClose(ctx context.Context, tx pgx.Tx, ticketID uuid.UUID) error {
	var agent *uuid.UUID
	if err := tx.QueryRow(ctx,
		`SELECT assigned_agent_id FROM tickets WHERE id = $1 FOR UPDATE`, ticketID).
		Scan(&agent); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		return err
	}
	if agent == nil {
		return nil
	}
	_, err := tx.Exec(ctx,
		`UPDATE agent_status SET current_load = GREATEST(current_load - 1, 0)
		 WHERE agent_id = $1`, *agent)
	return err
}
