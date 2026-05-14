// Package analytics owns the mart_* rollups described in §2 of the
// technical plan ("Analytics Svc — OLAP rollups; CSAT/AHT/FCR/SLA").
//
// Phase-2 implementation is intentionally simple: a daily job sweeps
// every day-bucket since the cursor and recomputes the mart_*_daily
// rows for the tenant. We keep the SQL transparent rather than
// pulling in a stream-processor; the volumes don't warrant it at
// <100 agents per tenant.
package analytics

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Materialiser drives the nightly rollup.
type Materialiser struct {
	Pool *pgxpool.Pool
	// Now is overridable for tests.
	Now func() time.Time
}

// New constructs a Materialiser bound to pool.
func New(pool *pgxpool.Pool) *Materialiser {
	return &Materialiser{Pool: pool, Now: time.Now}
}

// RefreshTenant materialises every completed day-bucket since the
// tenant's cursor up to (but not including) today. Idempotent —
// re-running with the same cursor is a no-op.
func (m *Materialiser) RefreshTenant(ctx context.Context, tenantID uuid.UUID) error {
	today := m.Now().UTC().Truncate(24 * time.Hour)

	cursor, err := m.cursor(ctx, tenantID)
	if err != nil {
		return err
	}

	// Iterate one day at a time so a partial failure doesn't leave the
	// cursor wildly ahead of what we've actually persisted.
	for day := cursor.AddDate(0, 0, 1); day.Before(today); day = day.AddDate(0, 0, 1) {
		if err := m.refreshDay(ctx, tenantID, day); err != nil {
			return fmt.Errorf("analytics: tenant %s day %s: %w", tenantID, day.Format("2006-01-02"), err)
		}
		if err := m.advanceCursor(ctx, tenantID, day); err != nil {
			return err
		}
	}
	return nil
}

// RefreshAll iterates all known tenants.
func (m *Materialiser) RefreshAll(ctx context.Context) error {
	rows, err := m.Pool.Query(ctx, `SELECT id FROM tenants WHERE status = 'active'`)
	if err != nil {
		return err
	}
	defer rows.Close()
	var ids []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, id := range ids {
		if err := m.RefreshTenant(ctx, id); err != nil {
			slog.WarnContext(ctx, "analytics: refresh tenant failed",
				slog.String("tenant_id", id.String()), slog.String("err", err.Error()))
		}
	}
	return nil
}

func (m *Materialiser) cursor(ctx context.Context, tenantID uuid.UUID) (time.Time, error) {
	var d time.Time
	err := m.Pool.QueryRow(ctx,
		`SELECT last_processed_day FROM mart_cursor WHERE tenant_id = $1`,
		tenantID).Scan(&d)
	if err == nil {
		return d, nil
	}
	// First refresh for this tenant: seed cursor to 90d ago.
	seed := m.Now().UTC().Truncate(24 * time.Hour).AddDate(0, 0, -90)
	if _, err := m.Pool.Exec(ctx,
		`INSERT INTO mart_cursor (tenant_id, last_processed_day) VALUES ($1, $2)
		 ON CONFLICT (tenant_id) DO NOTHING`,
		tenantID, seed); err != nil {
		return time.Time{}, err
	}
	return seed, nil
}

func (m *Materialiser) advanceCursor(ctx context.Context, tenantID uuid.UUID, day time.Time) error {
	_, err := m.Pool.Exec(ctx, `
		UPDATE mart_cursor
		SET last_processed_day = $2, refreshed_at = now()
		WHERE tenant_id = $1 AND last_processed_day < $2`,
		tenantID, day)
	return err
}

