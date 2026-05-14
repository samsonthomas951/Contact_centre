package ticket

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/samsonthomas951/contact-centre/internal/sla"
)

// ErrNotFound is returned when a row lookup misses.
var ErrNotFound = errors.New("ticket: not found")

// Repo is the data-access layer for tickets and messages.
//
// Sqlc-generated queries land in internal/ticket/db; for Phase 0 the
// surface area is small enough to keep handwritten. We'll move to sqlc
// when the query count grows.
type Repo struct{ pool *pgxpool.Pool }

// NewRepo binds a Repo to a pool.
func NewRepo(pool *pgxpool.Pool) *Repo { return &Repo{pool: pool} }

// CreateTicketParams is what the Ticket service accepts to open a new
// ticket. The state machine guarantees the initial state is `new`.
type CreateTicketParams struct {
	TenantID       uuid.UUID
	ConversationID uuid.UUID
	Priority       int16
	RequiredSkills []string
}

// CreateTicket inserts a new ticket in state=new with SLA deadlines
// derived from the priority's policy, and returns it.
func (r *Repo) CreateTicket(ctx context.Context, p CreateTicketParams) (*Ticket, error) {
	if p.Priority == 0 {
		p.Priority = 3
	}
	if p.RequiredSkills == nil {
		p.RequiredSkills = []string{}
	}
	now := time.Now().UTC()
	frDue, resDue := sla.Deadlines(now, p.Priority)

	row := r.pool.QueryRow(ctx, `
		INSERT INTO tickets (tenant_id, conversation_id, state, priority, required_skills,
		                     sla_first_response_due, sla_resolution_due)
		VALUES ($1, $2, 'new', $3, $4, $5, $6)
		RETURNING id, tenant_id, conversation_id, state, priority, required_skills,
		          assigned_agent_id, sla_first_response_due, sla_resolution_due,
		          first_response_at, resolved_at, closed_at, created_at, updated_at`,
		p.TenantID, p.ConversationID, p.Priority, p.RequiredSkills, frDue, resDue,
	)
	return scanTicket(row)
}

// GetTicket loads a single ticket; returns ErrNotFound when missing.
func (r *Repo) GetTicket(ctx context.Context, tenantID, id uuid.UUID) (*Ticket, error) {
	row := r.pool.QueryRow(ctx, `
		SELECT id, tenant_id, conversation_id, state, priority, required_skills,
		       assigned_agent_id, sla_first_response_due, sla_resolution_due,
		       first_response_at, resolved_at, closed_at, created_at, updated_at
		FROM tickets
		WHERE tenant_id = $1 AND id = $2`,
		tenantID, id,
	)
	t, err := scanTicket(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return t, err
}

// ChangeState moves the ticket to `to` if the transition is legal.
// resolved_at / closed_at are stamped automatically when the target state
// is one of those terminal-ish states.
func (r *Repo) ChangeState(ctx context.Context, tenantID, id uuid.UUID, to State) (*Ticket, error) {
	if err := to.Validate(); err != nil {
		return nil, err
	}

	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return nil, fmt.Errorf("ticket: begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var current State
	if err := tx.QueryRow(ctx,
		`SELECT state FROM tickets WHERE tenant_id = $1 AND id = $2 FOR UPDATE`,
		tenantID, id).Scan(&current); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	if err := Transition(current, to); err != nil {
		return nil, err
	}

	now := time.Now().UTC()
	var resolvedAt, closedAt *time.Time
	switch to {
	case StateResolved:
		resolvedAt = &now
	case StateClosed:
		closedAt = &now
	}

	row := tx.QueryRow(ctx, `
		UPDATE tickets
		SET state = $3,
		    resolved_at = COALESCE($4, resolved_at),
		    closed_at   = COALESCE($5, closed_at)
		WHERE tenant_id = $1 AND id = $2
		RETURNING id, tenant_id, conversation_id, state, priority, required_skills,
		          assigned_agent_id, sla_first_response_due, sla_resolution_due,
		          first_response_at, resolved_at, closed_at, created_at, updated_at`,
		tenantID, id, to, resolvedAt, closedAt,
	)
	t, err := scanTicket(row)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return t, nil
}

// AppendMessageParams is the input for adding to a ticket's conversation.
type AppendMessageParams struct {
	TenantID          uuid.UUID
	TicketID          uuid.UUID
	Direction         Direction
	AgentID           *uuid.UUID
	Body              string
	Attachments       []uuid.UUID
	PlatformMessageID *string
}

// AppendMessage records a message and stamps first_response_at on the
// ticket if it's the first outbound from an agent.
func (r *Repo) AppendMessage(ctx context.Context, p AppendMessageParams) (*Message, error) {
	if p.Attachments == nil {
		p.Attachments = []uuid.UUID{}
	}

	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return nil, fmt.Errorf("ticket: begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	row := tx.QueryRow(ctx, `
		INSERT INTO messages
		  (tenant_id, ticket_id, direction, agent_id, body, attachments, platform_message_id)
		VALUES ($1,$2,$3,$4,$5,$6,$7)
		RETURNING id, tenant_id, ticket_id, direction, agent_id, body,
		          attachments, platform_message_id, created_at`,
		p.TenantID, p.TicketID, p.Direction, p.AgentID, p.Body,
		p.Attachments, p.PlatformMessageID,
	)
	m, err := scanMessage(row)
	if err != nil {
		return nil, err
	}

	if p.Direction == DirectionOut && p.AgentID != nil {
		if _, err := tx.Exec(ctx, `
			UPDATE tickets SET first_response_at = COALESCE(first_response_at, $3)
			WHERE tenant_id = $1 AND id = $2`,
			p.TenantID, p.TicketID, m.CreatedAt); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return m, nil
}

// ListMessages returns messages for a ticket newest-first; pagination is
// keyset on created_at.
func (r *Repo) ListMessages(ctx context.Context, tenantID, ticketID uuid.UUID, limit int) ([]Message, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := r.pool.Query(ctx, `
		SELECT id, tenant_id, ticket_id, direction, agent_id, body,
		       attachments, platform_message_id, created_at
		FROM messages
		WHERE tenant_id = $1 AND ticket_id = $2
		ORDER BY created_at DESC, id DESC
		LIMIT $3`, tenantID, ticketID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]Message, 0, limit)
	for rows.Next() {
		m, err := scanMessage(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *m)
	}
	return out, rows.Err()
}

// scanRow is the shared interface for QueryRow and Rows so scanTicket /
// scanMessage work with both.
type scanRow interface {
	Scan(dest ...any) error
}

func scanTicket(r scanRow) (*Ticket, error) {
	var t Ticket
	if err := r.Scan(
		&t.ID, &t.TenantID, &t.ConversationID, &t.State, &t.Priority,
		&t.RequiredSkills, &t.AssignedAgentID, &t.SLAFirstResponseDue,
		&t.SLAResolutionDue, &t.FirstResponseAt, &t.ResolvedAt,
		&t.ClosedAt, &t.CreatedAt, &t.UpdatedAt,
	); err != nil {
		return nil, err
	}
	return &t, nil
}

func scanMessage(r scanRow) (*Message, error) {
	var m Message
	if err := r.Scan(
		&m.ID, &m.TenantID, &m.TicketID, &m.Direction, &m.AgentID,
		&m.Body, &m.Attachments, &m.PlatformMessageID, &m.CreatedAt,
	); err != nil {
		return nil, err
	}
	return &m, nil
}
