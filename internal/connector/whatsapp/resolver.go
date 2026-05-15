package whatsapp

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// PGTenantResolver looks phone_number_id up in the wa_phone_numbers
// table populated by the FB OAuth callback's WA discovery step. Used
// by WebhookHandler.Resolver.
type PGTenantResolver struct{ Pool *pgxpool.Pool }

// NewPGTenantResolver binds a resolver to a pool.
func NewPGTenantResolver(pool *pgxpool.Pool) *PGTenantResolver {
	return &PGTenantResolver{Pool: pool}
}

// ResolveTenant returns the tenant id for a WhatsApp Business phone
// number. The existing TenantResolver interface (in normalize.go)
// takes phone_number_id as a string and returns a string tenant id.
func (r *PGTenantResolver) ResolveTenant(phoneNumberID string) (string, bool) {
	var tenant string
	ctx := context.Background()
	err := r.Pool.QueryRow(ctx,
		`SELECT tenant_id::text FROM wa_phone_numbers WHERE phone_number_id = $1`,
		phoneNumberID).Scan(&tenant)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false
	}
	if err != nil {
		return "", false
	}
	return tenant, true
}
