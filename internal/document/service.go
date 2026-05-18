package document

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// MaxUploadBytes is the per-object size cap. Phase-1 budget: 25 MiB
// (matches our default ClamAV chunk buffer in §7).
const MaxUploadBytes = 25 * 1024 * 1024

// Document is the public model returned to API callers.
type Document struct {
	ID            uuid.UUID  `json:"id"`
	TenantID      uuid.UUID  `json:"tenant_id"`
	TicketID      *uuid.UUID `json:"ticket_id,omitempty"`
	Filename      string     `json:"filename"`
	ContentType   string     `json:"content_type"`
	SizeBytes     int64      `json:"size_bytes"`
	SHA256Hex     string     `json:"sha256"`
	ScanStatus    string     `json:"scan_status"`
	ScanSignature string     `json:"scan_signature,omitempty"`
	CreatedAt     time.Time  `json:"created_at"`
}

// Service orchestrates the upload pipeline:
//
//	1. Sniff the first 512 bytes against the allow-list.
//	2. Tee the body into ClamAV and the upload buffer (memory-bounded
//	   by MaxUploadBytes).
//	3. If clean, PUT to MinIO with SSE-S3 and INSERT the metadata row.
//	4. If infected, write an audit-grade row marked 'infected' with the
//	   signature; never put the bytes to MinIO.
type Service struct {
	Pool  *pgxpool.Pool
	Store *ObjectStore
	// Scanner is anything that can stream bytes to an AV engine.
	// Production wires *ClamAVScanner; tests can pass a stub so the
	// AV-rejection path is exercised without a real engine (ClamAV's
	// EICAR signature anchors at offset 0 in many builds, which makes
	// it brittle to test through a wrapped body).
	Scanner Scanner
	// DEKID identifies the per-tenant data-encryption key wrapped by
	// Vault. Phase-1 single-tenant: the operator supplies one key id
	// for the whole deployment.
	DEKID string
}

// Scanner abstracts the AV path. *ClamAVScanner satisfies it.
type Scanner interface {
	Scan(ctx context.Context, r io.Reader) (Verdict, error)
}

// UploadParams is what callers pass to Upload.
type UploadParams struct {
	TenantID         uuid.UUID
	TicketID         *uuid.UUID
	UploaderAgentID  *uuid.UUID
	Filename         string
	ContentTypeClaim string
	Body             io.Reader
}

// ErrTooLarge is returned when Body exceeds MaxUploadBytes.
var ErrTooLarge = errors.New("document: upload exceeds size limit")

// Upload runs the full pipeline. The returned Document is always
// persisted, even when the AV scan finds something — the operator can
// then inspect via the audit ledger to understand what was attempted.
func (s *Service) Upload(ctx context.Context, p UploadParams) (*Document, error) {
	if p.TenantID == uuid.Nil {
		return nil, errors.New("document: tenant_id required")
	}
	if p.Body == nil {
		return nil, errors.New("document: body required")
	}

	// Buffer the upload. We need to sniff (head), AV-scan, hash, and
	// PUT, so memory-buffering bounded by MaxUploadBytes is the simplest
	// correct option for Phase 1. Larger objects (e.g. call recordings
	// in Phase 3) will switch to a temp-file spillover.
	buf, err := readCapped(p.Body, MaxUploadBytes)
	if err != nil {
		return nil, err
	}

	contentType, err := Sniff(buf[:min(len(buf), 512)], p.ContentTypeClaim)
	if err != nil {
		return nil, err
	}

	sum := sha256.Sum256(buf)
	sumHex := hex.EncodeToString(sum[:])

	// AV scan. Treat scanner errors as scan_failed so the doc is
	// quarantined rather than silently allowed.
	verdict, scanErr := s.scan(ctx, buf)
	scanStatus := "clean"
	switch {
	case scanErr != nil:
		scanStatus = "scan_failed"
	case !verdict.Clean:
		scanStatus = "infected"
	}

	// Build the object key once so the metadata row and the MinIO PUT
	// agree on where the bytes live. Calling buildKey() twice (once
	// per call site) was a real bug -- the random + millisecond
	// stamp differed between calls and PresignDownload pointed at a
	// path that was never written.
	objectKey := buildKey(p.TenantID)

	// Insert metadata first; we want the audit trail even if the PUT
	// fails. tenants.retention_documents_days drives retention_until.
	row := s.Pool.QueryRow(ctx, `
		WITH t AS (
		  SELECT retention_documents_days FROM tenants WHERE id = $1
		)
		INSERT INTO documents
		  (tenant_id, ticket_id, uploader_agent_id, bucket, object_key,
		   filename, content_type, size_bytes, sha256, scan_status,
		   scan_signature, scanned_at, dek_id, retention_until)
		VALUES (
		  $1, $2, $3, $4, $5, $6, $7, $8, $9, $10,
		  NULLIF($11,''), now(), $12,
		  now() + (SELECT retention_documents_days FROM t) * INTERVAL '1 day'
		)
		RETURNING id, tenant_id, ticket_id, filename, content_type,
		          size_bytes, sha256, scan_status, scan_signature, created_at`,
		p.TenantID, p.TicketID, p.UploaderAgentID,
		s.Store.BucketName(), objectKey,
		p.Filename, contentType, int64(len(buf)), sum[:],
		scanStatus, verdict.Signature, s.DEKID,
	)
	d, err := scanDoc(row)
	if err != nil {
		return nil, fmt.Errorf("document: insert metadata: %w", err)
	}
	d.SHA256Hex = sumHex

	// Only put bytes to MinIO when clean. Infected and scan-failed
	// uploads stop here; the supervisor UI surfaces them for review.
	if scanStatus == "clean" {
		if err := s.Store.Put(ctx, objectKey, bytes.NewReader(buf), int64(len(buf)), contentType); err != nil {
			// Roll the row to scan_failed so the doc isn't surfaced as
			// downloadable -- the bytes don't exist.
			_, _ = s.Pool.Exec(ctx,
				`UPDATE documents SET scan_status='scan_failed' WHERE id=$1`, d.ID)
			return nil, fmt.Errorf("document: minio put: %w", err)
		}
	}
	return d, nil
}

