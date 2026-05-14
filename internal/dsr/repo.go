package dsr

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrNotFound is returned when a request id is not in the tenant.
var ErrNotFound = errors.New("dsr: request not found")

// Repo is the data-access layer for DSR requests.
type Repo struct{ Pool *pgxpool.Pool }

// NewRepo binds a Repo.
func NewRepo(p *pgxpool.Pool) *Repo { return &Repo{Pool: p} }

// OpenParams is the input for OpenRequest.
type OpenParams struct {
	TenantID           uuid.UUID
	CustomerID         *uuid.UUID
	SubjectEmail       string
	SubjectPhone       string
	SubjectExternalRef string
	Kind               Kind
	Reason             string
}

// OpenRequest inserts a new DSR row in state=received with due_at set
// to received_at + DueWindow.
func (r *Repo) OpenRequest(ctx context.Context, p OpenParams) (*Request, error) {
	if err := p.Kind.Validate(); err != nil {
		return nil, err
	}
	if p.CustomerID == nil && p.SubjectEmail == "" && p.SubjectPhone == "" && p.SubjectExternalRef == "" {
		return nil, ErrSubjectRequired
	}
	now := time.Now().UTC()
	due := now.Add(DueWindow)
	row := r.Pool.QueryRow(ctx, `
		INSERT INTO dsr_requests
		  (tenant_id, customer_id, subject_email, subject_phone, subject_external_ref,
		   kind, reason, received_at, due_at)
		VALUES ($1, $2, NULLIF($3,''), NULLIF($4,''), NULLIF($5,''),
		        $6::dsr_kind, NULLIF($7,''), $8, $9)
		RETURNING ` + columnList,
		p.TenantID, p.CustomerID, p.SubjectEmail, p.SubjectPhone, p.SubjectExternalRef,
		string(p.Kind), p.Reason, now, due)
	return scanRequest(row)
}

// Get returns one request by id, tenant-scoped.
func (r *Repo) Get(ctx context.Context, tenantID, id uuid.UUID) (*Request, error) {
	row := r.Pool.QueryRow(ctx,
		`SELECT `+columnList+` FROM dsr_requests WHERE tenant_id = $1 AND id = $2`,
		tenantID, id)
	req, err := scanRequest(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return req, err
}

// List returns open requests for the tenant, soonest-due first.
// `state` filters; empty string returns received + in_progress.
func (r *Repo) List(ctx context.Context, tenantID uuid.UUID, state State, limit int) ([]Request, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	q := `SELECT ` + columnList + ` FROM dsr_requests WHERE tenant_id = $1`
	args := []any{tenantID}
	if state == "" {
		q += ` AND state IN ('received','in_progress')`
	} else {
		q += ` AND state = $2::dsr_state`
		args = append(args, string(state))
	}
	q += ` ORDER BY due_at ASC LIMIT ` + fmt.Sprint(limit)

	rows, err := r.Pool.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []Request{}
	for rows.Next() {
		req, err := scanRequest(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *req)
	}
	return out, rows.Err()
}

// Assign sets the assigned_to_agent_id and moves received -> in_progress.
func (r *Repo) Assign(ctx context.Context, tenantID, id, agentID uuid.UUID) (*Request, error) {
	tx, err := r.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var current State
	if err := tx.QueryRow(ctx,
		`SELECT state FROM dsr_requests WHERE tenant_id = $1 AND id = $2 FOR UPDATE`,
		tenantID, id).Scan(&current); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	if current == StateReceived {
		if err := Transition(current, StateInProgress); err != nil {
			return nil, err
		}
		if _, err := tx.Exec(ctx,
			`UPDATE dsr_requests SET state = 'in_progress', assigned_to_agent_id = $3
			 WHERE tenant_id = $1 AND id = $2`,
			tenantID, id, agentID); err != nil {
			return nil, err
		}
	} else {
		// Already in_progress or terminal — only rebind the assignee.
		if _, err := tx.Exec(ctx,
			`UPDATE dsr_requests SET assigned_to_agent_id = $3
			 WHERE tenant_id = $1 AND id = $2`,
			tenantID, id, agentID); err != nil {
			return nil, err
		}
	}

	row := tx.QueryRow(ctx,
		`SELECT `+columnList+` FROM dsr_requests WHERE tenant_id = $1 AND id = $2`,
		tenantID, id)
	req, err := scanRequest(row)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return req, nil
}

// ResolveParams moves a request to a terminal state.
type ResolveParams struct {
	TenantID       uuid.UUID
	ID             uuid.UUID
	To             State // fulfilled | partially_fulfilled | rejected | withdrawn
	ResolutionNote string
}

// Resolve transitions the request to a terminal state and stamps
// fulfilled_at. Rejected requires a resolution_note (DB CHECK
// constraint also enforces this).
func (r *Repo) Resolve(ctx context.Context, p ResolveParams) (*Request, error) {
	tx, err := r.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var current State
	if err := tx.QueryRow(ctx,
		`SELECT state FROM dsr_requests WHERE tenant_id = $1 AND id = $2 FOR UPDATE`,
		p.TenantID, p.ID).Scan(&current); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	if err := Transition(current, p.To); err != nil {
		return nil, err
	}

	now := time.Now().UTC()
	if _, err := tx.Exec(ctx, `
		UPDATE dsr_requests SET
		  state           = $3::dsr_state,
		  fulfilled_at    = CASE WHEN $3 IN ('fulfilled','partially_fulfilled') THEN $4 ELSE fulfilled_at END,
		  resolution_note = NULLIF($5,'')
		WHERE tenant_id = $1 AND id = $2`,
		p.TenantID, p.ID, string(p.To), now, p.ResolutionNote); err != nil {
		return nil, err
	}

	row := tx.QueryRow(ctx,
		`SELECT `+columnList+` FROM dsr_requests WHERE tenant_id = $1 AND id = $2`,
		p.TenantID, p.ID)
	req, err := scanRequest(row)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return req, nil
}

// LogAction appends one dsr_actions row.
func (r *Repo) LogAction(ctx context.Context, requestID uuid.UUID, agentID *uuid.UUID, kind, note string) error {
	_, err := r.Pool.Exec(ctx, `
		INSERT INTO dsr_actions (request_id, actor_agent_id, kind, note)
		VALUES ($1, $2, $3, NULLIF($4,''))`,
		requestID, agentID, kind, note)
	return err
}

// columnList is the canonical select clause used by every read query.
const columnList = `
  id, tenant_id, customer_id, COALESCE(subject_email::text,''), COALESCE(subject_phone,''),
  COALESCE(subject_external_ref,''), kind::text, state::text, COALESCE(reason,''),
  received_at, due_at, fulfilled_at, COALESCE(resolution_note,''),
  assigned_to_agent_id, created_at, updated_at`

func scanRequest(r interface{ Scan(...any) error }) (*Request, error) {
	var req Request
	var kindStr, stateStr string
	if err := r.Scan(
		&req.ID, &req.TenantID, &req.CustomerID, &req.SubjectEmail, &req.SubjectPhone,
		&req.SubjectExternalRef, &kindStr, &stateStr, &req.Reason,
		&req.ReceivedAt, &req.DueAt, &req.FulfilledAt, &req.ResolutionNote,
		&req.AssignedToAgentID, &req.CreatedAt, &req.UpdatedAt,
	); err != nil {
		return nil, err
	}
	req.Kind = Kind(kindStr)
	req.State = State(stateStr)
	return &req, nil
}
