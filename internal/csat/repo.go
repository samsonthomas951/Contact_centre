package csat

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// DefaultLinkLifetime is how long a survey link is valid. 7 days is
// long enough for a customer to remember the issue but short enough
// that we're not collecting on stale interactions.
const DefaultLinkLifetime = 7 * 24 * time.Hour

// ErrNotFound is returned when no survey matches a token.
var ErrNotFound = errors.New("csat: survey not found")

// ErrExpired is returned when a survey link has aged out.
var ErrExpired = errors.New("csat: survey expired")

// ErrAlreadyResponded is returned when the same token is used twice.
var ErrAlreadyResponded = errors.New("csat: survey already responded")

// Survey mirrors the public columns of csat_surveys.
type Survey struct {
	ID          uuid.UUID  `json:"id"`
	TenantID    uuid.UUID  `json:"tenant_id"`
	TicketID    uuid.UUID  `json:"ticket_id"`
	CustomerID  uuid.UUID  `json:"customer_id"`
	Channel     string     `json:"channel"`
	Token       string     `json:"-"`              // never returned to API callers
	ExpiresAt   time.Time  `json:"expires_at"`
	SentAt      time.Time  `json:"sent_at"`
	RespondedAt *time.Time `json:"responded_at,omitempty"`
	Score       *int16     `json:"score,omitempty"`
	Comment     string     `json:"comment,omitempty"`
}

// Repo is the data-access layer.
type Repo struct{ Pool *pgxpool.Pool }

// NewRepo binds a Repo.
func NewRepo(p *pgxpool.Pool) *Repo { return &Repo{Pool: p} }

// CreateParams is what callers pass to Create.
type CreateParams struct {
	TenantID   uuid.UUID
	TicketID   uuid.UUID
	CustomerID uuid.UUID
	Channel    string
	Lifetime   time.Duration
}

// Create inserts a new survey row with a fresh random token. Returns
// the row including the token so the caller can build the link
// (the token is not returned by any other read path).
func (r *Repo) Create(ctx context.Context, p CreateParams) (*Survey, string, error) {
	if p.TicketID == uuid.Nil || p.CustomerID == uuid.Nil || p.TenantID == uuid.Nil {
		return nil, "", errors.New("csat: tenant_id, ticket_id, customer_id required")
	}
	if p.Lifetime <= 0 {
		p.Lifetime = DefaultLinkLifetime
	}
	tok := NewToken()
	expires := time.Now().UTC().Add(p.Lifetime)

	row := r.Pool.QueryRow(ctx, `
		INSERT INTO csat_surveys
		  (tenant_id, ticket_id, customer_id, channel, token, expires_at)
		VALUES ($1, $2, $3, $4, $5, $6)
		RETURNING id, tenant_id, ticket_id, customer_id, channel, token,
		          expires_at, sent_at, responded_at, score, COALESCE(comment, '')`,
		p.TenantID, p.TicketID, p.CustomerID, p.Channel, tok, expires)
	s, err := scanSurvey(row)
	if err != nil {
		return nil, "", err
	}
	return s, tok, nil
}

// FindByToken returns the survey for a public token. Returns ErrExpired
// when past expires_at and ErrAlreadyResponded when already responded;
// the caller can decide how to surface either to the public collector.
func (r *Repo) FindByToken(ctx context.Context, token string) (*Survey, error) {
	if token == "" {
		return nil, ErrNotFound
	}
	row := r.Pool.QueryRow(ctx, `
		SELECT id, tenant_id, ticket_id, customer_id, channel, token,
		       expires_at, sent_at, responded_at, score, COALESCE(comment, '')
		FROM csat_surveys
		WHERE token = $1 AND revoked_at IS NULL`, token)
	s, err := scanSurvey(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if time.Now().UTC().After(s.ExpiresAt) {
		return s, ErrExpired
	}
	if s.RespondedAt != nil {
		return s, ErrAlreadyResponded
	}
	return s, nil
}

// SubmitParams carries a customer's response.
type SubmitParams struct {
	Token   string
	Score   int16  // 1..5
	Comment string // free text, capped at 4 KB by the HTTP layer
}

// Submit records a response and stamps responded_at. Re-submitting the
// same token returns ErrAlreadyResponded.
func (r *Repo) Submit(ctx context.Context, p SubmitParams) (*Survey, error) {
	if p.Score < 1 || p.Score > 5 {
		return nil, fmt.Errorf("csat: score must be 1..5, got %d", p.Score)
	}
	survey, err := r.FindByToken(ctx, p.Token)
	if err != nil {
		return survey, err
	}
	now := time.Now().UTC()
	if _, err := r.Pool.Exec(ctx, `
		UPDATE csat_surveys
		SET score = $2, comment = NULLIF($3,''), responded_at = $4
		WHERE token = $1 AND responded_at IS NULL`,
		p.Token, p.Score, p.Comment, now); err != nil {
		return nil, err
	}
	survey.Score = &p.Score
	survey.Comment = p.Comment
	survey.RespondedAt = &now
	return survey, nil
}

// Revoke clears a previously-submitted response. The row stays so
// audit reconstructs, but the score doesn't count toward analytics.
func (r *Repo) Revoke(ctx context.Context, tenantID, surveyID uuid.UUID) error {
	tag, err := r.Pool.Exec(ctx, `
		UPDATE csat_surveys
		SET score = NULL, comment = NULL, revoked_at = now()
		WHERE id = $1 AND tenant_id = $2 AND revoked_at IS NULL`,
		surveyID, tenantID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func scanSurvey(r interface{ Scan(...any) error }) (*Survey, error) {
	var s Survey
	if err := r.Scan(&s.ID, &s.TenantID, &s.TicketID, &s.CustomerID,
		&s.Channel, &s.Token, &s.ExpiresAt, &s.SentAt,
		&s.RespondedAt, &s.Score, &s.Comment); err != nil {
		return nil, err
	}
	return &s, nil
}
