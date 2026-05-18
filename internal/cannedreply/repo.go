// Package cannedreply owns the saved-response store: per-agent and
// per-tenant text snippets surfaced by the composer's "/" trigger.
// CRUD + a List that honours the visibility rules (agent sees their
// own + tenant-shared; admin sees everything).
package cannedreply

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrNotFound surfaces 404 to callers.
var ErrNotFound = errors.New("cannedreply: not found")

// ErrForbidden surfaces 403 (e.g. agent trying to mutate someone else's).
var ErrForbidden = errors.New("cannedreply: not your reply to edit")

// ErrInvalid surfaces 400 for malformed input.
var ErrInvalid = errors.New("cannedreply: invalid input")

// shortcutPattern enforces lower_snake_case: letters, digits,
// underscore, 1-32 chars. Rejected at the boundary so the composer's
// "/" autocomplete UX stays predictable.
var shortcutPattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,31}$`)

// Reply is the public model returned to callers.
type Reply struct {
	ID            uuid.UUID  `json:"id"`
	TenantID      uuid.UUID  `json:"tenant_id"`
	OwnerAgentID  *uuid.UUID `json:"owner_agent_id,omitempty"`
	Shortcut      string     `json:"shortcut"`
	Title         string     `json:"title"`
	Body          string     `json:"body"`
	Channel       *string    `json:"channel,omitempty"`
}

// Repo is a thin pgx wrapper.
type Repo struct{ Pool *pgxpool.Pool }

// NewRepo wires a repo.
func NewRepo(pool *pgxpool.Pool) *Repo { return &Repo{Pool: pool} }

// CreateParams is the create body.
type CreateParams struct {
	TenantID       uuid.UUID
	OwnerAgentID   *uuid.UUID // nil = tenant-shared
	Shortcut       string
	Title          string
	Body           string
	Channel        *string
}

// Create inserts a new reply. UNIQUE (tenant, owner, shortcut) is
// surfaced as ErrInvalid -- the caller (the agent UI) renders a
// useful 400 instead of a 500.
func (r *Repo) Create(ctx context.Context, p CreateParams) (*Reply, error) {
	if err := validateShortcut(p.Shortcut); err != nil {
		return nil, err
	}
	if strings.TrimSpace(p.Title) == "" || strings.TrimSpace(p.Body) == "" {
		return nil, fmt.Errorf("%w: title and body required", ErrInvalid)
	}
	if p.Channel != nil && !validChannel(*p.Channel) {
		return nil, fmt.Errorf("%w: unknown channel %q", ErrInvalid, *p.Channel)
	}

	var out Reply
	err := r.Pool.QueryRow(ctx, `
		INSERT INTO canned_replies
		  (tenant_id, owner_agent_id, shortcut, title, body, channel)
		VALUES ($1, $2, $3, $4, $5, $6)
		RETURNING id, tenant_id, owner_agent_id, shortcut, title, body, channel`,
		p.TenantID, p.OwnerAgentID, p.Shortcut, strings.TrimSpace(p.Title),
		p.Body, p.Channel,
	).Scan(&out.ID, &out.TenantID, &out.OwnerAgentID, &out.Shortcut,
		&out.Title, &out.Body, &out.Channel)
	if err != nil {
		if strings.Contains(err.Error(), "SQLSTATE 23505") {
			return nil, fmt.Errorf("%w: shortcut %q already exists for this owner", ErrInvalid, p.Shortcut)
		}
		return nil, err
	}
	return &out, nil
}

// ListVisible returns every reply the caller can see: their personal
// + tenant-shared, ordered shared-first then personal, alphabetised
// by shortcut so the picker is stable.
func (r *Repo) ListVisible(ctx context.Context, tenantID, agentID uuid.UUID) ([]Reply, error) {
	rows, err := r.Pool.Query(ctx, `
		SELECT id, tenant_id, owner_agent_id, shortcut, title, body, channel
		FROM canned_replies
		WHERE tenant_id = $1 AND (owner_agent_id IS NULL OR owner_agent_id = $2)
		ORDER BY owner_agent_id NULLS FIRST, shortcut ASC`,
		tenantID, agentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Reply{}
	for rows.Next() {
		var rep Reply
		if err := rows.Scan(&rep.ID, &rep.TenantID, &rep.OwnerAgentID,
			&rep.Shortcut, &rep.Title, &rep.Body, &rep.Channel); err != nil {
			return nil, err
		}
		out = append(out, rep)
	}
	return out, rows.Err()
}

// UpdateParams patches a reply. Fields are pointers so the caller
// can leave them unset.
type UpdateParams struct {
	TenantID    uuid.UUID
	ID          uuid.UUID
	ActingAgent uuid.UUID
	ActingAdmin bool
	Title       *string
	Body        *string
	Shortcut    *string
	Channel     *string // empty string -> NULL (clear channel scope)
}

// Update applies the patch. Agents can only edit their own; admins
// can edit anything.
func (r *Repo) Update(ctx context.Context, p UpdateParams) (*Reply, error) {
	if p.Shortcut != nil {
		if err := validateShortcut(*p.Shortcut); err != nil {
			return nil, err
		}
	}
	cur, err := r.get(ctx, p.TenantID, p.ID)
	if err != nil {
		return nil, err
	}
	if !p.ActingAdmin {
		if cur.OwnerAgentID == nil || *cur.OwnerAgentID != p.ActingAgent {
			return nil, ErrForbidden
		}
	}

	// nilable channel: pointer-to-empty-string means "clear".
	var ch *string
	if p.Channel != nil {
		if *p.Channel == "" {
			ch = nil
		} else if !validChannel(*p.Channel) {
			return nil, fmt.Errorf("%w: unknown channel %q", ErrInvalid, *p.Channel)
		} else {
			ch = p.Channel
		}
	} else {
		ch = cur.Channel
	}

	var out Reply
	err = r.Pool.QueryRow(ctx, `
		UPDATE canned_replies SET
		  title    = COALESCE($3, title),
		  body     = COALESCE($4, body),
		  shortcut = COALESCE($5, shortcut),
		  channel  = $6
		WHERE tenant_id = $1 AND id = $2
		RETURNING id, tenant_id, owner_agent_id, shortcut, title, body, channel`,
		p.TenantID, p.ID, p.Title, p.Body, p.Shortcut, ch,
	).Scan(&out.ID, &out.TenantID, &out.OwnerAgentID, &out.Shortcut,
		&out.Title, &out.Body, &out.Channel)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil && strings.Contains(err.Error(), "SQLSTATE 23505") {
		return nil, fmt.Errorf("%w: shortcut already exists for this owner", ErrInvalid)
	}
	return &out, err
}

// Delete removes one row. Same visibility rules as Update.
func (r *Repo) Delete(ctx context.Context, tenantID, id, actingAgent uuid.UUID, actingAdmin bool) error {
	cur, err := r.get(ctx, tenantID, id)
	if err != nil {
		return err
	}
	if !actingAdmin {
		if cur.OwnerAgentID == nil || *cur.OwnerAgentID != actingAgent {
			return ErrForbidden
		}
	}
	_, err = r.Pool.Exec(ctx,
		`DELETE FROM canned_replies WHERE tenant_id = $1 AND id = $2`,
		tenantID, id)
	return err
}

func (r *Repo) get(ctx context.Context, tenantID, id uuid.UUID) (*Reply, error) {
	var rep Reply
	err := r.Pool.QueryRow(ctx, `
		SELECT id, tenant_id, owner_agent_id, shortcut, title, body, channel
		FROM canned_replies
		WHERE tenant_id = $1 AND id = $2`,
		tenantID, id).
		Scan(&rep.ID, &rep.TenantID, &rep.OwnerAgentID, &rep.Shortcut,
			&rep.Title, &rep.Body, &rep.Channel)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return &rep, err
}

func validateShortcut(s string) error {
	if !shortcutPattern.MatchString(s) {
		return fmt.Errorf(
			"%w: shortcut must be lower_snake_case, 1-32 chars, starting with a letter",
			ErrInvalid)
	}
	return nil
}

func validChannel(s string) bool {
	switch s {
	case "fb", "x", "wa", "ig", "widget", "voice", "email":
		return true
	}
	return false
}
