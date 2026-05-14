-- 0001_init: extensions, helper functions, tenants table.
-- Multi-tenant by tenant_id on every business table; one Postgres database
-- with row-level isolation rather than schema-per-tenant (per §9, sharding
-- is deferred until a single tenant grows past ~50 GB).

CREATE EXTENSION IF NOT EXISTS pgcrypto;   -- gen_random_uuid()
CREATE EXTENSION IF NOT EXISTS citext;     -- case-insensitive emails

-- updated_at trigger helper used across tables.
CREATE OR REPLACE FUNCTION set_updated_at()
RETURNS TRIGGER AS $$
BEGIN
  NEW.updated_at = now();
  RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TABLE tenants (
  id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  slug            TEXT NOT NULL UNIQUE,
  display_name    TEXT NOT NULL,
  status          TEXT NOT NULL DEFAULT 'active'
                  CHECK (status IN ('active','suspended','archived')),
  -- Per §13: brands choose their retention windows within a policy band.
  retention_messages_days  INTEGER NOT NULL DEFAULT 730,   -- 24 months default
  retention_documents_days INTEGER NOT NULL DEFAULT 30,    -- after ticket close
  retention_audit_days     INTEGER NOT NULL DEFAULT 2557,  -- 7 years
  created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TRIGGER tenants_set_updated_at
  BEFORE UPDATE ON tenants
  FOR EACH ROW EXECUTE FUNCTION set_updated_at();
