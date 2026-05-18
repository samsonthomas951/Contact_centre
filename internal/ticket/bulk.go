package ticket

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
)

// BulkAssign sets assigned_agent_id (NULL = unassign) for every
// ticket id in `ids` that belongs to the tenant. Resolved + closed
// tickets are skipped (no point reshuffling work that's done). When
// assigning, the ticket also bumps to 'open' so the SLA timers and
// inbox views treat it as active work, mirroring the routing
// engine's behaviour.
//
// callerLimit constrains the update to tickets that are *visible*
// to the caller for reassignment:
//
//	nil               no extra scope (supervisor/admin context)
//	&callerAgentID    only rows currently unassigned OR already owned
//	                  by the caller — prevents a plain agent from
//	                  stealing someone else's queue via self-assign
//
// Returns the number of rows actually updated.
func (r *Repo) BulkAssign(ctx context.Context, tenantID uuid.UUID, agentID *uuid.UUID, ids []uuid.UUID, callerLimit *uuid.UUID) (int, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	idStrs := make([]string, len(ids))
	for i, u := range ids {
		idStrs[i] = u.String()
	}
	var ct int
	if agentID != nil {
		// Self-assign / cross-assign. The callerLimit clause is a
		// no-op when the caller is privileged (callerLimit is NULL).
		row := r.pool.QueryRow(ctx, `
			WITH updated AS (
			  UPDATE tickets
			  SET assigned_agent_id = $2,
			      state             = CASE WHEN state = 'new' THEN 'open'::ticket_state ELSE state END
			  WHERE tenant_id = $1
			    AND id = ANY($3::uuid[])
			    AND state NOT IN ('resolved','closed')
			    AND ($4::uuid IS NULL
			         OR assigned_agent_id IS NULL
			         OR assigned_agent_id = $4)
			  RETURNING 1
			)
			SELECT COUNT(*) FROM updated`,
			tenantID, agentID, idStrs, callerLimit)
		if err := row.Scan(&ct); err != nil {
			return 0, fmt.Errorf("ticket: bulk assign: %w", err)
		}
		return ct, nil
	}
	// Unassign path is admin/supervisor only — handler enforces.
	row := r.pool.QueryRow(ctx, `
		WITH updated AS (
		  UPDATE tickets
		  SET assigned_agent_id = NULL
		  WHERE tenant_id = $1
		    AND id = ANY($2::uuid[])
		    AND state NOT IN ('resolved','closed')
		  RETURNING 1
		)
		SELECT COUNT(*) FROM updated`,
		tenantID, idStrs)
	if err := row.Scan(&ct); err != nil {
		return 0, fmt.Errorf("ticket: bulk unassign: %w", err)
	}
	return ct, nil
}

// BulkFailure carries per-id errors back to the caller so partial
// success is visible (no abort-on-first).
type BulkFailure struct {
	ID     uuid.UUID `json:"id"`
	Reason string    `json:"reason"`
}

// BulkState applies a state transition across many tickets. We still
// loop per-id because ChangeState owns the transition validation +
// resolved_at/closed_at stamping, but we suppress the per-row
// realtime publish (via skipPublish) and emit a single bulk envelope
// at the end. Transition errors are collected, not aborted.
//
// The OnBulkStateChange hook (separate from OnStateChange) lets the
// gateway emit one realtime envelope per batch instead of N — keeps
// the supervisor channel quiet under load.
func (r *Repo) BulkState(ctx context.Context, tenantID uuid.UUID, ids []uuid.UUID, to State) (int, []BulkFailure, error) {
	if err := to.Validate(); err != nil {
		return 0, nil, err
	}
	ok := 0
	failures := []BulkFailure{}
	succeeded := make([]uuid.UUID, 0, len(ids))

	// Temporarily detach OnStateChange so we don't fire N realtime
	// publishes; restore on return. The hook is set once at boot, so
	// this swap is local to the batch.
	saved := r.OnStateChange
	r.OnStateChange = nil
	defer func() { r.OnStateChange = saved }()

	for _, id := range ids {
		if _, err := r.ChangeState(ctx, tenantID, id, to); err != nil {
			if errors.Is(err, ErrNotFound) {
				failures = append(failures, BulkFailure{ID: id, Reason: "not found"})
				continue
			}
			if isClientError(err) {
				failures = append(failures, BulkFailure{ID: id, Reason: err.Error()})
				continue
			}
			return ok, failures, err
		}
		ok++
		succeeded = append(succeeded, id)
	}

	// Single batched publish for the whole successful set. The
	// OnBulkStateChange hook is optional; if unset, no notification.
	if r.OnBulkStateChange != nil && len(succeeded) > 0 {
		r.OnBulkStateChange(ctx, tenantID, succeeded, to)
	}
	return ok, failures, nil
}
