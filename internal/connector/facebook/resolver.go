package facebook

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// PGTenantResolver looks page_id up in the fb_pages table populated by
// the OAuth connect flow. Used by WebhookHandler.Resolver.
type PGTenantResolver struct{ Pool *pgxpool.Pool }

// NewPGTenantResolver binds a resolver to a pool.
func NewPGTenantResolver(pool *pgxpool.Pool) *PGTenantResolver {
	return &PGTenantResolver{Pool: pool}
}

// ResolveTenant returns the tenant_id (as a string for the Normalize
// fan-out) for a given Page id, or ("", false) when the Page hasn't
// been connected yet.
func (r *PGTenantResolver) ResolveTenant(pageID string) (string, bool) {
	var tenant string
	ctx := context.Background() // resolver has no per-request ctx; keep the lookup quick
	err := r.Pool.QueryRow(ctx,
		`SELECT tenant_id::text FROM fb_pages WHERE page_id = $1`, pageID).Scan(&tenant)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false
	}
	if err != nil {
		// A DB blip drops the inbound; Meta retries 5xx, but our
		// webhook handler returns 200 on success and 401 on bad
		// signature -- nothing else. We'd rather log the lookup
		// failure than 5xx and have Meta retry-storm us.
		return "", false
	}
	return tenant, true
}
