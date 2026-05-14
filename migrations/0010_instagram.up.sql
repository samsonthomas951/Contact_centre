-- 0010_instagram: per-tenant IG account vault + dedupe.
-- Two-token model so we can serve both Path 1 (FB-linked IG accounts,
-- legacy `instagram_*` perms) and Path 2 (direct IG login,
-- `instagram_business_*` perms). The token actually used depends on
-- which scope set was granted at onboarding -- the outbound code
-- picks the right one.

CREATE TABLE ig_accounts (
  id                       UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id                UUID NOT NULL REFERENCES tenants(id),
  ig_user_id               TEXT NOT NULL UNIQUE,        -- numeric IG user id
  username                 TEXT NOT NULL,
  flow                     TEXT NOT NULL CHECK (flow IN ('path1','path2')),
  -- Path 1: ride the linked Page's access token (FK to fb_pages).
  fb_page_id               TEXT REFERENCES fb_pages(page_id),
  -- Path 2: own access token (encrypted).
  access_token_ct          BYTEA,
  dek_id                   TEXT,
  webhook_subscribed       BOOLEAN NOT NULL DEFAULT FALSE,
  created_at               TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at               TIMESTAMPTZ NOT NULL DEFAULT now(),
  -- Exactly one of (fb_page_id, access_token_ct) must be set.
  CHECK (
    (flow = 'path1' AND fb_page_id IS NOT NULL AND access_token_ct IS NULL) OR
    (flow = 'path2' AND fb_page_id IS NULL AND access_token_ct IS NOT NULL)
  )
);
CREATE INDEX ig_accounts_tenant_idx ON ig_accounts (tenant_id);

CREATE TRIGGER ig_accounts_set_updated_at
  BEFORE UPDATE ON ig_accounts
  FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE TABLE ig_webhook_events (
  id            BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  ig_user_id    TEXT NOT NULL,
  event_id      TEXT NOT NULL,
  received_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (ig_user_id, event_id)
);
