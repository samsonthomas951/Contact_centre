// Package customer is the read-side for the customer profile sidebar
// on the agent UI. Production deployments add write paths later (merge,
// GDPR rename, etc.); for now we only need to render context, so the
// repo is GetWithHistory-only.
package customer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrNotFound surfaces 404 to callers.
var ErrNotFound = errors.New("customer: not found")

// Repo binds a pool.
type Repo struct{ Pool *pgxpool.Pool }

// NewRepo wires the repo.
func NewRepo(pool *pgxpool.Pool) *Repo { return &Repo{Pool: pool} }

// ChannelRef is one platform identity the customer has used to reach
// us. Repeats happen when the same person hits us via, say, FB and
// IG -- each gets its own row in the sidebar.
type ChannelRef struct {
	Channel     string `json:"channel"`
	ExternalRef string `json:"external_ref"`
}

// RecentTicket is the truncated ticket-history shape the sidebar
// renders. Subject is the first inbound message body, trimmed.
type RecentTicket struct {
	ID        uuid.UUID `json:"id"`
	State     string    `json:"state"`
	Channel   string    `json:"channel"`
	Subject   string    `json:"subject"`
	CreatedAt time.Time `json:"created_at"`
}

// Profile is the bundled response GetWithHistory returns.
type Profile struct {
	ID             uuid.UUID      `json:"id"`
	TenantID       uuid.UUID      `json:"tenant_id"`
	DisplayName    string         `json:"display_name"`
	Email          string         `json:"email,omitempty"`
	Phone          string         `json:"phone,omitempty"`
	Channels       []ChannelRef   `json:"channels"`
	TicketCount    int            `json:"ticket_count"`
	FirstContactAt *time.Time     `json:"first_contact_at,omitempty"`
	LastContactAt  *time.Time     `json:"last_contact_at,omitempty"`
	RecentTickets  []RecentTicket `json:"recent_tickets"`
}

// CustomerIDForTicket resolves a ticket id to its conversation's
// customer id. Tenant-scoped so a stolen ticket id from one tenant
// can't probe another.
func (r *Repo) CustomerIDForTicket(ctx context.Context, tenantID, ticketID uuid.UUID) (uuid.UUID, error) {
	var id uuid.UUID
	err := r.Pool.QueryRow(ctx, `
		SELECT c.customer_id
		FROM tickets t
		JOIN conversations c ON c.id = t.conversation_id
		WHERE t.tenant_id = $1 AND t.id = $2`,
		tenantID, ticketID).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, ErrNotFound
	}
	return id, err
}

// GetWithHistory loads the customer + the latest 10 tickets and the
// first-message subject per ticket. Tenant-scoped so a hostile customer
// id from one tenant can't peek at another.
func (r *Repo) GetWithHistory(ctx context.Context, tenantID, customerID uuid.UUID) (*Profile, error) {
	var (
		p      Profile
		email  *string
		phone  *string
		refs   []byte
	)
	err := r.Pool.QueryRow(ctx, `
		SELECT id, tenant_id, COALESCE(display_name, ''), email, phone, external_refs
		FROM customers
		WHERE tenant_id = $1 AND id = $2`,
		tenantID, customerID).
		Scan(&p.ID, &p.TenantID, &p.DisplayName, &email, &phone, &refs)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("customer: load: %w", err)
	}
	if email != nil {
		p.Email = *email
	}
	if phone != nil {
		p.Phone = *phone
	}
	p.Channels = parseExternalRefs(refs)

	// Ticket history -- recent 10 + count + first/last contact in one
	// round-trip via a CTE. Subject is the first inbound message's
	// body trimmed; the LATERAL guarantees one row per ticket without
	// a window function.
	rows, err := r.Pool.Query(ctx, `
		WITH tix AS (
		  SELECT t.id, t.state, c.channel, t.created_at
		  FROM tickets t
		  JOIN conversations c ON c.id = t.conversation_id
		  WHERE t.tenant_id = $1 AND c.customer_id = $2
		)
		SELECT t.id, t.state::text, t.channel, t.created_at,
		       COALESCE(m.body, '')
		FROM tix t
		LEFT JOIN LATERAL (
		  SELECT body FROM messages
		  WHERE tenant_id = $1 AND ticket_id = t.id AND direction = 'in'
		  ORDER BY created_at ASC LIMIT 1
		) m ON TRUE
		ORDER BY t.created_at DESC
		LIMIT 10`, tenantID, customerID)
	if err != nil {
		return nil, fmt.Errorf("customer: history: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var rt RecentTicket
		var body string
		if err := rows.Scan(&rt.ID, &rt.State, &rt.Channel, &rt.CreatedAt, &body); err != nil {
			return nil, err
		}
		rt.Subject = truncate(body, 80)
		p.RecentTickets = append(p.RecentTickets, rt)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Aggregates -- count + first/last contact across ALL tickets
	// (not just the page we just fetched). Qualify created_at because
	// both tickets and conversations carry one.
	if err := r.Pool.QueryRow(ctx, `
		SELECT count(*)::int, min(t.created_at), max(t.created_at)
		FROM tickets t
		JOIN conversations c ON c.id = t.conversation_id
		WHERE t.tenant_id = $1 AND c.customer_id = $2`,
		tenantID, customerID).Scan(&p.TicketCount, &p.FirstContactAt, &p.LastContactAt); err != nil {
		return nil, fmt.Errorf("customer: aggregates: %w", err)
	}
	return &p, nil
}

// parseExternalRefs turns the JSONB blob {"fb":"PSID_001","ig":"IGSID_005"}
// into a stable-ordered slice of ChannelRef.
func parseExternalRefs(raw []byte) []ChannelRef {
	if len(raw) == 0 {
		return []ChannelRef{}
	}
	var m map[string]string
	if err := json.Unmarshal(raw, &m); err != nil {
		return []ChannelRef{}
	}
	// Stable iteration order for the UI: fb / ig / wa / x / widget /
	// voice / email, then anything else.
	order := []string{"fb", "ig", "wa", "x", "widget", "voice", "email"}
	out := make([]ChannelRef, 0, len(m))
	seen := make(map[string]bool, len(m))
	for _, k := range order {
		if v, ok := m[k]; ok {
			out = append(out, ChannelRef{Channel: k, ExternalRef: v})
			seen[k] = true
		}
	}
	for k, v := range m {
		if !seen[k] {
			out = append(out, ChannelRef{Channel: k, ExternalRef: v})
		}
	}
	return out
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
