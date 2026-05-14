-- 0014_voice: per-tenant voice numbers + call records.
-- Africa's Talking is the Phase-3 default carrier (Kenyan-incorporated,
-- supports local DIDs, USSD and SIP termination -- see §14). Twilio and
-- self-hosted FreeSWITCH plug in via the same `voice_numbers.provider`
-- field; the connector dispatches per provider.

CREATE TABLE voice_numbers (
  id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id           UUID NOT NULL REFERENCES tenants(id),
  provider            TEXT NOT NULL CHECK (provider IN ('africastalking','twilio','freeswitch')),
  number              TEXT NOT NULL UNIQUE,            -- E.164 with leading "+"
  display_name        TEXT NOT NULL,
  -- Webhook secret for inbound-call verification. AT uses a shared
  -- secret in the URL path (we keep it here so the connector can
  -- rotate it without redeploying).
  webhook_secret_ct   BYTEA NOT NULL,
  dek_id              TEXT  NOT NULL,
  active              BOOLEAN NOT NULL DEFAULT TRUE,
  -- Africa's Talking publishes a recording URL on hangup. We mirror
  -- this into the Document Service (stored in MinIO, scanned by
  -- ClamAV like any other upload).
  recording_enabled   BOOLEAN NOT NULL DEFAULT TRUE,
  created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at          TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX voice_numbers_tenant_idx ON voice_numbers (tenant_id);

CREATE TRIGGER voice_numbers_set_updated_at
  BEFORE UPDATE ON voice_numbers
  FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- One row per call leg. The Voice Connector maintains state here and
-- raises a ticket of channel='voice' on first connect.
CREATE TYPE voice_call_state AS ENUM (
  'ringing', 'in_progress', 'completed', 'failed', 'no_answer', 'busy'
);

CREATE TABLE voice_calls (
  id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id           UUID NOT NULL REFERENCES tenants(id),
  voice_number_id     UUID NOT NULL REFERENCES voice_numbers(id),
  -- Carrier session id (AT sessionId, Twilio CallSid, etc.). Used to
  -- dedupe webhook events for the same call.
  session_id          TEXT NOT NULL,
  -- Direction from the brand's perspective.
  direction           TEXT NOT NULL CHECK (direction IN ('inbound','outbound')),
  caller              TEXT NOT NULL,                   -- E.164
  callee              TEXT NOT NULL,
  state               voice_call_state NOT NULL DEFAULT 'ringing',
  -- Recording consent captured via IVR DTMF (e.g. "press 1 to consent").
  -- Kenya DPA s.30/s.45 requires explicit consent before recording.
  -- When recording_enabled=true and consent_dtmf=NULL, the call is
  -- routed to a consent IVR before connecting to an agent.
  consent_dtmf        CHAR(1),
  consent_at          TIMESTAMPTZ,
  -- Optional ticket linkage. Created on first connect; carries audit
  -- and analytics into the same ticket lifecycle as other channels.
  ticket_id           UUID REFERENCES tickets(id),
  -- Recording document, present once the call ends and we've ingested
  -- the carrier-supplied URL through Doc Svc.
  recording_doc_id    UUID REFERENCES documents(id),
  recording_url       TEXT,                            -- carrier URL prior to ingest
  duration_seconds    INTEGER,
  started_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
  answered_at         TIMESTAMPTZ,
  ended_at            TIMESTAMPTZ,
  UNIQUE (voice_number_id, session_id)
);
CREATE INDEX voice_calls_tenant_idx       ON voice_calls (tenant_id, started_at DESC);
CREATE INDEX voice_calls_ticket_idx       ON voice_calls (ticket_id) WHERE ticket_id IS NOT NULL;
CREATE INDEX voice_calls_active_idx       ON voice_calls (tenant_id) WHERE state IN ('ringing','in_progress');