// refreshDay computes per-channel + ALL rollups for one day. Wraps in
// a transaction so a tenant's mart for that day is consistent.
func (m *Materialiser) refreshDay(ctx context.Context, tenantID uuid.UUID, day time.Time) error {
	tx, err := m.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx,
		`DELETE FROM mart_tickets_daily WHERE tenant_id = $1 AND day = $2`,
		tenantID, day); err != nil {
		return err
	}

	// Per-channel rollup. The aggregation joins tickets to their
	// conversation (for channel) and computes the operational metrics.
	if _, err := tx.Exec(ctx, `
		INSERT INTO mart_tickets_daily
		  (tenant_id, channel, day, created_count, resolved_count, closed_count,
		   reopened_count, avg_first_response_seconds, avg_resolution_seconds,
		   first_response_sla_met, resolution_sla_met, fcr_rate)
		SELECT
		  t.tenant_id,
		  c.channel,
		  $2::date AS day,
		  COUNT(*) FILTER (WHERE t.created_at::date = $2) AS created_count,
		  COUNT(*) FILTER (WHERE t.resolved_at::date = $2) AS resolved_count,
		  COUNT(*) FILTER (WHERE t.closed_at::date = $2)   AS closed_count,
		  COUNT(*) FILTER (
		    WHERE EXISTS (
		      SELECT 1 FROM assignments a
		      WHERE a.ticket_id = t.id AND a.reason = 'reassign_offline' AND a.at::date = $2
		    )
		  ) AS reopened_count,
		  (AVG(EXTRACT(EPOCH FROM (t.first_response_at - t.created_at)))
		     FILTER (WHERE t.first_response_at IS NOT NULL AND t.created_at::date = $2)
		   )::int AS avg_first_response_seconds,
		  (AVG(EXTRACT(EPOCH FROM (t.resolved_at - t.created_at)))
		     FILTER (WHERE t.resolved_at::date = $2)
		   )::int AS avg_resolution_seconds,
		  AVG(
		    CASE
		      WHEN t.created_at::date = $2 AND t.sla_first_response_due IS NOT NULL
		      THEN CASE WHEN t.first_response_at <= t.sla_first_response_due THEN 1.0 ELSE 0.0 END
		    END
		  )::numeric(5,4) AS first_response_sla_met,
		  AVG(
		    CASE
		      WHEN t.resolved_at::date = $2 AND t.sla_resolution_due IS NOT NULL
		      THEN CASE WHEN t.resolved_at <= t.sla_resolution_due THEN 1.0 ELSE 0.0 END
		    END
		  )::numeric(5,4) AS resolution_sla_met,
		  AVG(
		    CASE
		      WHEN t.resolved_at::date = $2
		      THEN CASE WHEN (
		        SELECT COUNT(*) FROM tickets r WHERE r.conversation_id = t.conversation_id AND r.id <> t.id
		      ) = 0 THEN 1.0 ELSE 0.0 END
		    END
		  )::numeric(5,4) AS fcr_rate
		FROM tickets t
		JOIN conversations c ON c.id = t.conversation_id
		WHERE t.tenant_id = $1
		  AND (t.created_at::date = $2 OR t.resolved_at::date = $2 OR t.closed_at::date = $2)
		GROUP BY t.tenant_id, c.channel`,
		tenantID, day); err != nil {
		return err
	}

	// Roll the per-channel rows up into a synthetic channel='all'.
	if _, err := tx.Exec(ctx, `
		INSERT INTO mart_tickets_daily
		  (tenant_id, channel, day, created_count, resolved_count,
		   closed_count, reopened_count, avg_first_response_seconds,
		   avg_resolution_seconds, first_response_sla_met,
		   resolution_sla_met, fcr_rate)
		SELECT
		  tenant_id, 'all', $2::date,
		  SUM(created_count), SUM(resolved_count),
		  SUM(closed_count), SUM(reopened_count),
		  -- Volume-weighted averages.
		  (SUM(avg_first_response_seconds * created_count)::numeric
		     / NULLIF(SUM(created_count), 0))::int,
		  (SUM(avg_resolution_seconds * resolved_count)::numeric
		     / NULLIF(SUM(resolved_count), 0))::int,
		  (SUM(first_response_sla_met * created_count)
		     / NULLIF(SUM(created_count), 0))::numeric(5,4),
		  (SUM(resolution_sla_met * resolved_count)
		     / NULLIF(SUM(resolved_count), 0))::numeric(5,4),
		  (SUM(fcr_rate * resolved_count)
		     / NULLIF(SUM(resolved_count), 0))::numeric(5,4)
		FROM mart_tickets_daily
		WHERE tenant_id = $1 AND day = $2 AND channel <> 'all'
		GROUP BY tenant_id`,
		tenantID, day); err != nil {
		return err
	}

	// Per-agent rollup.
	if _, err := tx.Exec(ctx,
		`DELETE FROM mart_agents_daily WHERE tenant_id = $1 AND day = $2`,
		tenantID, day); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO mart_agents_daily
		  (tenant_id, agent_id, day, tickets_handled, messages_sent,
		   avg_handle_seconds, sla_breaches)
		SELECT
		  $1, asg.agent_id, $2::date,
		  COUNT(DISTINCT asg.ticket_id),
		  (SELECT COUNT(*) FROM messages m
		   WHERE m.tenant_id = $1 AND m.agent_id = asg.agent_id
		     AND m.created_at::date = $2 AND m.direction = 'out'),
		  NULL::int,
		  (SELECT COUNT(*) FROM tickets t2
		   WHERE t2.tenant_id = $1 AND t2.assigned_agent_id = asg.agent_id
		     AND (
		       (t2.first_response_at > t2.sla_first_response_due AND t2.first_response_at::date = $2)
		     OR (t2.resolved_at > t2.sla_resolution_due AND t2.resolved_at::date = $2)
		     ))
		FROM assignments asg
		WHERE asg.at::date = $2
		  AND asg.agent_id IN (SELECT id FROM agents WHERE tenant_id = $1)
		GROUP BY asg.agent_id`,
		tenantID, day); err != nil {
		return err
	}

	return tx.Commit(ctx)
}
