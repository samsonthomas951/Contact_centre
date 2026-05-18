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
// Returns the number of rows actually updated.
func (r *Repo) BulkAssign(ctx context.Context, tenantID uuid.UUID, agentID *uuid.UUID, ids []uuid.UUID) (int, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	idStrs := make([]string, len(ids))
	for i, u := range ids {
		idStrs[i] = u.String()
	}
	var ct int
	if agentID != nil {
		row := r.pool.QueryRow(ctx, `
			WITH updated AS (
			  UPDATE tickets
			  SET assigned_agent_id = $2,
			      state             = CASE WHEN state = 'new' THEN 'open'::ticket_state ELSE state END
			  WHERE tenant_id = $1
			    AND id = ANY($3::uuid[])
			    AND state NOT IN ('resolved','closed')
			  RETURNING 1
			)
			SELECT COUNT(*) FROM updated`,
			tenantID, agentID, idStrs)
		if err := row.Scan(&ct); err != nil {
			return 0, fmt.Errorf("ticket: bulk assign: %w", err)
		}
		return ct, nil
	}
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

// BulkState walks each id and calls ChangeState. Transition errors
// (e.g. closed->new) are collected into `failures` rather than
// aborting the whole batch -- the agent sees partial success in the
// UI and can retry the ones that fell out.
type BulkFailure struct {
	ID     uuid.UUID `json:"id"`
	Reason string    `json:"reason"`
}

func (r *Repo) BulkState(ctx context.Context, tenantID uuid.UUID, ids []uuid.UUID, to State) (int, []BulkFailure, error) {
	if err := to.Validate(); err != nil {
		return 0, nil, err
	}
	ok := 0
	failures := []BulkFailure{}
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
	}
	return ok, failures, nil
}
