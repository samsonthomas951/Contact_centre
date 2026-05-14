// Package auth verifies Zitadel-issued JWTs, attaches the resulting
// identity to the request context, and enforces RBAC at HTTP handler
// boundaries.
//
// Defense in depth: the API gateway verifies the JWT once at ingress and
// the called service re-checks roles against the same context — never
// trust upstream alone.
package auth

import (
	"context"
	"errors"
	"net/http"
	"slices"

	"github.com/google/uuid"
	"github.com/samsonthomas951/contact-centre/internal/pkg/ctxkeys"
)

// Role names mirror the RBAC table in §5 of the technical plan.
type Role string

const (
	RoleAgent       Role = "agent"
	RoleSeniorAgent Role = "senior_agent"
	RoleSupervisor  Role = "supervisor"
	RoleAdmin       Role = "admin"
	RoleAuditor     Role = "auditor"
	RoleDPO         Role = "dpo"
)

// Identity is what every authenticated request carries on its context.
type Identity struct {
	AgentID  uuid.UUID
	TenantID uuid.UUID
	Email    string
	Roles    []Role
}

// HasRole reports whether the identity holds at least one of want.
func (i Identity) HasRole(want ...Role) bool {
	for _, w := range want {
		if slices.Contains(i.Roles, w) {
			return true
		}
	}
	return false
}

// ErrNoIdentity is returned by FromContext when the request was not
// authenticated.
var ErrNoIdentity = errors.New("auth: no identity on context")

// FromContext returns the identity attached by the auth middleware.
func FromContext(ctx context.Context) (Identity, error) {
	v := ctx.Value(identityCtxKey{})
	if v == nil {
		return Identity{}, ErrNoIdentity
	}
	id, ok := v.(Identity)
	if !ok {
		return Identity{}, ErrNoIdentity
	}
	return id, nil
}

// withIdentity stores id on ctx and mirrors the agent/tenant IDs onto
// the well-known ctxkeys so logging and audit middleware pick them up.
func withIdentity(ctx context.Context, id Identity) context.Context {
	ctx = context.WithValue(ctx, identityCtxKey{}, id)
	ctx = context.WithValue(ctx, ctxkeys.AgentID, id.AgentID.String())
	ctx = context.WithValue(ctx, ctxkeys.TenantID, id.TenantID.String())
	roleStrs := make([]string, len(id.Roles))
	for i, r := range id.Roles {
		roleStrs[i] = string(r)
	}
	ctx = context.WithValue(ctx, ctxkeys.AgentRoles, roleStrs)
	return ctx
}

// identityCtxKey is unexported so other packages cannot forge identities
// by stashing their own values under the same key.
type identityCtxKey struct{}

// RequireRole returns a middleware that 403s requests whose identity
// lacks any of the listed roles. Used by the API gateway and re-applied
// inside services for defense in depth.
func RequireRole(want ...Role) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			id, err := FromContext(r.Context())
			if err != nil {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			if !id.HasRole(want...) {
				http.Error(w, "forbidden", http.StatusForbidden)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
