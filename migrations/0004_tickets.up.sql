-- 0004_tickets: tickets, partitioned messages, assignments.
-- Messages are RANGE-partitioned monthly so retention drops are cheap
-- (per §9 scaling path). Assignments are an event log we keep for
-- routing analytics and dispute resolution.

CREATE TYPE ticket_state AS ENUM (
  'new', 'open', 'pending', 'on_hold', 'resolved', 'closed', 'reopened'
);

CREATE TABLE tickets (
  id                       UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id                UUID NOT NULL REFERENCES tenants(id),
  conversation_id          UUID NOT NULL REFERENCES conversations(id),
  state                    ticket_state NOT NULL DEFAULT 'new',
  priority                 SMALLINT NOT NULL DEFAULT 3 CHECK (priority BETWEEN 1 AND 5),
  required_skills          TEXT[] NOT NULL DEFAULT '{}',
  assigned_agent_id        UUID REFERENCES agents(id),
  sla_first_response_due   TIMESTAMPTZ,
  sla_resolution_due       TIMESTAMPTZ,
  first_response_at        TIMESTAMPTZ,
  resolved_at              TIMESTAMPTZ,
  closed_at                TIMESTAMPTZ,
  created_at               TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at               TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX tickets_tenant_state_priority_idx
  ON tickets (tenant_id, state, priority);
CREATE INDEX tickets_assigned_open_idx
  ON tickets (assigned_agent_id)
  WHERE state IN ('open','pending');

CREATE TRIGGER tickets_set_updated_at
  BEFORE UPDATE ON tickets
  FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- Messages: composite PK on (created_at, id) so we can RANGE-partition by
-- created_at and still have a logical primary key.
CREATE TABLE messages (
  id                   BIGINT GENERATED ALWAYS AS IDENTITY,
  tenant_id            UUID NOT NULL,
  ticket_id            UUID NOT NULL,
  direction            TEXT NOT NULL CHECK (direction IN ('in','out','note')),
  agent_id             UUID,
  body                 TEXT NOT NULL DEFAULT '',
  attachments          UUID[] NOT NULL DEFAULT '{}',
  platform_message_id  TEXT,
  created_at           TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (created_at, id)
) PARTITION BY RANGE (created_at);

CREATE INDEX messages_ticket_idx ON messages (ticket_id, created_at DESC);
-- Lookup by platform message id (e.g. when reconciling outbound results).
-- Cross-partition uniqueness can't be enforced via a unique index on a
-- RANGE-partitioned table; dedupe lives in the per-connector
-- `*_webhook_events` tables (see 0006) and a Redis SET (§17).
CREATE INDEX messages_platform_idx
  ON messages (tenant_id, platform_message_id)
  WHERE platform_message_id IS NOT NULL;

-- A default partition catches anything outside known ranges so inserts
-- never fail; a job rotates monthly partitions ahead of time.
CREATE TABLE messages_default PARTITION OF messages DEFAULT;

CREATE TABLE assignments (
  id              BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  ticket_id       UUID NOT NULL REFERENCES tickets(id),
  agent_id        UUID NOT NULL REFERENCES agents(id),
  from_agent_id   UUID REFERENCES agents(id),
  reason          TEXT NOT NULL CHECK (reason IN
                  ('auto_route','manual','escalation','reassign_offline','sla_breach')),
  at              TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX assignments_ticket_idx ON assignments (ticket_id, at DESC);
CREATE INDEX assignments_agent_idx  ON assignments (agent_id, at DESC);
