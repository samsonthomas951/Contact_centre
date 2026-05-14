-- 0009_whatsapp: per-tenant WA Business phone-number vault + dedupe.
-- Mirrors fb_pages (0006) and x_accounts (0007). System access token
-- is a long-lived "System User" token bound to the WhatsApp Business
-- Account; rotation is operator-driven via the onboarding flow.

CREATE TABLE wa_phone_numbers (
  id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id           UUID NOT NULL REFERENCES tenants(id),
  waba_id             TEXT NOT NULL,                 -- WhatsApp Business Account id
  phone_number_id     TEXT NOT NULL UNIQUE,          -- the value Meta gives us
  display_phone_number TEXT NOT NULL,                -- E.164 with "+"
  -- AES-GCM ciphertext of the system access token. See §7 envelope
  -- encryption -- decryption key id stored under dek_id.
  access_token_ct     BYTEA NOT NULL,
  dek_id              TEXT  NOT NULL,
  webhook_subscribed  BOOLEAN NOT NULL DEFAULT FALSE,
  created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at          TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX wa_phone_numbers_tenant_idx ON wa_phone_numbers (tenant_id);

CREATE TRIGGER wa_phone_numbers_set_updated_at
  BEFORE UPDATE ON wa_phone_numbers
  FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- Template registry. Each row is a Meta-approved message template. The
-- agent UI selects from approved templates when the 24h customer
-- window is closed; the outbound code path inserts variable values
-- into {{1}}, {{2}} ... placeholders.
CREATE TABLE wa_templates (
  id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  phone_number_id     TEXT NOT NULL REFERENCES wa_phone_numbers(phone_number_id) ON DELETE CASCADE,
  name                TEXT NOT NULL,
  language            TEXT NOT NULL DEFAULT 'en',
  category            TEXT NOT NULL CHECK (category IN ('MARKETING','UTILITY','AUTHENTICATION')),
  status              TEXT NOT NULL CHECK (status IN ('APPROVED','PENDING','REJECTED','PAUSED','DISABLED')),
  body                TEXT NOT NULL,                 -- with {{n}} placeholders
  variable_count      SMALLINT NOT NULL DEFAULT 0,
  approved_at         TIMESTAMPTZ,
  created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (phone_number_id, name, language)
);
CREATE INDEX wa_templates_approved_idx
  ON wa_templates (phone_number_id, category) WHERE status = 'APPROVED';

CREATE TRIGGER wa_templates_set_updated_at
  BEFORE UPDATE ON wa_templates
  FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- Dedupe table (same shape as fb_webhook_events and x_webhook_events).
CREATE TABLE wa_webhook_events (
  id           BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  phone_number_id TEXT NOT NULL,
  event_id     TEXT NOT NULL,                        -- wamid
  received_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (phone_number_id, event_id)
);
