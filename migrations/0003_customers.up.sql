-- 0003_customers: customer + conversation tables.
-- A `customer` is the end-user a brand interacts with. Identity across
-- channels is keyed by `external_refs` (jsonb of platform → id) so the
-- same person reaching out on FB and X can be merged later.
-- A `conversation` is one thread on one channel; multiple tickets can
-- share a conversation when a customer reopens.

CREATE TABLE customers (
  id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id       UUID NOT NULL REFERENCES tenants(id),
  external_refs   JSONB NOT NULL DEFAULT '{}'::jsonb,
  display_name    TEXT,
  email           CITEXT,
  phone           TEXT,
  created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX customers_tenant_idx       ON customers (tenant_id);
CREATE INDEX customers_external_refs_gin ON customers USING gin (external_refs jsonb_path_ops);
CREATE INDEX customers_email_idx        ON customers (tenant_id, email) WHERE email IS NOT NULL;

CREATE TRIGGER customers_set_updated_at
  BEFORE UPDATE ON customers
  FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE TABLE conversations (
  id                 UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id          UUID NOT NULL REFERENCES tenants(id),
  customer_id        UUID NOT NULL REFERENCES customers(id),
  channel            TEXT NOT NULL CHECK (channel IN ('fb','x','wa','ig','widget','voice')),
  channel_thread_id  TEXT,           -- FB thread id, X DM convo id, etc.
  created_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (tenant_id, channel, channel_thread_id)
);
CREATE INDEX conversations_customer_idx ON conversations (customer_id);
