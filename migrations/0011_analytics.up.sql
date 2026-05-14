-- 0011_analytics: OLAP rollup tables (mart_*).
-- Daily-granularity aggregates of operational metrics. Cheap to query
-- from the supervisor dashboard, refreshed by the nightly analytics
-- materialiser job. Per §2 of the plan: "Analytics Svc owns mart_*
-- tables (PG)."
--
-- Each row is one tenant × one channel × one day. The (tenant_id,
-- channel, day) tuple is the natural primary key.

CREATE TABLE mart_tickets_daily (
  tenant_id           UUID NOT NULL REFERENCES tenants(id),
  channel             TEXT NOT NULL CHECK (channel IN ('fb','x','wa','ig','widget','voice','all')),
  day                 DATE NOT NULL,
  -- Volume
  created_count       INTEGER NOT NULL DEFAULT 0,
  resolved_count      INTEGER NOT NULL DEFAULT 0,
  closed_count        INTEGER NOT NULL DEFAULT 0,
  reopened_count      INTEGER NOT NULL DEFAULT 0,
  -- Operational metrics
  avg_first_response_seconds  INTEGER,             -- AHT-first-response in seconds
  avg_resolution_seconds      INTEGER,             -- average wall-clock resolution
  -- SLA attainment as fraction in [0, 1].
  first_response_sla_met      NUMERIC(5,4),
  resolution_sla_met          NUMERIC(5,4),
  -- First-contact resolution: tickets resolved without reopening.
  fcr_rate                    NUMERIC(5,4),
  refreshed_at                TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, channel, day)
);

CREATE INDEX mart_tickets_daily_day_idx ON mart_tickets_daily (day DESC);

-- Per-agent daily rollup. Average handle time, ticket count, and
-- their share of SLA misses.
CREATE TABLE mart_agents_daily (
  tenant_id            UUID NOT NULL REFERENCES tenants(id),
  agent_id             UUID NOT NULL REFERENCES agents(id),
  day                  DATE NOT NULL,
  tickets_handled      INTEGER NOT NULL DEFAULT 0,
  messages_sent        INTEGER NOT NULL DEFAULT 0,
  avg_handle_seconds   INTEGER,
  sla_breaches         INTEGER NOT NULL DEFAULT 0,
  refreshed_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, agent_id, day)
);
CREATE INDEX mart_agents_daily_day_idx ON mart_agents_daily (tenant_id, day DESC);

-- Materialisation cursor. The nightly job reads `last_processed_at`,
-- materialises any day-buckets that have completed since, and bumps
-- the cursor. One row per tenant.
CREATE TABLE mart_cursor (
  tenant_id          UUID PRIMARY KEY REFERENCES tenants(id),
  last_processed_day DATE NOT NULL DEFAULT '1970-01-01',
  refreshed_at       TIMESTAMPTZ NOT NULL DEFAULT now()
);
