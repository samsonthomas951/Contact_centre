// Package supervisor exposes read-only dashboards used by the
// supervisor / admin / DPO / auditor roles. Every query is tenant-
// scoped and read-only -- the package never mutates state.
//
// All endpoints are gated to the four oversight roles by the gateway:
//
//	auth.RequireRole(RoleSupervisor, RoleAdmin, RoleAuditor, RoleDPO)
package supervisor

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Repo is the read-only data access for the dashboard endpoints.
type Repo struct{ Pool *pgxpool.Pool }

// NewRepo binds a Repo to a pool.
func NewRepo(pool *pgxpool.Pool) *Repo { return &Repo{Pool: pool} }

// QueueDepth is the per-state ticket count for one tenant. Used by the
// "How big is the backlog?" widget.
type QueueDepth struct {
	State string `json:"state"`
	Count int    `json:"count"`
}

// QueueDepth returns one row per ticket_state for the tenant.
func (r *Repo) QueueDepth(ctx context.Context, tenantID uuid.UUID) ([]QueueDepth, error) {
	rows, err := r.Pool.Query(ctx, `
		SELECT state::text, COUNT(*)
		FROM tickets
		WHERE tenant_id = $1
		GROUP BY state
		ORDER BY state`, tenantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []QueueDepth{}
	for rows.Next() {
		var q QueueDepth
		if err := rows.Scan(&q.State, &q.Count); err != nil {
			return nil, err
		}
		out = append(out, q)
	}
	return out, rows.Err()
}

// AgentPresence is the at-a-glance row for the "Who's available?" panel.
type AgentPresence struct {
	AgentID       uuid.UUID `json:"agent_id"`
	DisplayName   string    `json:"display_name"`
	Status        string    `json:"status"`
	CurrentLoad   int16     `json:"current_load"`
	MaxConcurrent int16     `json:"max_concurrent"`
	LastHeartbeat time.Time `json:"last_heartbeat"`
}

// AgentPresence returns one row per active agent in the tenant.
func (r *Repo) AgentPresence(ctx context.Context, tenantID uuid.UUID) ([]AgentPresence, error) {
	rows, err := r.Pool.Query(ctx, `
		SELECT a.id, a.display_name,
		       COALESCE(s.status, 'offline'),
		       COALESCE(s.current_load, 0),
		       a.max_concurrent,
		       COALESCE(s.last_heartbeat, 'epoch'::timestamptz)
		FROM agents a
		LEFT JOIN agent_status s ON s.agent_id = a.id
		WHERE a.tenant_id = $1 AND a.active = TRUE
		ORDER BY a.display_name`, tenantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AgentPresence{}
	for rows.Next() {
		var p AgentPresence
		if err := rows.Scan(&p.AgentID, &p.DisplayName, &p.Status,
			&p.CurrentLoad, &p.MaxConcurrent, &p.LastHeartbeat); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// AtRiskTicket is the "what should I escalate next?" row. Surfaces
// tickets within `withinDuration` of breach -- the supervisor UI sets
// this to e.g. 30 minutes for first-response and 4 hours for
// resolution.
type AtRiskTicket struct {
	TicketID        uuid.UUID `json:"ticket_id"`
	Priority        int16     `json:"priority"`
	State           string    `json:"state"`
	AssignedAgentID *uuid.UUID `json:"assigned_agent_id,omitempty"`
	FirstResponseDue *time.Time `json:"first_response_due,omitempty"`
	ResolutionDue   *time.Time `json:"resolution_due,omitempty"`
	Created         time.Time `json:"created_at"`
}

// AtRiskTickets returns up to limit open tickets whose first-response
// or resolution deadline lies within `within` of now. Sorted by
// soonest-deadline first so the operator triages top-down.
func (r *Repo) AtRiskTickets(ctx context.Context, tenantID uuid.UUID, within time.Duration, limit int) ([]AtRiskTicket, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	cutoff := time.Now().Add(within)
	rows, err := r.Pool.Query(ctx, `
		SELECT id, priority, state::text, assigned_agent_id,
		       sla_first_response_due, sla_resolution_due, created_at
		FROM tickets
		WHERE tenant_id = $1
		  AND state IN ('new','open','pending','on_hold','reopened')
		  AND (
		        (first_response_at IS NULL AND sla_first_response_due IS NOT NULL
		           AND sla_first_response_due <= $2)
		     OR (resolved_at IS NULL AND sla_resolution_due IS NOT NULL
		           AND sla_resolution_due <= $2)
		      )
		ORDER BY LEAST(
		    COALESCE(sla_first_response_due, 'infinity'::timestamptz),
		    COALESCE(sla_resolution_due,     'infinity'::timestamptz)
		  ) ASC
		LIMIT $3`,
		tenantID, cutoff, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []AtRiskTicket{}
	for rows.Next() {
		var t AtRiskTicket
		if err := rows.Scan(&t.TicketID, &t.Priority, &t.State, &t.AssignedAgentID,
			&t.FirstResponseDue, &t.ResolutionDue, &t.Created); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}
