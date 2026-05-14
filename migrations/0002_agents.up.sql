-- 0002_agents: agent directory mirrored from Zitadel + skills + presence.
-- Zitadel is the source of truth for identity; this table caches the
-- minimum needed for routing decisions (skills, capacity, presence) and
-- foreign-key targets for assignments/audit.

CREATE TABLE agents (
  id                 UUID PRIMARY KEY,                 -- subject from Zitadel
  tenant_id          UUID NOT NULL REFERENCES tenants(id),
  email              CITEXT NOT NULL,
  display_name       TEXT NOT NULL,
  role               TEXT NOT NULL DEFAULT 'agent'
                     CHECK (role IN ('agent','senior_agent','supervisor','admin','auditor','dpo')),
  max_concurrent     SMALLINT NOT NULL DEFAULT 5 CHECK (max_concurrent BETWEEN 1 AND 50),
  active             BOOLEAN NOT NULL DEFAULT TRUE,
  created_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (tenant_id, email)
);
CREATE INDEX agents_tenant_active_idx ON agents (tenant_id) WHERE active;

CREATE TRIGGER agents_set_updated_at
  BEFORE UPDATE ON agents
  FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- Skills are free-form short tags; routing matches required_skills ⊆ skills.
CREATE TABLE agent_skills (
  agent_id   UUID NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
  skill      TEXT NOT NULL,
  PRIMARY KEY (agent_id, skill)
);
CREATE INDEX agent_skills_skill_idx ON agent_skills (skill);

-- Presence state is hot-path; we mirror Redis to PG for crash recovery
-- and supervisor reporting. Last-write-wins on heartbeat.
CREATE TABLE agent_status (
  agent_id        UUID PRIMARY KEY REFERENCES agents(id) ON DELETE CASCADE,
  status          TEXT NOT NULL CHECK (status IN ('online','away','break','offline')),
  current_load    SMALLINT NOT NULL DEFAULT 0,
  last_heartbeat  TIMESTAMPTZ NOT NULL DEFAULT now()
);
