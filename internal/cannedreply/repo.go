// Package cannedreply owns the saved-response store: per-agent and
// per-tenant text snippets surfaced by the composer's "/" trigger.
// CRUD + a List that honours the visibility rules (agent sees their
// own + tenant-shared; admin sees everything).
package cannedreply

import (
	"context"
	"encoding/json"
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
	// Actions are macro side-effects fired after the reply text is
	// sent. Empty array = plain canned reply (the historic behaviour).
	Actions []Action `json:"actions"`
}

// Action is a single macro step. The JSON shape is open-ended -- new
// `type` values can be added without a migration -- but only the
// types the composer knows how to execute are surfaced in the UI.
//
//	{"type":"set_state","to":"resolved"}
//	{"type":"add_tag","tag_slug":"refund"}
//	{"type":"assign","to":"me"|"unassign"|"<agent-uuid>"}
type Action struct {
	Type    string `json:"type"`
	To      string `json:"to,omitempty"`
	TagSlug string `json:"tag_slug,omitempty"`
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
	Actions        []Action
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
	if err := validateActions(p.Actions); err != nil {
		return nil, err
	}
	actionsJSON, err := marshalActions(p.Actions)
	if err != nil {
		return nil, err
	}

	var out Reply
	var actionsRaw []byte
	err = r.Pool.QueryRow(ctx, `
		INSERT INTO canned_replies
		  (tenant_id, owner_agent_id, shortcut, title, body, channel, actions)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		RETURNING id, tenant_id, owner_agent_id, shortcut, title, body, channel, actions`,
		p.TenantID, p.OwnerAgentID, p.Shortcut, strings.TrimSpace(p.Title),
		p.Body, p.Channel, actionsJSON,
	).Scan(&out.ID, &out.TenantID, &out.OwnerAgentID, &out.Shortcut,
		&out.Title, &out.Body, &out.Channel, &actionsRaw)
	if err != nil {
		if strings.Contains(err.Error(), "SQLSTATE 23505") {
			return nil, fmt.Errorf("%w: shortcut %q already exists for this owner", ErrInvalid, p.Shortcut)
		}
		return nil, err
	}
	out.Actions = decodeActions(actionsRaw)
	return &out, nil
}

// ListVisible returns every reply the caller can see: their personal
// + tenant-shared, ordered shared-first then personal, alphabetised
// by shortcut so the picker is stable.
func (r *Repo) ListVisible(ctx context.Context, tenantID, agentID uuid.UUID) ([]Reply, error) {
	rows, err := r.Pool.Query(ctx, `
		SELECT id, tenant_id, owner_agent_id, shortcut, title, body, channel, actions
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
		var (
			rep        Reply
			actionsRaw []byte
		)
		if err := rows.Scan(&rep.ID, &rep.TenantID, &rep.OwnerAgentID,
			&rep.Shortcut, &rep.Title, &rep.Body, &rep.Channel, &actionsRaw); err != nil {
			return nil, err
		}
		rep.Actions = decodeActions(actionsRaw)
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
	Channel     *string  // empty string -> NULL (clear channel scope)
	Actions     *[]Action // nil = leave untouched; empty slice = clear
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

	// Actions: nil pointer leaves untouched (re-encode current rows),
	// non-nil replaces with the supplied slice (empty = clear).
	var actionsToWrite []Action
	if p.Actions != nil {
		if err := validateActions(*p.Actions); err != nil {
			return nil, err
		}
		actionsToWrite = *p.Actions
	} else {
		actionsToWrite = cur.Actions
	}
	actionsJSON, err := marshalActions(actionsToWrite)
	if err != nil {
		return nil, err
	}

	var out Reply
	var actionsRaw []byte
	err = r.Pool.QueryRow(ctx, `
		UPDATE canned_replies SET
		  title    = COALESCE($3, title),
		  body     = COALESCE($4, body),
		  shortcut = COALESCE($5, shortcut),
		  channel  = $6,
		  actions  = $7
		WHERE tenant_id = $1 AND id = $2
		RETURNING id, tenant_id, owner_agent_id, shortcut, title, body, channel, actions`,
		p.TenantID, p.ID, p.Title, p.Body, p.Shortcut, ch, actionsJSON,
	).Scan(&out.ID, &out.TenantID, &out.OwnerAgentID, &out.Shortcut,
		&out.Title, &out.Body, &out.Channel, &actionsRaw)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil && strings.Contains(err.Error(), "SQLSTATE 23505") {
		return nil, fmt.Errorf("%w: shortcut already exists for this owner", ErrInvalid)
	}
	out.Actions = decodeActions(actionsRaw)
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
	var (
		rep        Reply
		actionsRaw []byte
	)
	err := r.Pool.QueryRow(ctx, `
		SELECT id, tenant_id, owner_agent_id, shortcut, title, body, channel, actions
		FROM canned_replies
		WHERE tenant_id = $1 AND id = $2`,
		tenantID, id).
		Scan(&rep.ID, &rep.TenantID, &rep.OwnerAgentID, &rep.Shortcut,
			&rep.Title, &rep.Body, &rep.Channel, &actionsRaw)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	rep.Actions = decodeActions(actionsRaw)
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

// validateActions checks the macro action list at the boundary so
// the DB only ever holds well-formed payloads. Unknown action types
// are rejected -- forwards-compatibility lives in adding new types,
// not in storing values nobody will execute.
func validateActions(actions []Action) error {
	for i, a := range actions {
		switch a.Type {
		case "set_state":
			switch a.To {
			case "open", "pending", "on_hold", "resolved", "closed", "reopened":
			default:
				return fmt.Errorf("%w: action[%d] set_state.to=%q invalid", ErrInvalid, i, a.To)
			}
		case "add_tag":
			if !slugPatternBoundary(a.TagSlug) {
				return fmt.Errorf("%w: action[%d] add_tag.tag_slug invalid", ErrInvalid, i)
			}
		case "assign":
			if a.To == "me" || a.To == "unassign" {
				continue
			}
			if _, err := uuid.Parse(a.To); err != nil {
				return fmt.Errorf("%w: action[%d] assign.to must be me/unassign/uuid", ErrInvalid, i)
			}
		default:
			return fmt.Errorf("%w: action[%d] unknown type %q", ErrInvalid, i, a.Type)
		}
	}
	return nil
}

// slugPatternBoundary mirrors the tag package's slug rule without
// taking a dependency on it; same lower_snake_case 1-32 chars.
var slugRE = regexp.MustCompile(`^[a-z][a-z0-9_]{0,31}$`)

func slugPatternBoundary(s string) bool { return slugRE.MatchString(s) }

// marshalActions normalises a nil slice to '[]' so the DB never
// holds NULL even though the column is NOT NULL DEFAULT '[]'.
func marshalActions(actions []Action) ([]byte, error) {
	if actions == nil {
		actions = []Action{}
	}
	return json.Marshal(actions)
}

// decodeActions is the inverse; empty/invalid JSON falls back to an
// empty slice so the caller never has to nil-check.
func decodeActions(raw []byte) []Action {
	if len(raw) == 0 {
		return []Action{}
	}
	var out []Action
	if err := json.Unmarshal(raw, &out); err != nil {
		return []Action{}
	}
	if out == nil {
		out = []Action{}
	}
	return out
}

func validChannel(s string) bool {
	switch s {
	case "fb", "x", "wa", "ig", "widget", "voice", "email":
		return true
	}
	return false
}
