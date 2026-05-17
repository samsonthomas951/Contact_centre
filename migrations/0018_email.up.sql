-- 0018_email: email as a first-class channel.
--
-- One mailbox per tenant per address. Tokens (SMTP password, IMAP
-- password, Mailgun webhook signing key) are envelope-encrypted via
-- the same DEK pattern as fb_pages / wa_phone_numbers. Inbound goes
-- either via the webhook (Mailgun / Postmark / SES → /v1/email/webhook)
-- or the IMAP poller; outbound is always SMTP.
--
-- Threading: standard RFC 5322 In-Reply-To / References. Inbound
-- normalisation looks up our messages.platform_message_id for the
-- supplied In-Reply-To and reuses the same conversation when found;
-- conversation_key falls back to the customer email address.

BEGIN;

-- Widen the channel CHECK on every table that pins the enum. Postgres
-- requires DROP + ADD; cheaper than carrying a real enum type because
-- adding values to TYPE-enums isn't transactional pre-PG14 and is
-- still finicky.
ALTER TABLE conversations DROP CONSTRAINT conversations_channel_check;
ALTER TABLE conversations ADD CONSTRAINT conversations_channel_check
  CHECK (channel IN ('fb','x','wa','ig','widget','voice','email'));

ALTER TABLE mart_tickets_daily DROP CONSTRAINT mart_tickets_daily_channel_check;
ALTER TABLE mart_tickets_daily ADD CONSTRAINT mart_tickets_daily_channel_check
  CHECK (channel IN ('fb','x','wa','ig','widget','voice','email','all'));

ALTER TABLE csat_surveys DROP CONSTRAINT csat_surveys_channel_check;
ALTER TABLE csat_surveys ADD CONSTRAINT csat_surveys_channel_check
  CHECK (channel IN ('fb','x','wa','ig','widget','voice','email'));

CREATE TABLE email_mailboxes (
  id                       UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id                UUID NOT NULL REFERENCES tenants(id),
  address                  CITEXT NOT NULL,                -- support@acme.co.ke
  display_name             TEXT  NOT NULL,                 -- "Acme Support"
  -- Outbound: SMTP.
  smtp_host                TEXT  NOT NULL,
  smtp_port                INT   NOT NULL,
  smtp_username            TEXT  NOT NULL,
  smtp_password_ct         BYTEA NOT NULL,
  smtp_password_dek_id     TEXT  NOT NULL,
  -- Inbound option A: IMAP poller. NULLable so a webhook-only mailbox
  -- can leave them empty.
  imap_host                TEXT,
  imap_port                INT,
  imap_username            TEXT,
  imap_password_ct         BYTEA,
  imap_password_dek_id     TEXT,
  imap_last_seen_uid       BIGINT NOT NULL DEFAULT 0,      -- UIDVALIDITY/UID checkpoint
  -- Inbound option B: webhook. Provider posts to /v1/email/webhook
  -- with HMAC-signed body. NULL = webhook disabled.
  webhook_signing_key_ct   BYTEA,
  webhook_signing_key_dek_id TEXT,
  active                   BOOLEAN NOT NULL DEFAULT TRUE,
  created_at               TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at               TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (tenant_id, address)
);
CREATE INDEX email_mailboxes_tenant_idx ON email_mailboxes (tenant_id) WHERE active;
CREATE INDEX email_mailboxes_address_idx ON email_mailboxes (address)  WHERE active;

CREATE TRIGGER email_mailboxes_set_updated_at
  BEFORE UPDATE ON email_mailboxes
  FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- Webhook dedupe -- providers retry on 5xx, so we keep (mailbox_id,
-- message_id) for 24h to silently ack duplicates instead of re-
-- ingesting. Cleanup runs on the same River job as fb_webhook_events.
CREATE TABLE email_webhook_events (
  id           BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  mailbox_id   UUID NOT NULL REFERENCES email_mailboxes(id) ON DELETE CASCADE,
  message_id   TEXT NOT NULL,                 -- RFC 5322 Message-ID header
  received_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (mailbox_id, message_id)
);

-- Outbound delivery log keyed on the Message-ID we generated, so a
-- bounce notification fired by the provider hours later can be matched
-- back to the original send.
CREATE TABLE email_outbound_log (
  id           BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  tenant_id    UUID NOT NULL REFERENCES tenants(id),
  ticket_id    UUID REFERENCES tickets(id),
  mailbox_id   UUID NOT NULL REFERENCES email_mailboxes(id),
  message_id   TEXT NOT NULL,                 -- our generated Message-ID
  bounced      BOOLEAN NOT NULL DEFAULT FALSE,
  bounce_kind  TEXT,                          -- 'soft' | 'hard' | NULL
  bounce_reason TEXT,
  sent_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (message_id)
);
CREATE INDEX email_outbound_log_ticket_idx ON email_outbound_log (ticket_id) WHERE ticket_id IS NOT NULL;

COMMIT;
