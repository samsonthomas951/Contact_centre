// Package tag owns the per-tenant ticket-label catalogue and the
// many-to-many between tags and tickets. The repo speaks pgx; HTTP
// boundary lives in http.go.
package tag

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

var (
	ErrNotFound  = errors.New("tag: not found")
	ErrInvalid   = errors.New("tag: invalid input")
	ErrForbidden = errors.New("tag: not permitted")
)

// slugPattern enforces lower_snake_case 1-32 chars starting with a
// letter. Mirrors canned_replies.shortcut so the two concepts read
// the same way in the UI.
var slugPattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,31}$`)

// colorPattern is a 6-char RGB hex without the leading '#'. The
// app validates so the DB can keep `color` a plain TEXT.
var colorPattern = regexp.MustCompile(`^[0-9a-fA-F]{6}$`)

// Tag is the public model.
type Tag struct {
	ID       uuid.UUID `json:"id"`
	TenantID uuid.UUID `json:"tenant_id"`
	Slug     string    `json:"slug"`
	Name     string    `json:"name"`
	Color    *string   `json:"color,omitempty"`
}

type Repo struct{ Pool *pgxpool.Pool }

func NewRepo(pool *pgxpool.Pool) *Repo { return &Repo{Pool: pool} }

// CreateParams is the input for Create.
type CreateParams struct {
	TenantID uuid.UUID
	Slug     string
	Name     string
	Color    *string
}

// Create inserts a tag. Slug uniqueness is per-tenant; collisions
// surface as ErrInvalid so the agent UI renders a 400.
func (r *Repo) Create(ctx context.Context, p CreateParams) (*Tag, error) {
	if err := validateSlug(p.Slug); err != nil {
		return nil, err
	}
	if strings.TrimSpace(p.Name) == "" {
		return nil, fmt.Errorf("%w: name required", ErrInvalid)
	}
	if p.Color != nil {
		if *p.Color == "" {
			p.Color = nil
		} else if !colorPattern.MatchString(*p.Color) {
			return nil, fmt.Errorf("%w: color must be 6-char RGB hex", ErrInvalid)
		}
	}
	var out Tag
	err := r.Pool.QueryRow(ctx, `
		INSERT INTO tags (tenant_id, slug, name, color)
		VALUES ($1, $2, $3, $4)
		RETURNING id, tenant_id, slug, name, color`,
		p.TenantID, p.Slug, strings.TrimSpace(p.Name), p.Color,
	).Scan(&out.ID, &out.TenantID, &out.Slug, &out.Name, &out.Color)
	if err != nil {
		if strings.Contains(err.Error(), "SQLSTATE 23505") {
			return nil, fmt.Errorf("%w: slug %q already exists", ErrInvalid, p.Slug)
		}
		return nil, err
	}
	return &out, nil
}

// List returns every tag for the tenant in slug order.
func (r *Repo) List(ctx context.Context, tenantID uuid.UUID) ([]Tag, error) {
	rows, err := r.Pool.Query(ctx, `
		SELECT id, tenant_id, slug, name, color
		FROM tags
		WHERE tenant_id = $1
		ORDER BY slug ASC`, tenantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Tag{}
	for rows.Next() {
		var t Tag
		if err := rows.Scan(&t.ID, &t.TenantID, &t.Slug, &t.Name, &t.Color); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// Delete drops a tag and (via ON DELETE CASCADE) all its links.
func (r *Repo) Delete(ctx context.Context, tenantID, id uuid.UUID) error {
	ct, err := r.Pool.Exec(ctx,
		`DELETE FROM tags WHERE tenant_id = $1 AND id = $2`, tenantID, id)
	if err != nil {
		return err
	}
	if ct.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// Attach adds (tenant, ticket, tag); idempotent via ON CONFLICT.
// Returns ErrNotFound if either ticket or tag is missing for this
// tenant (joins against tickets to enforce the tenant scope).
func (r *Repo) Attach(ctx context.Context, tenantID, ticketID, tagID uuid.UUID) error {
	// Verify ticket belongs to tenant; cheap guard so a foreign
	// ticket_id can't be tagged through this endpoint.
	var ok bool
	if err := r.Pool.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM tickets WHERE tenant_id = $1 AND id = $2)`,
		tenantID, ticketID).Scan(&ok); err != nil {
		return err
	}
	if !ok {
		return ErrNotFound
	}
	if err := r.Pool.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM tags WHERE tenant_id = $1 AND id = $2)`,
		tenantID, tagID).Scan(&ok); err != nil {
		return err
	}
	if !ok {
		return ErrNotFound
	}
	_, err := r.Pool.Exec(ctx, `
		INSERT INTO ticket_tags (tenant_id, ticket_id, tag_id)
		VALUES ($1, $2, $3)
		ON CONFLICT DO NOTHING`,
		tenantID, ticketID, tagID)
	return err
}

// Detach removes the link. Idempotent; missing link is not an error.
func (r *Repo) Detach(ctx context.Context, tenantID, ticketID, tagID uuid.UUID) error {
	_, err := r.Pool.Exec(ctx, `
		DELETE FROM ticket_tags
		WHERE tenant_id = $1 AND ticket_id = $2 AND tag_id = $3`,
		tenantID, ticketID, tagID)
	return err
}

// ForTicket returns the tags attached to a single ticket. Ordered
// by slug for stable rendering.
func (r *Repo) ForTicket(ctx context.Context, tenantID, ticketID uuid.UUID) ([]Tag, error) {
	rows, err := r.Pool.Query(ctx, `
		SELECT t.id, t.tenant_id, t.slug, t.name, t.color
		FROM ticket_tags tt
		JOIN tags t ON t.id = tt.tag_id
		WHERE tt.tenant_id = $1 AND tt.ticket_id = $2
		ORDER BY t.slug ASC`,
		tenantID, ticketID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Tag{}
	for rows.Next() {
		var t Tag
		if err := rows.Scan(&t.ID, &t.TenantID, &t.Slug, &t.Name, &t.Color); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// ForTickets returns tags keyed by ticket_id for a batch lookup;
// avoids an N+1 when the inbox renders many rows.
func (r *Repo) ForTickets(ctx context.Context, tenantID uuid.UUID, ticketIDs []uuid.UUID) (map[uuid.UUID][]Tag, error) {
	out := map[uuid.UUID][]Tag{}
	if len(ticketIDs) == 0 {
		return out, nil
	}
	ids := make([]string, len(ticketIDs))
	for i, u := range ticketIDs {
		ids[i] = u.String()
	}
	rows, err := r.Pool.Query(ctx, `
		SELECT tt.ticket_id, t.id, t.tenant_id, t.slug, t.name, t.color
		FROM ticket_tags tt
		JOIN tags t ON t.id = tt.tag_id
		WHERE tt.tenant_id = $1 AND tt.ticket_id = ANY($2::uuid[])
		ORDER BY tt.ticket_id, t.slug ASC`,
		tenantID, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var (
			tid uuid.UUID
			t   Tag
		)
		if err := rows.Scan(&tid, &t.ID, &t.TenantID, &t.Slug, &t.Name, &t.Color); err != nil {
			return nil, err
		}
		out[tid] = append(out[tid], t)
	}
	return out, rows.Err()
}

// SlugsToIDs resolves a list of slugs to tag IDs for the tenant.
// Used by the inbox tag filter so the URL stays human-readable
// (`?tag=billing&tag=urgent`) while the SQL joins on UUIDs.
func (r *Repo) SlugsToIDs(ctx context.Context, tenantID uuid.UUID, slugs []string) ([]uuid.UUID, error) {
	if len(slugs) == 0 {
		return nil, nil
	}
	rows, err := r.Pool.Query(ctx, `
		SELECT id FROM tags
		WHERE tenant_id = $1 AND slug = ANY($2::text[])`,
		tenantID, slugs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []uuid.UUID{}
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

func validateSlug(s string) error {
	if !slugPattern.MatchString(s) {
		return fmt.Errorf(
			"%w: slug must be lower_snake_case, 1-32 chars, starting with a letter",
			ErrInvalid)
	}
	return nil
}

// scan helper for places that take a Row instead of Rows.
func scanTag(row pgx.Row) (*Tag, error) {
	var t Tag
	if err := row.Scan(&t.ID, &t.TenantID, &t.Slug, &t.Name, &t.Color); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &t, nil
}

var _ = scanTag // reserved for future Get() calls
