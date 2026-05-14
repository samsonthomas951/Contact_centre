-- 0012_dsr: Data Subject Request tracking.
-- Implements the DPA s.26 / s.40 30-day response window. Every request
-- carries a state machine (received -> in_progress -> fulfilled |
-- partially_fulfilled | rejected | withdrawn) and a hard due_at column
-- the dashboard sorts by.

CREATE TYPE dsr_kind AS ENUM (
  'access',         -- s.26(1)(a) — copy of held data
  'rectification',  -- s.26(1)(b) — fix incorrect data
  'erasure',        -- s.40 — right to be forgotten
  'restriction',    -- s.26(1)(e)
  'portability',    -- s.26(1)(d)
  'objection'       -- s.26(1)(c) — stop further processing
);

CREATE TYPE dsr_state AS ENUM (
  'received', 'in_progress', 'fulfilled',
  'partially_fulfilled', 'rejected', 'withdrawn'
);

CREATE TABLE dsr_requests (
  id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id           UUID NOT NULL REFERENCES tenants(id),
  -- Optional internal customer linkage. End-customer requests come via
  -- the brand and identify the subject by external_ref; agents-as-
  -- subjects identify by agent_id.
  customer_id         UUID REFERENCES customers(id),
  subject_email       CITEXT,
  subject_phone       TEXT,
  subject_external_ref TEXT,                       -- e.g. fb PSID
  kind                dsr_kind NOT NULL,
  state               dsr_state NOT NULL DEFAULT 'received',
  reason              TEXT,                        -- subject-supplied narrative
  -- Statutory 30-day clock starts at received_at; due_at is set to
  -- received_at + 30 days unless an extension is granted.
  received_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
  due_at              TIMESTAMPTZ NOT NULL,
  fulfilled_at        TIMESTAMPTZ,
  -- Operator action for the audit ledger; required when state=rejected.
  resolution_note     TEXT,
  assigned_to_agent_id UUID REFERENCES agents(id),
  created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
  CHECK (
    customer_id IS NOT NULL
    OR subject_email IS NOT NULL
    OR subject_phone IS NOT NULL
    OR subject_external_ref IS NOT NULL
  ),
  CHECK (state <> 'rejected' OR resolution_note IS NOT NULL)
);
CREATE INDEX dsr_requests_tenant_open_idx
  ON dsr_requests (tenant_id, due_at)
  WHERE state IN ('received','in_progress');
CREATE INDEX dsr_requests_subject_email_idx
  ON dsr_requests (tenant_id, subject_email)
  WHERE subject_email IS NOT NULL;

CREATE TRIGGER dsr_requests_set_updated_at
  BEFORE UPDATE ON dsr_requests
  FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- Per-request action log. Each step (verification, extract, deletion,
-- notification) writes a row so the DPO can reconstruct the fulfilment
-- trail. Append-only at the API layer; we keep it as a normal table
-- here for ease, then audit_events carries the durable tamper-evident
-- copy.
CREATE TABLE dsr_actions (
  id            BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  request_id    UUID NOT NULL REFERENCES dsr_requests(id) ON DELETE CASCADE,
  actor_agent_id UUID REFERENCES agents(id),
  kind          TEXT NOT NULL,                     -- e.g. 'verify_identity','extract','delete_documents'
  note          TEXT,
  at            TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX dsr_actions_request_idx ON dsr_actions (request_id, at DESC);
