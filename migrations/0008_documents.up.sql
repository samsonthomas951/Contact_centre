-- 0008_documents: object metadata + retention/holds.
-- The MinIO bucket holds the bytes; this table is the index. Per §7
-- "Documents are accessed only via presigned URLs minted by Doc Svc",
-- so direct bucket reads must be blocked at the MinIO ACL layer. The
-- audit ledger references document_id; deletes happen here AND on
-- MinIO, but the audit row stays (we log the erasure, not the data).

CREATE TABLE documents (
  id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id       UUID NOT NULL REFERENCES tenants(id),
  -- Optional ticket linkage. Documents may be uploaded before a ticket
  -- exists (widget pre-chat attachments) so this is nullable.
  ticket_id       UUID REFERENCES tickets(id) ON DELETE SET NULL,
  uploader_agent_id UUID REFERENCES agents(id),
  -- Bucket + object key under MinIO. The connector layer always writes
  -- to s3://docs/{tenant_id}/{ulid}.bin -- no human-meaningful names so
  -- a leaked URL doesn't reveal anything.
  bucket          TEXT NOT NULL,
  object_key      TEXT NOT NULL,
  filename        TEXT NOT NULL,                   -- as provided by uploader
  content_type    TEXT NOT NULL,
  size_bytes      BIGINT NOT NULL CHECK (size_bytes > 0),
  sha256          BYTEA NOT NULL,
  -- AV scan outcome. `pending` while scan runs; we never mint a URL for
  -- a `pending` doc.
  scan_status     TEXT NOT NULL DEFAULT 'pending'
                  CHECK (scan_status IN ('pending','clean','infected','scan_failed')),
  scan_signature  TEXT,                            -- ClamAV signature name when infected
  scanned_at      TIMESTAMPTZ,
  -- DEK identifier (envelope encryption per §7). Bytes themselves are
  -- encrypted at rest by MinIO SSE-S3.
  dek_id          TEXT NOT NULL,
  -- Retention scheduling. Set on insert from the tenant's
  -- retention_documents_days; nightly retention job deletes objects
  -- whose retention_until < now() AND that have no active hold.
  retention_until TIMESTAMPTZ NOT NULL,
  deleted_at      TIMESTAMPTZ,
  deleted_reason  TEXT,
  created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (bucket, object_key)
);
CREATE INDEX documents_tenant_idx           ON documents (tenant_id) WHERE deleted_at IS NULL;
CREATE INDEX documents_ticket_idx           ON documents (ticket_id) WHERE ticket_id IS NOT NULL;
CREATE INDEX documents_retention_idx        ON documents (retention_until) WHERE deleted_at IS NULL;
CREATE INDEX documents_scan_pending_idx     ON documents (scan_status, created_at) WHERE scan_status = 'pending';

-- Legal/case holds suspend retention. A doc with any active hold is
-- skipped by the retention reaper. Holds are append-only audit-grade
-- records; releasing one inserts a release row rather than UPDATE-ing.
CREATE TABLE document_holds (
  id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  document_id   UUID NOT NULL REFERENCES documents(id) ON DELETE CASCADE,
  reason        TEXT NOT NULL,                    -- e.g. 'subpoena', 'regulator_query'
  ref           TEXT,                             -- case number, ticket reference
  placed_by     UUID REFERENCES agents(id),
  placed_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
  released_at   TIMESTAMPTZ
);
CREATE INDEX document_holds_active_idx
  ON document_holds (document_id) WHERE released_at IS NULL;

-- Helper view: documents that the retention reaper may delete tonight.
CREATE VIEW documents_purgeable AS
  SELECT d.*
  FROM documents d
  LEFT JOIN document_holds h
    ON h.document_id = d.id AND h.released_at IS NULL
  WHERE d.deleted_at IS NULL
    AND d.retention_until < now()
    AND h.id IS NULL;
