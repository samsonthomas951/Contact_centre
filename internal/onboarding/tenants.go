// Package onboarding owns the self-serve flows the tenant
// administrator uses to set up their workspace:
//
//   - tenant create / update / suspend
//   - agent invitation (delegated to Zitadel)
//   - channel registration: FB Page, X account, WA phone number, IG
//     account; each writes a row to the relevant token vault
//
// All endpoints sit behind RequireRole(admin). The DPA-relevant
// configuration (retention windows, DPO contact) lives on the tenants
// row directly.
package onboarding

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// TenantRepo handles tenant CRUD.
type TenantRepo struct{ Pool *pgxpool.Pool }

// NewTenantRepo binds a TenantRepo.
func NewTenantRepo(p *pgxpool.Pool) *TenantRepo { return &TenantRepo{Pool: p} }

// Tenant mirrors the public columns of tenants.
type Tenant struct {
	ID                       uuid.UUID `json:"id"`
	Slug                     string    `json:"slug"`
	DisplayName              string    `json:"display_name"`
	Status                   string    `json:"status"`
	RetentionMessagesDays    int       `json:"retention_messages_days"`
	RetentionDocumentsDays   int       `json:"retention_documents_days"`
	RetentionAuditDays       int       `json:"retention_audit_days"`
}

// CreateTenantParams is the input for Create.
type CreateTenantParams struct {
	Slug        string
	DisplayName string
}

// ErrSlugTaken is returned when the requested slug already exists.
var ErrSlugTaken = errors.New("onboarding: slug already taken")

// Create inserts a new tenant in status='active' with default retention
// windows from the tenants table defaults.
func (r *TenantRepo) Create(ctx context.Context, p CreateTenantParams) (*Tenant, error) {
	if p.Slug == "" || p.DisplayName == "" {
		return nil, errors.New("onboarding: slug and display_name required")
	}
	row := r.Pool.QueryRow(ctx, `
		INSERT INTO tenants (slug, display_name)
		VALUES ($1, $2)
		RETURNING id, slug, display_name, status,
		          retention_messages_days, retention_documents_days, retention_audit_days`,
		p.Slug, p.DisplayName)
	var t Tenant
	if err := row.Scan(&t.ID, &t.Slug, &t.DisplayName, &t.Status,
		&t.RetentionMessagesDays, &t.RetentionDocumentsDays, &t.RetentionAuditDays); err != nil {
		if isUniqueViolation(err) {
			return nil, ErrSlugTaken
		}
		return nil, err
	}
	return &t, nil
}

// UpdateRetentionParams allows the tenant admin to tweak the
// retention windows that flow into the per-tenant reaper.
type UpdateRetentionParams struct {
	TenantID          uuid.UUID
	MessagesDays      *int
	DocumentsDays     *int
	AuditDays         *int
}

// UpdateRetention applies whichever fields the admin supplied.
// Caller-side validation enforces the policy band; we accept any
// non-nil non-negative integer.
func (r *TenantRepo) UpdateRetention(ctx context.Context, p UpdateRetentionParams) (*Tenant, error) {
	if p.TenantID == uuid.Nil {
		return nil, errors.New("onboarding: tenant_id required")
	}
	row := r.Pool.QueryRow(ctx, `
		UPDATE tenants SET
		  retention_messages_days  = COALESCE($2, retention_messages_days),
		  retention_documents_days = COALESCE($3, retention_documents_days),
		  retention_audit_days     = COALESCE($4, retention_audit_days)
		WHERE id = $1
		RETURNING id, slug, display_name, status,
		          retention_messages_days, retention_documents_days, retention_audit_days`,
		p.TenantID, p.MessagesDays, p.DocumentsDays, p.AuditDays)
	var t Tenant
	if err := row.Scan(&t.ID, &t.Slug, &t.DisplayName, &t.Status,
		&t.RetentionMessagesDays, &t.RetentionDocumentsDays, &t.RetentionAuditDays); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf("onboarding: tenant %s not found", p.TenantID)
		}
		return nil, err
	}
	return &t, nil
}

// Suspend flips a tenant to status='suspended'. Reversible via Activate.
func (r *TenantRepo) Suspend(ctx context.Context, tenantID uuid.UUID) error {
	return r.setStatus(ctx, tenantID, "suspended")
}

// Activate restores a suspended tenant to active.
func (r *TenantRepo) Activate(ctx context.Context, tenantID uuid.UUID) error {
	return r.setStatus(ctx, tenantID, "active")
}

func (r *TenantRepo) setStatus(ctx context.Context, tenantID uuid.UUID, status string) error {
	tag, err := r.Pool.Exec(ctx, `UPDATE tenants SET status = $2 WHERE id = $1`,
		tenantID, status)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("onboarding: tenant %s not found", tenantID)
	}
	return nil
}

// Get fetches a tenant by id; returns ErrNotFound when missing.
func (r *TenantRepo) Get(ctx context.Context, tenantID uuid.UUID) (*Tenant, error) {
	var t Tenant
	err := r.Pool.QueryRow(ctx, `
		SELECT id, slug, display_name, status,
		       retention_messages_days, retention_documents_days, retention_audit_days
		FROM tenants WHERE id = $1`, tenantID).Scan(
		&t.ID, &t.Slug, &t.DisplayName, &t.Status,
		&t.RetentionMessagesDays, &t.RetentionDocumentsDays, &t.RetentionAuditDays)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("onboarding: tenant %s not found", tenantID)
	}
	return &t, err
}

// isUniqueViolation returns true when err is a PG unique-constraint
// failure (SQLSTATE 23505). Without bringing in a pgx-version-pinned
// import, we string-match on the error message; both pgx v4 and v5
// surface the SQLSTATE.
func isUniqueViolation(err error) bool {
	return err != nil && (errContains(err, "23505") || errContains(err, "duplicate key"))
}

func errContains(err error, sub string) bool {
	if err == nil {
		return false
	}
	s := err.Error()
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
