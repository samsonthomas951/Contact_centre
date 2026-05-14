package onboarding

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// AgentRepo handles agent invitations and role/skill management.
//
// Identity itself lives in Zitadel; this package writes the local
// mirror that the rest of the platform reads. The full invitation flow
// (email to the new agent, Zitadel user create, organisation linkage)
// is delegated to a Zitadel-Go SDK call wrapped in InvitationSender --
// the repo provides the local-state half so tests don't need Zitadel.
type AgentRepo struct{ Pool *pgxpool.Pool }

// NewAgentRepo binds an AgentRepo.
func NewAgentRepo(p *pgxpool.Pool) *AgentRepo { return &AgentRepo{Pool: p} }

// Agent mirrors the public columns of agents.
type Agent struct {
	ID            uuid.UUID `json:"id"`
	TenantID      uuid.UUID `json:"tenant_id"`
	Email         string    `json:"email"`
	DisplayName   string    `json:"display_name"`
	Role          string    `json:"role"`
	MaxConcurrent int16     `json:"max_concurrent"`
	Active        bool      `json:"active"`
}

// InviteParams describes the new agent to provision.
type InviteParams struct {
	TenantID      uuid.UUID
	AgentID       uuid.UUID // produced by Zitadel; we mirror it locally
	Email         string
	DisplayName   string
	Role          string  // agent | senior_agent | supervisor | admin | auditor | dpo
	MaxConcurrent int16
	Skills        []string
}

// ErrInvalidRole is returned for an unknown role value.
var ErrInvalidRole = errors.New("onboarding: invalid role")

var validRoles = map[string]struct{}{
	"agent": {}, "senior_agent": {}, "supervisor": {},
	"admin": {}, "auditor": {}, "dpo": {},
}

// Invite mirrors a new agent into the local DB after Zitadel has
// provisioned the identity. Idempotent on (tenant_id, email): re-
// running with the same pair updates display_name + role + skills.
func (r *AgentRepo) Invite(ctx context.Context, p InviteParams) (*Agent, error) {
	if p.TenantID == uuid.Nil {
		return nil, errors.New("onboarding: tenant_id required")
	}
	if p.AgentID == uuid.Nil {
		return nil, errors.New("onboarding: agent_id required (Zitadel subject)")
	}
	if p.Email == "" || p.DisplayName == "" {
		return nil, errors.New("onboarding: email and display_name required")
	}
	if _, ok := validRoles[p.Role]; !ok {
		return nil, ErrInvalidRole
	}
	if p.MaxConcurrent <= 0 {
		p.MaxConcurrent = 5
	}

	tx, err := r.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, `
		INSERT INTO agents (id, tenant_id, email, display_name, role, max_concurrent)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (tenant_id, email)
		DO UPDATE SET display_name = EXCLUDED.display_name,
		              role         = EXCLUDED.role,
		              max_concurrent = EXCLUDED.max_concurrent,
		              active = TRUE`,
		p.AgentID, p.TenantID, p.Email, p.DisplayName, p.Role, p.MaxConcurrent); err != nil {
		return nil, err
	}

	// Replace skills wholesale. Caller-friendly: re-inviting with a new
	// skill list overwrites; pass nil to leave skills empty.
	if _, err := tx.Exec(ctx,
		`DELETE FROM agent_skills WHERE agent_id = $1`, p.AgentID); err != nil {
		return nil, err
	}
	for _, sk := range p.Skills {
		if _, err := tx.Exec(ctx,
			`INSERT INTO agent_skills (agent_id, skill) VALUES ($1, $2)`,
			p.AgentID, sk); err != nil {
			return nil, err
		}
	}

	// Seed agent_status if not present.
	if _, err := tx.Exec(ctx, `
		INSERT INTO agent_status (agent_id, status, current_load)
		VALUES ($1, 'offline', 0)
		ON CONFLICT (agent_id) DO NOTHING`, p.AgentID); err != nil {
		return nil, err
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return r.Get(ctx, p.AgentID)
}

// Get fetches an agent.
func (r *AgentRepo) Get(ctx context.Context, agentID uuid.UUID) (*Agent, error) {
	var a Agent
	err := r.Pool.QueryRow(ctx, `
		SELECT id, tenant_id, email, display_name, role, max_concurrent, active
		FROM agents WHERE id = $1`, agentID).Scan(
		&a.ID, &a.TenantID, &a.Email, &a.DisplayName, &a.Role, &a.MaxConcurrent, &a.Active)
	if err != nil {
		return nil, fmt.Errorf("onboarding: get agent %s: %w", agentID, err)
	}
	return &a, nil
}

// Deactivate marks an agent inactive without dropping the row -- audit
// references survive, but routing skips them.
func (r *AgentRepo) Deactivate(ctx context.Context, tenantID, agentID uuid.UUID) error {
	tag, err := r.Pool.Exec(ctx,
		`UPDATE agents SET active = FALSE WHERE id = $1 AND tenant_id = $2`,
		agentID, tenantID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("onboarding: agent %s not found in tenant %s", agentID, tenantID)
	}
	// Also drop them out of routing immediately.
	_, _ = r.Pool.Exec(ctx,
		`UPDATE agent_status SET status = 'offline' WHERE agent_id = $1`, agentID)
	return nil
}
