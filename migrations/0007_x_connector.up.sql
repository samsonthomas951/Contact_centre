-- 0007_x_connector: per-tenant X account vault and AAA env mapping.
-- Mirrors fb_pages from 0006. Tokens are envelope-encrypted via Vault
-- (see §7); only the connector decrypts them.

CREATE TABLE x_accounts (
  id                       UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id                UUID NOT NULL REFERENCES tenants(id),
  account_id               TEXT NOT NULL,                 -- numeric id_str
  screen_name              TEXT NOT NULL,
  -- OAuth1 user-context tokens for AAA management calls (subscribe,
  -- list, etc.). v2 endpoints (post, reply) use OAuth2 PKCE which is
  -- stored under access_token_v2_*.
  oauth1_access_token_ct      BYTEA NOT NULL,
  oauth1_access_secret_ct     BYTEA NOT NULL,
  oauth1_dek_id               TEXT  NOT NULL,
  -- Optional v2 PKCE bearer + refresh, encrypted.
  oauth2_access_token_ct      BYTEA,
  oauth2_refresh_token_ct     BYTEA,
  oauth2_dek_id               TEXT,
  oauth2_expires_at           TIMESTAMPTZ,
  -- Account Activity environment label this account is subscribed to.
  -- One AAA env can host up to 250 subscriptions.
  aaa_env_label               TEXT NOT NULL DEFAULT 'prod',
  webhook_subscribed          BOOLEAN NOT NULL DEFAULT FALSE,
  created_at                  TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at                  TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (account_id)
);
CREATE INDEX x_accounts_tenant_idx ON x_accounts (tenant_id);

CREATE TRIGGER x_accounts_set_updated_at
  BEFORE UPDATE ON x_accounts
  FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- Webhook event dedupe (X can deliver the same mention twice when both
-- A and B are subscribed and A mentions B — see §17 "Webhook duplicates
-- / replay"). 24h-window cleanup runs as a River job.
CREATE TABLE x_webhook_events (
  id           BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  account_id   TEXT NOT NULL,
  event_id     TEXT NOT NULL,
  received_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (account_id, event_id)
);