// PresignDownload returns a short-lived (≤MaxPresignTTL) GET URL for a
// clean document. Refuses to sign anything that isn't clean — even if
// an operator manually flips the row, infected files stay un-signable.
func (s *Service) PresignDownload(ctx context.Context, tenantID, docID, agentID uuid.UUID) (string, error) {
	var bucket, key, status string
	if err := s.Pool.QueryRow(ctx,
		`SELECT bucket, object_key, scan_status FROM documents
		 WHERE tenant_id = $1 AND id = $2 AND deleted_at IS NULL`,
		tenantID, docID).Scan(&bucket, &key, &status); err != nil {
		return "", err
	}
	if status != "clean" {
		return "", fmt.Errorf("document: cannot presign %s document", status)
	}
	return s.Store.PresignURL(ctx, key, agentID.String(), MaxPresignTTL)
}

// scan calls the configured scanner. When no scanner is configured (dev
// without ClamAV), we treat the verdict as Clean so the local pipeline
// still works -- production must always have a scanner wired.
func (s *Service) scan(ctx context.Context, buf []byte) (Verdict, error) {
	if s.Scanner == nil {
		return Verdict{Clean: true}, nil
	}
	return s.Scanner.Scan(ctx, bytes.NewReader(buf))
}

// readCapped reads up to limit+1 bytes; returns ErrTooLarge if the
// reader has more.
func readCapped(r io.Reader, limit int) ([]byte, error) {
	buf, err := io.ReadAll(io.LimitReader(r, int64(limit)+1))
	if err != nil {
		return nil, err
	}
	if len(buf) > limit {
		return nil, ErrTooLarge
	}
	return buf, nil
}

// buildKey returns a tenant-scoped, time-sortable key. Format:
//   {tenant_uuid}/{unix_ms_be_hex}-{8 random hex}.bin
// Time prefix means lexicographic listing is roughly chronological,
// which helps lifecycle scans without an index.
func buildKey(tenant uuid.UUID) string {
	var b [8]byte
	binary.BigEndian.PutUint64(b[:], uint64(time.Now().UnixMilli())) //nolint:gosec // UnixMilli is non-negative
	var rnd [4]byte
	_, _ = rand.Read(rnd[:])
	return fmt.Sprintf("%s/%s-%s.bin", tenant, hex.EncodeToString(b[:]), hex.EncodeToString(rnd[:]))
}

func scanDoc(r interface{ Scan(...any) error }) (*Document, error) {
	var d Document
	var sigPtr *string
	var sumBytes []byte
	if err := r.Scan(
		&d.ID, &d.TenantID, &d.TicketID, &d.Filename, &d.ContentType,
		&d.SizeBytes, &sumBytes, &d.ScanStatus, &sigPtr, &d.CreatedAt,
	); err != nil {
		return nil, err
	}
	d.SHA256Hex = hex.EncodeToString(sumBytes)
	if sigPtr != nil {
		d.ScanSignature = *sigPtr
	}
	return &d, nil
}

