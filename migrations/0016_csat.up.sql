-- 0016_csat: post-resolution survey + collected scores.
-- One survey is generated per ticket on transition to 'resolved'. The
-- survey URL is sent to the customer over the same channel they used
-- to reach us (FB DM / X DM / WA / IG / widget); voice surveys go via
-- SMS where the carrier supports it.
--
-- The token is the load-bearing access control: a 16-byte random
-- value that the public collection endpoint uses to bind a vote to
-- one survey + one ticket. No subject login required (the customer
-- doesn't have an account on us).

CREATE TABLE csat_surveys (
  id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id     UUID NOT NULL REFERENCES tenants(id),
  ticket_id     UUID NOT NULL REFERENCES tickets(id),
  customer_id   UUID NOT NULL REFERENCES customers(id),
  channel       TEXT NOT NULL CHECK (channel IN ('fb','x','wa','ig','widget','voice')),
  -- Random URL-safe token, base64url(16 bytes). Issued once; the
  -- customer gets it inside the survey link; the public endpoint
  -- consumes it and stamps responded_at.
  token         TEXT NOT NULL UNIQUE,
  -- Lifetime of the link. After expires_at the public endpoint 410s.
  expires_at    TIMESTAMPTZ NOT NULL,
  sent_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
  responded_at  TIMESTAMPTZ,
  -- Score is 1..5 (CSAT) when present. NPS support is deferred to a
  -- future migration -- both fit this same table by adding nps_score
  -- and a `kind` column when needed.
  score         SMALLINT CHECK (score IS NULL OR score BETWEEN 1 AND 5),
  comment       TEXT,
  -- A vote can be revoked within the link's lifetime (rare but the
  -- DPA gives the subject the right). We keep the row, set
  -- score/comment to NULL, stamp revoked_at.
  revoked_at    TIMESTAMPTZ,
  created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX csat_surveys_ticket_idx ON csat_surveys (ticket_id);
CREATE INDEX csat_surveys_pending_idx
  ON csat_surveys (tenant_id, sent_at) WHERE responded_at IS NULL AND revoked_at IS NULL;

-- Roll responded scores into the existing analytics mart.
ALTER TABLE mart_tickets_daily
  ADD COLUMN csat_responses INTEGER NOT NULL DEFAULT 0,
  ADD COLUMN csat_avg       NUMERIC(3,2);
