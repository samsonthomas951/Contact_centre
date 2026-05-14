package routing

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrNoEligibleAgent is returned when the eligibility filter produces
// no candidates. The caller queues the ticket for the periodic
// re-evaluation job.
var ErrNoEligibleAgent = errors.New("routing: no eligible agent")

// Engine executes one routing decision per call.
type Engine struct {
	Pool *pgxpool.Pool
	// Now is overridable for tests; production passes time.Now.
	Now func() time.Time
}

// New returns an Engine bound to pool with the wall clock as Now.
func New(pool *pgxpool.Pool) *Engine {
	return &Engine{Pool: pool, Now: time.Now}
}

// AssignmentReason mirrors the values the assignments.reason CHECK
// constraint accepts.
type AssignmentReason string

const (
	ReasonAutoRoute       AssignmentReason = "auto_route"
	ReasonManual          AssignmentReason = "manual"
	ReasonEscalation      AssignmentReason = "escalation"
	ReasonReassignOffline AssignmentReason = "reassign_offline"
	ReasonSLABreach       AssignmentReason = "sla_breach"
)

// Decision is the result of Route. AgentID is the picked agent;
// FromAgentID is the previous assignee, if any (zero UUID otherwise).
type Decision struct {
	TicketID    uuid.UUID
	AgentID     uuid.UUID
	FromAgentID uuid.UUID
	Reason      AssignmentReason
}

// Route picks an agent for the ticket and persists the assignment in a
// single transaction:
//
//	1. SELECT ... FOR UPDATE on the ticket so concurrent routers can't
//	   race onto the same ticket.
//	2. Load eligible agents (online, with capacity, with skills).
//	3. Pick using the score function.
//	4. UPDATE tickets.assigned_agent_id + INSERT into assignments.
//	5. INCR agent_status.current_load on the picked agent.
//
// Returns ErrNoEligibleAgent when the filter is empty -- caller queues.
func (e *Engine) Route(ctx context.Context, tenantID, ticketID uuid.UUID, reason AssignmentReason) (Decision, error) {
	tx, err := e.Pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return Decision{}, fmt.Errorf("routing: begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	tk, fromAgent, err := loadTicketForUpdate(ctx, tx, tenantID, ticketID)
	if err != nil {
		return Decision{}, err
	}

	agents, err := loadEligibleAgents(ctx, tx, tenantID, tk.RequiredSkills)
	if err != nil {
		return Decision{}, err
	}

	picked, ok := Pick(agents, tk, e.Now())
	if !ok {
		return Decision{}, ErrNoEligibleAgent
	}

	if _, err := tx.Exec(ctx, `
		UPDATE tickets
		SET assigned_agent_id = $3, state = 'open'
		WHERE tenant_id = $1 AND id = $2`,
		tenantID, ticketID, picked.ID); err != nil {
		return Decision{}, fmt.Errorf("routing: update ticket: %w", err)
	}

	// pgx encodes a nil *uuid.UUID as SQL NULL natively; that's cleaner
	// than wrapping with NULLIF on a sentinel value (which trips type
	// inference because pgx can't deduce $3 from a NULLIF context alone).
	var fromPtr *uuid.UUID
	if fromAgent != uuid.Nil {
		f := fromAgent
		fromPtr = &f
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO assignments (ticket_id, agent_id, from_agent_id, reason)
		VALUES ($1, $2, $3::uuid, $4)`,
		ticketID, picked.ID, fromPtr, string(reason)); err != nil {
		return Decision{}, fmt.Errorf("routing: insert assignment: %w", err)
	}

	if _, err := tx.Exec(ctx, `
		UPDATE agent_status SET current_load = current_load + 1
		WHERE agent_id = $1`, picked.ID); err != nil {
		return Decision{}, fmt.Errorf("routing: incr load: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return Decision{}, fmt.Errorf("routing: commit: %w", err)
	}

	return Decision{
		TicketID:    ticketID,
		AgentID:     picked.ID,
		FromAgentID: fromAgent,
		Reason:      reason,
	}, nil
}

// loadTicketForUpdate locks the ticket row and returns the routing-time
// view plus the prior assignee (zero UUID when unassigned).
func loadTicketForUpdate(ctx context.Context, tx pgx.Tx, tenantID, ticketID uuid.UUID) (Ticket, uuid.UUID, error) {
	var (
		t        Ticket
		from     *uuid.UUID
		skills   []string
		priority int16
		created  time.Time
	)
	err := tx.QueryRow(ctx, `
		SELECT priority, required_skills, assigned_agent_id, created_at
		FROM tickets
		WHERE tenant_id = $1 AND id = $2
		FOR UPDATE`,
		tenantID, ticketID).Scan(&priority, &skills, &from, &created)
	if errors.Is(err, pgx.ErrNoRows) {
		return Ticket{}, uuid.Nil, errors.New("routing: ticket not found")
	}
	if err != nil {
		return Ticket{}, uuid.Nil, err
	}
	t.ID = ticketID
	t.Priority = priority
	t.RequiredSkills = skills
	t.CreatedAt = created
	if from != nil {
		return t, *from, nil
	}
	return t, uuid.Nil, nil
}

// loadEligibleAgents pulls all online agents with spare capacity whose
// skill set is a superset of `required` (or unrestricted when empty).
// We over-fetch (everyone online with capacity) and let the in-process
// filter handle skills — at < 100 agents per tenant the network round
// trip dominates anyway.
func loadEligibleAgents(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, required []string) ([]Agent, error) {
	rows, err := tx.Query(ctx, `
		WITH a_skills AS (
		  SELECT agent_id, array_agg(skill) AS skills
		  FROM agent_skills
		  GROUP BY agent_id
		),
		last_assign AS (
		  SELECT agent_id, MAX(at) AS last_at
		  FROM assignments
		  GROUP BY agent_id
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
		  AND COALESCE(s.status, 'offline') = 'online'
		  AND COALESCE(s.current_load, 0) < a.max_concurrent`,
		tenantID)
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
		// Filter by required skills here so the SQL stays simple.
		ok := true
		for _, req := range required {
			if !slices.Contains(a.Skills, req) {
				ok = false
				break
			}
		}
		if ok {
			out = append(out, a)
		}
	}
	return out, rows.Err()
}
