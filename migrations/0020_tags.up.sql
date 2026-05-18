-- 0020_tags: per-tenant ticket labels.
--
-- Tags are simple bag-of-strings that agents stick on tickets to
-- group them by topic / product / SLA-treatment / etc. Two tables:
--
--   tags         catalogue of slugs available within a tenant
--   ticket_tags  many-to-many between tickets and tags
--
-- Slug is the lower_snake_case stable identifier; name is the human
-- label shown in chips. Colour is a 6-char hex (without #) for the
-- chip swatch -- enforced at the app layer so this column stays a
-- plain TEXT and the SQL keeps cheap to scan.

BEGIN;

CREATE TABLE tags (
  id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id   UUID NOT NULL REFERENCES tenants(id),
  slug        TEXT NOT NULL,
  name        TEXT NOT NULL,
  -- 6-char RGB hex without the leading "#"; NULL = use a default
  -- slate swatch in the UI. Validated by the app boundary; here we
  -- only constrain length so a long string can't blow out the row.
  color       TEXT,
  created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
  CONSTRAINT tags_color_len CHECK (color IS NULL OR length(color) = 6),
  CONSTRAINT tags_tenant_slug_unique UNIQUE (tenant_id, slug)
);
CREATE INDEX tags_tenant_idx ON tags (tenant_id);

CREATE TABLE ticket_tags (
  tenant_id   UUID NOT NULL REFERENCES tenants(id),
  ticket_id   UUID NOT NULL REFERENCES tickets(id) ON DELETE CASCADE,
  tag_id      UUID NOT NULL REFERENCES tags(id)    ON DELETE CASCADE,
  created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (ticket_id, tag_id)
);
-- Reverse-lookup index so "tickets with tag X" stays a single index
-- scan rather than a sequential.
CREATE INDEX ticket_tags_by_tag_idx ON ticket_tags (tenant_id, tag_id);

COMMIT;
