package onboarding

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// WidgetRepo manages widget_sites rows -- the per-tenant registry of
// origins allowed to open a /ws/widget connection. A brand admin lists
// + registers their company sites here; the generated embed_key is
// what they paste into the <script data-embed-key="..."> snippet.
type WidgetRepo struct{ Pool *pgxpool.Pool }

// NewWidgetRepo constructs a repo bound to a pool.
func NewWidgetRepo(pool *pgxpool.Pool) *WidgetRepo { return &WidgetRepo{Pool: pool} }

// WidgetSite is the public-facing shape returned by the API. Mirrors
// the widget_sites table columns the admin UI renders.
type WidgetSite struct {
	ID             uuid.UUID `json:"id"`
	Origin         string    `json:"origin"`
	DisplayName    string    `json:"display_name"`
	WelcomeMessage string    `json:"welcome_message"`
	EmbedKey       string    `json:"embed_key"`
	Active         bool      `json:"active"`
}

// ErrOriginExists is returned by Register when the (tenant, origin)
// pair is already on file -- callers surface a 409.
var ErrOriginExists = errors.New("onboarding: origin already registered for this tenant")

// ErrInvalidOrigin is returned when the supplied origin doesn't parse
// as an http(s) URL with a host. Caught at the boundary so the JS
// snippet can't be set up against a value the WS server will then
// reject at connect time.
var ErrInvalidOrigin = errors.New("onboarding: origin must be a valid http(s):// URL with a host and no path/query")

// ListWidgets returns every site for the tenant, newest first.
func (r *WidgetRepo) ListWidgets(ctx context.Context, tenantID uuid.UUID) ([]WidgetSite, error) {
	rows, err := r.Pool.Query(ctx, `
		SELECT id, origin, display_name, welcome_message, embed_key, active
		FROM widget_sites
		WHERE tenant_id = $1
		ORDER BY created_at DESC`, tenantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []WidgetSite{}
	for rows.Next() {
		var w WidgetSite
		if err := rows.Scan(&w.ID, &w.Origin, &w.DisplayName, &w.WelcomeMessage, &w.EmbedKey, &w.Active); err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	return out, rows.Err()
}

// RegisterParams is the create body.
type RegisterParams struct {
	TenantID       uuid.UUID
	Origin         string
	DisplayName    string
	WelcomeMessage string // optional; defaults if blank
}

// Register inserts a new widget site with a freshly-generated embed_key.
// Returns ErrOriginExists on (tenant_id, origin) UNIQUE collision so
// the caller can surface a 409.
func (r *WidgetRepo) Register(ctx context.Context, p RegisterParams) (*WidgetSite, error) {
	origin, err := normalizeOrigin(p.Origin)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(p.DisplayName) == "" {
		return nil, fmt.Errorf("onboarding: display_name required")
	}
	welcome := strings.TrimSpace(p.WelcomeMessage)
	if welcome == "" {
		welcome = "Hi! How can we help?"
	}

	key, err := newEmbedKey()
	if err != nil {
		return nil, fmt.Errorf("onboarding: gen embed key: %w", err)
	}

	var w WidgetSite
	err = r.Pool.QueryRow(ctx, `
		INSERT INTO widget_sites
		  (tenant_id, origin, display_name, welcome_message, embed_key, active)
		VALUES ($1, $2, $3, $4, $5, TRUE)
		RETURNING id, origin, display_name, welcome_message, embed_key, active`,
		p.TenantID, origin, strings.TrimSpace(p.DisplayName), welcome, key,
	).Scan(&w.ID, &w.Origin, &w.DisplayName, &w.WelcomeMessage, &w.EmbedKey, &w.Active)
	if err != nil {
		// UNIQUE (tenant_id, origin) violation -> typed error.
		if isUniqueViolation(err) {
			return nil, ErrOriginExists
		}
		return nil, err
	}
	return &w, nil
}

// UpdateParams is the patch body. Fields are pointers so unset fields
// stay untouched (you can flip active without rewriting the welcome
// text, etc.).
type UpdateParams struct {
	TenantID       uuid.UUID
	ID             uuid.UUID
	DisplayName    *string
	WelcomeMessage *string
	Active         *bool
}

// Update applies a partial patch. Returns the updated row.
func (r *WidgetRepo) Update(ctx context.Context, p UpdateParams) (*WidgetSite, error) {
	// COALESCE($n, column) preserves the existing value when the
	// argument is NULL. Welcome message is allowed to be set to a
	// blank-trimmed empty (it'll default-back via DB DEFAULT on
	// rewrite); display_name must stay non-blank if provided.
	if p.DisplayName != nil && strings.TrimSpace(*p.DisplayName) == "" {
		return nil, fmt.Errorf("onboarding: display_name cannot be blank")
	}

	var w WidgetSite
	err := r.Pool.QueryRow(ctx, `
		UPDATE widget_sites SET
		  display_name    = COALESCE($3, display_name),
		  welcome_message = COALESCE($4, welcome_message),
		  active          = COALESCE($5, active)
		WHERE tenant_id = $1 AND id = $2
		RETURNING id, origin, display_name, welcome_message, embed_key, active`,
		p.TenantID, p.ID, p.DisplayName, p.WelcomeMessage, p.Active,
	).Scan(&w.ID, &w.Origin, &w.DisplayName, &w.WelcomeMessage, &w.EmbedKey, &w.Active)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("onboarding: widget not found")
	}
	return &w, err
}

// Delete hard-deletes the row. Soft-delete via active=false is also a
// fine choice; we delete here so an admin who registered the wrong
// origin can re-register the exact same origin without a unique-key
// fight.
func (r *WidgetRepo) Delete(ctx context.Context, tenantID, id uuid.UUID) error {
	ct, err := r.Pool.Exec(ctx, `
		DELETE FROM widget_sites WHERE tenant_id = $1 AND id = $2`,
		tenantID, id)
	if err != nil {
		return err
	}
	if ct.RowsAffected() == 0 {
		return fmt.Errorf("onboarding: widget not found")
	}
	return nil
}

// normalizeOrigin validates the supplied origin matches what the WS
// server will see in the Origin header: scheme://host[:port], no path,
// no query, no trailing slash. Anything else is rejected.
func normalizeOrigin(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", ErrInvalidOrigin
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", ErrInvalidOrigin
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", ErrInvalidOrigin
	}
	if u.Host == "" {
		return "", ErrInvalidOrigin
	}
	if u.Path != "" && u.Path != "/" {
		return "", ErrInvalidOrigin
	}
	if u.RawQuery != "" || u.Fragment != "" {
		return "", ErrInvalidOrigin
	}
	return u.Scheme + "://" + u.Host, nil
}

// newEmbedKey returns 16 random bytes hex-encoded (32 chars).
// Not a secret -- the Origin check is the load-bearing defence -- but
// long enough that a careless rotation still produces a distinct value.
func newEmbedKey() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

// (isUniqueViolation lives in tenants.go; reused here.)
