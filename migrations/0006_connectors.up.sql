-- 0006_connectors: per-channel token vaults.
-- Tokens are stored as ciphertext (BYTEA); the connector decrypts via
-- an envelope DEK held in HashiCorp Vault per §7. The plaintext token
-- is never logged and never leaves the connector process.

CREATE TABLE fb_pages (
  id                    UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id             UUID NOT NULL REFERENCES tenants(id),
  page_id               TEXT NOT NULL,
  page_name             TEXT NOT NULL,
  -- AES-GCM ciphertext of the long-lived Page access token. Per Meta
  -- docs these "do not have an expiration date" — rotation is incident-
  -- driven, see §17 risk register.
  access_token_ct       BYTEA NOT NULL,
  access_token_dek_id   TEXT  NOT NULL,
  webhook_subscribed    BOOLEAN NOT NULL DEFAULT FALSE,
  subscribed_fields     TEXT[] NOT NULL DEFAULT
                        '{messages,messaging_postbacks,message_reads,message_deliveries,feed,mention}',
  created_at            TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at            TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (page_id)
);
CREATE INDEX fb_pages_tenant_idx ON fb_pages (tenant_id);

CREATE TRIGGER fb_pages_set_updated_at
  BEFORE UPDATE ON fb_pages
  FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- Webhook event dedupe (Meta retries 5xx — we want exactly-once into NATS).
CREATE TABLE fb_webhook_events (
  id           BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  page_id      TEXT NOT NULL,
  event_id     TEXT NOT NULL,         -- platform message id or composite
  received_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (page_id, event_id)
);
-- Cleanup older than 24 h is handled by a River job; matches the Redis
-- 24 h dedupe SET in §17 ("Webhook duplicates / replay").
