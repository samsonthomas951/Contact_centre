package analytics

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// TicketsDailyRow is one row of mart_tickets_daily.
type TicketsDailyRow struct {
	Channel                 string     `json:"channel"`
	Day                     time.Time  `json:"day"`
	CreatedCount            int        `json:"created_count"`
	ResolvedCount           int        `json:"resolved_count"`
	ClosedCount             int        `json:"closed_count"`
	AvgFirstResponseSeconds *int       `json:"avg_first_response_seconds,omitempty"`
	AvgResolutionSeconds    *int       `json:"avg_resolution_seconds,omitempty"`
	FirstResponseSLAMet     *float64   `json:"first_response_sla_met,omitempty"`
	ResolutionSLAMet        *float64   `json:"resolution_sla_met,omitempty"`
	FCRRate                 *float64   `json:"fcr_rate,omitempty"`
	CSATResponses           int        `json:"csat_responses"`
	CSATAvg                 *float64   `json:"csat_avg,omitempty"`
}

// TicketsDaily returns the per-channel rollup for the date range
// [from, to] inclusive. Channel="all" rows are included; the caller
// chooses which to render.
func (m *Materialiser) TicketsDaily(ctx context.Context, tenantID uuid.UUID, from, to time.Time) ([]TicketsDailyRow, error) {
	rows, err := m.Pool.Query(ctx, `
		SELECT channel, day, created_count, resolved_count, closed_count,
		       avg_first_response_seconds, avg_resolution_seconds,
		       first_response_sla_met, resolution_sla_met, fcr_rate,
		       csat_responses, csat_avg
		FROM mart_tickets_daily
		WHERE tenant_id = $1 AND day BETWEEN $2 AND $3
		ORDER BY day, channel`,
		tenantID, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []TicketsDailyRow{}
	for rows.Next() {
		var r TicketsDailyRow
		if err := rows.Scan(&r.Channel, &r.Day, &r.CreatedCount, &r.ResolvedCount,
			&r.ClosedCount, &r.AvgFirstResponseSeconds, &r.AvgResolutionSeconds,
			&r.FirstResponseSLAMet, &r.ResolutionSLAMet, &r.FCRRate,
			&r.CSATResponses, &r.CSATAvg); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// AgentsDailyRow is one row of mart_agents_daily.
type AgentsDailyRow struct {
	AgentID          uuid.UUID `json:"agent_id"`
	Day              time.Time `json:"day"`
	TicketsHandled   int       `json:"tickets_handled"`
	MessagesSent     int       `json:"messages_sent"`
	AvgHandleSeconds *int      `json:"avg_handle_seconds,omitempty"`
	SLABreaches      int       `json:"sla_breaches"`
}

// AgentsDaily returns the per-agent rollup for [from, to].
func (m *Materialiser) AgentsDaily(ctx context.Context, tenantID uuid.UUID, from, to time.Time) ([]AgentsDailyRow, error) {
	rows, err := m.Pool.Query(ctx, `
		SELECT agent_id, day, tickets_handled, messages_sent,
		       avg_handle_seconds, sla_breaches
		FROM mart_agents_daily
		WHERE tenant_id = $1 AND day BETWEEN $2 AND $3
		ORDER BY day, agent_id`,
		tenantID, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AgentsDailyRow{}
	for rows.Next() {
		var r AgentsDailyRow
		if err := rows.Scan(&r.AgentID, &r.Day, &r.TicketsHandled, &r.MessagesSent,
			&r.AvgHandleSeconds, &r.SLABreaches); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
