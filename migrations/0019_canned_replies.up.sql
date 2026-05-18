-- 0019_canned_replies: per-tenant + per-agent saved responses.
--
-- An owner_agent_id of NULL means the row is tenant-shared (every
-- agent on the tenant can use it; only admins can edit). A non-null
-- owner_agent_id is personal (only that agent + admins see/edit).
--
-- shortcut is what the agent types after "/" in the composer; lower-
-- snake-case is the convention (refund_yes, oos_apology). We enforce
-- format at the application layer so the SQL stays cheap to query.

BEGIN;

CREATE TABLE canned_replies (
  id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id       UUID NOT NULL REFERENCES tenants(id),
  owner_agent_id  UUID REFERENCES agents(id) ON DELETE CASCADE,
  shortcut        TEXT NOT NULL,        -- "refund_status", "thanks"
  title           TEXT NOT NULL,        -- human label for the picker
  body            TEXT NOT NULL,        -- the inserted text
  -- Optional channel scoping. NULL = available on every channel.
  -- Useful for e.g. an HTML-shaped email signature that would look
  -- wrong on a WhatsApp send.
  channel         TEXT CHECK (channel IS NULL OR channel IN
                  ('fb','x','wa','ig','widget','voice','email')),
  created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
  -- Personal shadows shared when both match. Postgres treats NULL as
  -- distinct in plain UNIQUE constraints, so we use two partial
  -- unique indexes -- one for tenant-shared (owner IS NULL) and one
  -- for personal -- to actually enforce one "thanks" per scope.
  CONSTRAINT canned_replies_personal_unique UNIQUE (tenant_id, owner_agent_id, shortcut)
);
CREATE UNIQUE INDEX canned_replies_shared_unique
  ON canned_replies (tenant_id, shortcut)
  WHERE owner_agent_id IS NULL;
CREATE INDEX canned_replies_tenant_idx ON canned_replies (tenant_id);
CREATE INDEX canned_replies_lookup_idx
  ON canned_replies (tenant_id, owner_agent_id, shortcut);

CREATE TRIGGER canned_replies_set_updated_at
  BEFORE UPDATE ON canned_replies
  FOR EACH ROW EXECUTE FUNCTION set_updated_at();

COMMIT;
