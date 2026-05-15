package instagram

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// PGTenantResolver looks ig_user_id up in the ig_accounts table that
// the FB OAuth callback (or the Path-2 direct-IG OAuth flow, when we
// build it) populates. Used by WebhookHandler.Resolver.
type PGTenantResolver struct{ Pool *pgxpool.Pool }

// NewPGTenantResolver binds a resolver to a pool.
func NewPGTenantResolver(pool *pgxpool.Pool) *PGTenantResolver {
	return &PGTenantResolver{Pool: pool}
}

// ResolveTenant returns the tenant id for an IG Business account. The
// existing TenantResolver interface (in normalize.go) takes ig_user_id
// as a string and returns a string tenant id.
func (r *PGTenantResolver) ResolveTenant(igUserID string) (string, bool) {
	var tenant string
	ctx := context.Background()
	err := r.Pool.QueryRow(ctx,
		`SELECT tenant_id::text FROM ig_accounts WHERE ig_user_id = $1`, igUserID).Scan(&tenant)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false
	}
	if err != nil {
		return "", false
	}
	return tenant, true
}
