package ticket

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
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
type Repo struct {
	pool *pgxpool.Pool
	// OnStateChange is fired after a successful ChangeState commit.
	// nil is a no-op so unit tests don't need to mock JetStream. The
	// gateway wires it to a JetStream publisher that emits
	// `ticket.state_change` so subscribers (CSAT dispatcher, webhook
	// fan-out, audit) react.
	OnStateChange StateChangeListener
	// OnBulkStateChange is fired once after a bulk batch lands so
	// the realtime hub gets one envelope per batch instead of N.
	// Receives the set of ticket ids that actually transitioned.
	// nil is a no-op (same convention as OnStateChange).
	OnBulkStateChange BulkStateChangeListener
	// OnOutbound is fired after AppendMessage commits an outbound
	// (direction=out) message. nil is a no-op. The gateway wires it
	// to a JetStream publisher on outbound.<channel>.text so per-
	// channel sender workers (FB Sender, X v2, WA Cloud, ...) ship.
	OnOutbound OutboundListener
}

// StateChangeListener is the post-commit hook signature.
type StateChangeListener func(ctx context.Context, tenantID, ticketID uuid.UUID, from, to State)

// BulkStateChangeListener fires once per successful bulk batch. The
// `to` argument is the target state every id in `ids` landed on
// (BulkState applies one transition across the whole batch).
type BulkStateChangeListener func(ctx context.Context, tenantID uuid.UUID, ids []uuid.UUID, to State)

// OutboundListener fires after a `direction=out` message is persisted.
// channel is the conversation's channel ('fb', 'x', 'wa', ...).
type OutboundListener func(ctx context.Context, tenantID, ticketID, customerID uuid.UUID, channel, body string)

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

// ListParams selects which tickets the inbox shows. State filter is
// optional; an empty States slice returns "open work" (everything that
// isn't resolved/closed). AssignedAgentID is optional too -- nil
// returns the whole tenant view, useful for supervisors.
type ListParams struct {
	TenantID        uuid.UUID
	States          []State
	AssignedAgentID *uuid.UUID
	// Channels filters to specific conversation channels (fb/ig/wa/
	// widget/voice/email/x). Empty = all.
	Channels []string
	// AssignedFilter overrides AssignedAgentID's semantics:
	//   "" / "all"    -> ignore AssignedAgentID
	//   "mine"        -> WHERE assigned_agent_id = AssignedAgentID
	//   "unassigned"  -> WHERE assigned_agent_id IS NULL
	// Set "mine" by default for agent-role inbox; supervisors usually
	// pass "all" so the whole tenant view stays visible.
	AssignedFilter string
	// Query case-insensitive substring match against messages.body for
	// any message on the ticket. Empty = no text filter.
	Query string
	// TagSlugs restricts to tickets that have at least one of these
	// tag slugs attached. Empty = no tag filter. Matches any-of (OR
	// semantics) rather than all-of -- agents pick filters to widen
	// what they see, not narrow it.
	TagSlugs []string
	// Limit caps the page; 0 falls back to 50, max 200.
	Limit int
}

