package auth

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/google/uuid"
)

// Config holds the Zitadel issuer + audience the service will accept.
type Config struct {
	IssuerURL string        `env:"OIDC_ISSUER_URL" required:"true"`
	Audience  string        `env:"OIDC_AUDIENCE"   required:"true"`
	ClockSkew time.Duration `env:"OIDC_CLOCK_SKEW" default:"30s"`
	// TenantClaim names the JWT claim that carries the tenant id.
	// Zitadel projects can map a project meta-attribute into a custom
	// claim; we default to a conventional name.
	TenantClaim string `env:"OIDC_TENANT_CLAIM" default:"urn:contactcentre:tenant_id"`
	// RolesClaim names the JWT claim that carries the agent's roles.
	RolesClaim string `env:"OIDC_ROLES_CLAIM" default:"urn:contactcentre:roles"`
	// DemoMode bypasses real OIDC verification and accepts a bearer
	// token of the form "demo:<agent_uuid>:<tenant_uuid>:<role>" so
	// the deploy/docker-compose.demo.yml stack can boot without
	// Zitadel. NEVER set this in production -- there's no signature
	// check; any request can synthesise any identity.
	DemoMode bool `env:"AUTH_DEMO_MODE" default:"false"`
}

// Verifier validates incoming bearer JWTs against the Zitadel issuer.
type Verifier struct {
	cfg      Config
	provider *oidc.Provider
	verifier *oidc.IDTokenVerifier
	demo     bool
}

// NewVerifier discovers Zitadel's OIDC metadata (JWKS, issuer) and
// returns a Verifier ready to validate tokens. In DemoMode it skips
// the discovery (which would fail without a real Zitadel) and returns
// a Verifier whose Verify() method parses the demo bearer format.
func NewVerifier(ctx context.Context, cfg Config) (*Verifier, error) {
	if cfg.DemoMode {
		return &Verifier{cfg: cfg, demo: true}, nil
	}
	provider, err := oidc.NewProvider(ctx, cfg.IssuerURL)
	if err != nil {
		return nil, fmt.Errorf("auth: discover issuer %s: %w", cfg.IssuerURL, err)
	}
	v := provider.Verifier(&oidc.Config{
		ClientID:        cfg.Audience,
		SkipIssuerCheck: false,
	})
	return &Verifier{cfg: cfg, provider: provider, verifier: v}, nil
}

// Verify parses and validates the bearer token from rawToken, returning
// the embedded Identity. The caller is expected to have already stripped
// any "Bearer " prefix.
func (v *Verifier) Verify(ctx context.Context, rawToken string) (Identity, error) {
	if v.demo {
		return demoVerify(rawToken)
	}
	tok, err := v.verifier.Verify(ctx, rawToken)
	if err != nil {
		return Identity{}, fmt.Errorf("auth: verify: %w", err)
	}
	var claims map[string]any
	if err := tok.Claims(&claims); err != nil {
		return Identity{}, fmt.Errorf("auth: claims: %w", err)
	}
	return extractIdentity(claims, v.cfg)
}

// demoVerify parses the demo-mode bearer format:
//
//	demo:<agent_uuid>:<tenant_uuid>:<role>[,<role>...]
//
// Used only by the docker-compose.demo.yml stack so the agent UI and
// gateway can shake hands without Zitadel. Production never enters
// this code path because cfg.DemoMode defaults false.
func demoVerify(token string) (Identity, error) {
	const prefix = "demo:"
	if !strings.HasPrefix(token, prefix) {
		return Identity{}, fmt.Errorf("auth: demo: expected %q prefix", prefix)
	}
	parts := strings.Split(token[len(prefix):], ":")
	if len(parts) < 3 {
		return Identity{}, fmt.Errorf("auth: demo: want demo:<agent>:<tenant>:<role>")
	}
	agentID, err := uuid.Parse(parts[0])
	if err != nil {
		return Identity{}, fmt.Errorf("auth: demo: bad agent id: %w", err)
	}
	tenantID, err := uuid.Parse(parts[1])
	if err != nil {
		return Identity{}, fmt.Errorf("auth: demo: bad tenant id: %w", err)
	}
	roles := parseRoles(parts[2])
	if len(roles) == 0 {
		return Identity{}, fmt.Errorf("auth: demo: no valid roles in %q", parts[2])
	}
	return Identity{
		AgentID: agentID, TenantID: tenantID,
		Email: "demo-agent@example.local",
		Roles: roles,
	}, nil
}

// extractIdentity is split out so it can be unit-tested without spinning
// up an OIDC issuer.
func extractIdentity(claims map[string]any, cfg Config) (Identity, error) {
	sub, _ := claims["sub"].(string)
	if sub == "" {
		return Identity{}, errors.New("auth: token missing subject")
	}
	agentID, err := uuid.Parse(sub)
	if err != nil {
		return Identity{}, fmt.Errorf("auth: subject %q is not a UUID: %w", sub, err)
	}

	tenantRaw, _ := claims[cfg.TenantClaim].(string)
	if tenantRaw == "" {
		return Identity{}, fmt.Errorf("auth: token missing tenant claim %q", cfg.TenantClaim)
	}
	tenantID, err := uuid.Parse(tenantRaw)
	if err != nil {
		return Identity{}, fmt.Errorf("auth: tenant claim %q is not a UUID: %w", tenantRaw, err)
	}

	email, _ := claims["email"].(string)

	id := Identity{AgentID: agentID, TenantID: tenantID, Email: email}
	id.Roles = parseRoles(claims[cfg.RolesClaim])
	return id, nil
}

// parseRoles accepts a few common shapes — a JSON array of strings, a
// space- or comma-delimited string, or a Zitadel-style object whose keys
// are role names — and produces a deduplicated, validated slice of Role.
func parseRoles(raw any) []Role {
	add := func(out *[]Role, r string) {
		r = strings.TrimSpace(r)
		if r == "" {
			return
		}
		role := Role(r)
		switch role {
		case RoleAgent, RoleSeniorAgent, RoleSupervisor, RoleAdmin, RoleAuditor, RoleDPO:
		default:
			return
		}
		if slices.Contains(*out, role) {
			return
		}
		*out = append(*out, role)
	}

	var roles []Role
	switch v := raw.(type) {
	case []any:
		for _, item := range v {
			if s, ok := item.(string); ok {
				add(&roles, s)
			}
		}
	case []string:
		for _, s := range v {
			add(&roles, s)
		}
	case string:
		for _, s := range strings.FieldsFunc(v, func(r rune) bool {
			return r == ',' || r == ' '
		}) {
			add(&roles, s)
		}
	case map[string]any:
		for k := range v {
			add(&roles, k)
		}
	}
	return roles
}
