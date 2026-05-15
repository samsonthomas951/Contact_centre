-- 0017_outbound_log: demo / dev table that the outbound-stub worker
-- writes to instead of calling Meta / X / WhatsApp / Instagram. Lets
-- the demo show a reply going "out" without real platform credentials.
-- Production removes this table and replaces the stub with the per-
-- channel Sender (facebook.SendText, etc.).

CREATE TABLE outbound_log (
  id            BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  tenant_id     UUID NOT NULL REFERENCES tenants(id),
  ticket_id     UUID REFERENCES tickets(id),
  customer_id   UUID REFERENCES customers(id),
  channel       TEXT NOT NULL,
  kind          TEXT NOT NULL,                  -- e.g. agent_reply | csat_survey
  body          TEXT NOT NULL,
  shipped_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX outbound_log_tenant_idx ON outbound_log (tenant_id, shipped_at DESC);
CREATE INDEX outbound_log_ticket_idx ON outbound_log (ticket_id) WHERE ticket_id IS NOT NULL;
