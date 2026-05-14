package routing

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// EscalationTier is the chain a ticket walks when an SLA breach or
// manual escalation fires.
type EscalationTier string

const (
	TierAgent       EscalationTier = "agent"
	TierSeniorAgent EscalationTier = "senior_agent"
	TierSupervisor  EscalationTier = "supervisor"
)

// nextTier returns the tier above the current one, or empty when
// we're already at the top.
func nextTier(t EscalationTier) EscalationTier {
	switch t {
	case TierAgent:
		return TierSeniorAgent
	case TierSeniorAgent:
		return TierSupervisor
	default:
		return ""
	}
}

// Escalate moves a ticket up one tier and re-routes to an agent at the
// new tier. Idempotent in spirit: if the ticket's currently-assigned
// agent already sits at the target tier, the call is a no-op.
//
// `from` is the optional inferred current tier; pass empty to let
// Escalate read it from the assigned agent's role.
func (e *Engine) Escalate(ctx context.Context, tenantID, ticketID uuid.UUID, from EscalationTier) (Decision, error) {
	tx, err := e.Pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return Decision{}, fmt.Errorf("routing: escalate begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var fromAgent *uuid.UUID
	if err := tx.QueryRow(ctx,
		`SELECT assigned_agent_id FROM tickets WHERE tenant_id = $1 AND id = $2 FOR UPDATE`,
		tenantID, ticketID).Scan(&fromAgent); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Decision{}, errors.New("routing: ticket not found")
		}
		return Decision{}, err
	}

	if from == "" && fromAgent != nil {
		var role string
		if err := tx.QueryRow(ctx, `SELECT role FROM agents WHERE id = $1`, *fromAgent).Scan(&role); err == nil {
			from = EscalationTier(role)
		}
	}
	target := nextTier(from)
	if target == "" {
		return Decision{}, errors.New("routing: already at top tier; cannot escalate further")
	}

	// Find an eligible agent at the target tier. We use the same
	// eligibility filter as auto-route but additionally require
	// agents.role = target tier.
	candidates, err := loadEligibleAgentsAtTier(ctx, tx, tenantID, target)
	if err != nil {
		return Decision{}, err
	}
	if len(candidates) == 0 {
		return Decision{}, ErrNoEligibleAgent
	}

	// Build a minimal ticket view so the scoring function works.
	var priority int16
	if err := tx.QueryRow(ctx,
		`SELECT priority FROM tickets WHERE id = $1`, ticketID).Scan(&priority); err != nil {
		return Decision{}, err
	}
	tk := Ticket{ID: ticketID, Priority: priority}
	picked, ok := Pick(candidates, tk, e.Now())
	if !ok {
		return Decision{}, ErrNoEligibleAgent
	}

	if _, err := tx.Exec(ctx,
		`UPDATE tickets SET assigned_agent_id = $3
		 WHERE tenant_id = $1 AND id = $2`,
		tenantID, ticketID, picked.ID); err != nil {
		return Decision{}, err
	}

	from32 := uuid.Nil
	if fromAgent != nil {
		from32 = *fromAgent
	}
	var fromPtr *uuid.UUID
	if from32 != uuid.Nil {
		f := from32
		fromPtr = &f
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO assignments (ticket_id, agent_id, from_agent_id, reason)
		VALUES ($1, $2, $3::uuid, $4)`,
		ticketID, picked.ID, fromPtr, string(ReasonEscalation)); err != nil {
		return Decision{}, err
	}
	if _, err := tx.Exec(ctx,
		`UPDATE agent_status SET current_load = current_load + 1 WHERE agent_id = $1`,
		picked.ID); err != nil {
		return Decision{}, err
	}
	if fromPtr != nil {
		// The previous assignee gets capacity back.
		if _, err := tx.Exec(ctx,
			`UPDATE agent_status SET current_load = GREATEST(current_load - 1, 0)
			 WHERE agent_id = $1`, *fromPtr); err != nil {
			return Decision{}, err
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return Decision{}, err
	}

	return Decision{
		TicketID:    ticketID,
		AgentID:     picked.ID,
		FromAgentID: from32,
		Reason:      ReasonEscalation,
	}, nil
}

// loadEligibleAgentsAtTier is loadEligibleAgents() with an extra
// `agents.role = $tier` filter.
func loadEligibleAgentsAtTier(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, tier EscalationTier) ([]Agent, error) {
	rows, err := tx.Query(ctx, `
		WITH a_skills AS (
		  SELECT agent_id, array_agg(skill) AS skills
		  FROM agent_skills GROUP BY agent_id
		),
		last_assign AS (
		  SELECT agent_id, MAX(at) AS last_at FROM assignments GROUP BY agent_id
		)
		SELECT a.id, a.max_concurrent, COALESCE(s.current_load, 0),
		       COALESCE(s.status, 'offline'),
		       COALESCE(sk.skills, '{}'::text[]),
		       COALESCE(la.last_at, 'epoch'::timestamptz)
		FROM agents a
		LEFT JOIN agent_status s  ON s.agent_id  = a.id
		LEFT JOIN a_skills    sk  ON sk.agent_id = a.id
		LEFT JOIN last_assign la  ON la.agent_id = a.id
		WHERE a.tenant_id = $1
		  AND a.active = TRUE
		  AND a.role = $2
		  AND COALESCE(s.status, 'offline') = 'online'
		  AND COALESCE(s.current_load, 0) < a.max_concurrent`,
		tenantID, string(tier))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Agent{}
	for rows.Next() {
		var a Agent
		if err := rows.Scan(&a.ID, &a.MaxConcurrent, &a.CurrentLoad, &a.Status, &a.Skills, &a.LastAssigned); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}