// List returns the inbox view with conversation + customer joined so
// each row has channel + customer_name + last message preview without
// a follow-up round-trip. Sorted by priority then created_at so the
// oldest urgent ticket lands at the top.
func (r *Repo) List(ctx context.Context, p ListParams) ([]ListItem, error) {
	if p.Limit <= 0 || p.Limit > 200 {
		p.Limit = 50
	}
	states := p.States
	if len(states) == 0 {
		states = []State{StateNew, StateOpen, StatePending, StateOnHold, StateReopened}
	}
	stateStrs := make([]string, len(states))
	for i, s := range states {
		stateStrs[i] = string(s)
	}
	// pgx encodes a nil slice as SQL NULL, which would break the
	// cardinality() = 0 guard below; normalize to an explicit empty
	// slice so the SQL sees a real (zero-length) array.
	channels := p.Channels
	if channels == nil {
		channels = []string{}
	}

	// Three assignment shapes via a small string sentinel so the SQL
	// stays a single prepared statement rather than CASE-ing in code.
	assignedMine, assignedUnassigned := false, false
	switch p.AssignedFilter {
	case "mine":
		assignedMine = true
	case "unassigned":
		assignedUnassigned = true
	}

	// CITEXT-style case-insensitive LIKE on messages.body. EXISTS
	// short-circuits per ticket; the partial index on
	// messages(ticket_id) keeps this cheap.
	q := "%" + strings.ToLower(strings.TrimSpace(p.Query)) + "%"
	hasQuery := strings.TrimSpace(p.Query) != ""

	tagSlugs := p.TagSlugs
	if tagSlugs == nil {
		tagSlugs = []string{}
	}

	// The list query is structured as two stages so the per-row
	// joins (last message, message count, tag rollup) execute only
	// for the LIMIT'd visible page, not for every matching ticket
	// in the tenant:
	//
	//   visible      filtered + sorted + LIMIT'd ticket rows
	//   tag_rollup   one grouped pass over ticket_tags scoped to
	//                visible (jsonb_agg ordered by slug)
	//   outer        re-applies the same ORDER BY for stable output
	//                and adds the cheap per-row lookups
	//
	// EXPLAIN on a 5k-ticket tenant: this drops the LATERAL loops
	// from 5005 to 50 and execution from ~60ms to ~6ms hot-cache.
	// MATERIALIZED is required so the channel-filter and tag-slug
	// EXISTS clauses don't get inlined into the LATERAL subqueries
	// (which would defeat the prune).
	rows, err := r.pool.Query(ctx, `
		WITH visible AS MATERIALIZED (
		  SELECT t.id, t.tenant_id, t.conversation_id, t.state, t.priority,
		         t.required_skills, t.assigned_agent_id,
		         t.sla_first_response_due, t.sla_resolution_due,
		         t.first_response_at, t.resolved_at, t.closed_at,
		         t.created_at, t.updated_at,
		         c.channel, c.customer_id
		  FROM tickets t
		  JOIN conversations c ON c.id = t.conversation_id
		  WHERE t.tenant_id = $1
		    AND t.state::text = ANY($2)
		    AND (cardinality($3::text[]) = 0 OR c.channel = ANY($3))
		    AND (NOT $5::bool OR t.assigned_agent_id = $4)
		    AND (NOT $6::bool OR t.assigned_agent_id IS NULL)
		    AND (NOT $7::bool OR EXISTS (
		        SELECT 1 FROM messages
		        WHERE tenant_id = t.tenant_id AND ticket_id = t.id
		          AND lower(body) LIKE $8))
		    AND (cardinality($10::text[]) = 0 OR EXISTS (
		        SELECT 1 FROM ticket_tags tt2
		        JOIN tags tg2 ON tg2.id = tt2.tag_id
		        WHERE tt2.tenant_id = t.tenant_id
		          AND tt2.ticket_id = t.id
		          AND tg2.slug = ANY($10)))
		  ORDER BY t.priority ASC, t.created_at ASC
		  LIMIT $9
		),
		tag_rollup AS (
		  SELECT tt.ticket_id,
		         jsonb_agg(jsonb_build_object(
		           'id',    tg.id,
		           'slug',  tg.slug,
		           'name',  tg.name,
		           'color', tg.color
		         ) ORDER BY tg.slug) AS tags
		  FROM ticket_tags tt
		  JOIN tags tg ON tg.id = tt.tag_id
		  WHERE tt.tenant_id = $1
		    AND tt.ticket_id IN (SELECT id FROM visible)
		  GROUP BY tt.ticket_id
		)
		SELECT
		  v.id, v.tenant_id, v.conversation_id, v.state, v.priority, v.required_skills,
		  v.assigned_agent_id, v.sla_first_response_due, v.sla_resolution_due,
		  v.first_response_at, v.resolved_at, v.closed_at, v.created_at, v.updated_at,
		  v.channel,
		  COALESCE(cu.display_name, ''),
		  COALESCE(lm.body, ''),
		  COALESCE(mc.cnt, 0),
		  COALESCE(tr.tags, '[]'::jsonb)
		FROM visible v
		JOIN customers cu ON cu.id = v.customer_id
		LEFT JOIN LATERAL (
		  SELECT body FROM messages
		  WHERE tenant_id = v.tenant_id AND ticket_id = v.id
		  ORDER BY created_at DESC LIMIT 1
		) lm ON TRUE
		LEFT JOIN LATERAL (
		  SELECT count(*) AS cnt FROM messages
		  WHERE tenant_id = v.tenant_id AND ticket_id = v.id
		) mc ON TRUE
		LEFT JOIN tag_rollup tr ON tr.ticket_id = v.id
		ORDER BY v.priority ASC, v.created_at ASC`,
		p.TenantID, stateStrs, channels,
		p.AssignedAgentID, assignedMine, assignedUnassigned,
		hasQuery, q,
		p.Limit, tagSlugs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]ListItem, 0, p.Limit)
	for rows.Next() {
		var (
			it       ListItem
			channel  string
			body     string
			count    int
			tagsJSON []byte
		)
		if err := rows.Scan(
			&it.ID, &it.TenantID, &it.ConversationID, &it.State, &it.Priority,
			&it.RequiredSkills, &it.AssignedAgentID, &it.SLAFirstResponseDue,
			&it.SLAResolutionDue, &it.FirstResponseAt, &it.ResolvedAt,
			&it.ClosedAt, &it.CreatedAt, &it.UpdatedAt,
			&channel, &it.CustomerName, &body, &count, &tagsJSON,
		); err != nil {
			return nil, err
		}
		it.Channel = channel
		it.LastMessageBody = body
		it.MessageCount = count
		it.Tags = []TagBrief{}
		if len(tagsJSON) > 0 {
			if err := json.Unmarshal(tagsJSON, &it.Tags); err != nil {
				return nil, fmt.Errorf("ticket: decode tags: %w", err)
			}
		}
		out = append(out, it)
	}
	return out, rows.Err()
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
	// Post-commit notification. We capture `current` (pre-commit) and
	// `to` (the actual landed state). The hook runs synchronously --
	// it's the caller's job to dispatch async work; this guarantees
	// "if we returned the new ticket, the listener saw it".
	if r.OnStateChange != nil {
		r.OnStateChange(ctx, tenantID, id, current, to)
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
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return nil, fmt.Errorf("ticket: begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// pgx's QueryExecModeExec (which we use for pgbouncer compat)
	// can't encode []uuid.UUID natively because there's no OID from
	// a prepared statement. Convert to []string and let PG cast.
	att := make([]string, len(p.Attachments))
	for i, u := range p.Attachments {
		att[i] = u.String()
	}

	row := tx.QueryRow(ctx, `
		INSERT INTO messages
		  (tenant_id, ticket_id, direction, agent_id, body, attachments, platform_message_id)
		VALUES ($1,$2,$3,$4,$5,$6::uuid[],$7)
		RETURNING id, tenant_id, ticket_id, direction, agent_id, body,
		          attachments, platform_message_id, created_at`,
		p.TenantID, p.TicketID, p.Direction, p.AgentID, p.Body,
		att, p.PlatformMessageID,
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

	// Post-commit fan-out for outbound messages. We look up the
	// channel + customer via the conversation join after the commit
	// so the listener has everything it needs to address the right
	// per-channel sender (outbound.<channel>.text).
	if p.Direction == DirectionOut && r.OnOutbound != nil {
		var channel string
		var customerID uuid.UUID
		if err := r.pool.QueryRow(ctx, `
			SELECT c.channel, c.customer_id
			FROM tickets t
			JOIN conversations c ON c.id = t.conversation_id
			WHERE t.id = $1`, p.TicketID).Scan(&channel, &customerID); err == nil {
			r.OnOutbound(ctx, p.TenantID, p.TicketID, customerID, channel, p.Body)
		}
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
