-- 0015_webhooks: tenant-managed outbound webhook subscriptions.
-- Lets brands subscribe to NATS events (ticket.*, message.*, sla.*)
-- and receive HMAC-signed POSTs to their own URLs. The delivery log
-- is the audit trail for every retry attempt.

CREATE TABLE webhook_subscriptions (
  id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id       UUID NOT NULL REFERENCES tenants(id),
  url             TEXT NOT NULL,
  -- Per-subscription HMAC secret. Encrypted at rest (envelope DEK
  -- per §7); the brand also holds the plaintext, so a leak forces
  -- rotation rather than re-issuance.
  secret_ct       BYTEA NOT NULL,
  dek_id          TEXT  NOT NULL,
  -- Subject patterns to match against. NATS-style wildcards: e.g.
  -- 'ticket.*' or 'sla.first_response.>'. Empty array means "everything".
  subjects        TEXT[] NOT NULL DEFAULT '{}',
  active          BOOLEAN NOT NULL DEFAULT TRUE,
  -- Auto-disable after N consecutive failures so a deleted endpoint
  -- doesn't burn our retry budget forever.
  consecutive_failures  INTEGER NOT NULL DEFAULT 0,
  disabled_at           TIMESTAMPTZ,
  disabled_reason       TEXT,
  description     TEXT,
  created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX webhook_subscriptions_tenant_active_idx
  ON webhook_subscriptions (tenant_id) WHERE active;

CREATE TRIGGER webhook_subscriptions_set_updated_at
  BEFORE UPDATE ON webhook_subscriptions
  FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- One row per delivery attempt (NOT per event). The delivery worker
-- inserts on every attempt; retries land here with attempt > 1.
CREATE TABLE webhook_deliveries (
  id              BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  subscription_id UUID NOT NULL REFERENCES webhook_subscriptions(id) ON DELETE CASCADE,
  event_id        TEXT NOT NULL,                  -- NATS msg id (correlation_id-derived)
  subject         TEXT NOT NULL,                  -- the matched NATS subject
  attempt         SMALLINT NOT NULL,              -- 1..MaxAttempts
  status_code     INTEGER,                        -- HTTP status returned, NULL on transport err
  error           TEXT,                           -- transport / timeout error
  -- Successful delivery: 2xx. Permanent failure: 4xx (except 408/429).
  -- Transient: 5xx, 408, 429, transport err -> retry with backoff.
  delivered       BOOLEAN NOT NULL DEFAULT FALSE,
  duration_ms     INTEGER,
  at              TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX webhook_deliveries_sub_idx ON webhook_deliveries (subscription_id, at DESC);
CREATE INDEX webhook_deliveries_event_idx ON webhook_deliveries (event_id);
