//go:build integration

package document

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// pdfBody is the smallest valid PDF the stdlib detector recognises as
// application/pdf. Magic header + minimal trailer.
var pdfBody = []byte("%PDF-1.7\n%mock\n1 0 obj<<>>endobj\nxref\n0 1\ntrailer<<>>\n%%EOF")

func envOr(t *testing.T, name string) string {
	t.Helper()
	v := os.Getenv(name)
	if v == "" {
		t.Skipf("%s not set", name)
	}
	return v
}

func freshPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	url := envOr(t, "DOCUMENT_TEST_DB_URL")
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	t.Cleanup(pool.Close)
	if _, err := pool.Exec(ctx,
		`DROP SCHEMA IF EXISTS public CASCADE; CREATE SCHEMA public;`); err != nil {
		t.Fatalf("reset: %v", err)
	}
	_, file, _, _ := runtime.Caller(0)
	root := filepath.Join(filepath.Dir(file), "..", "..", "migrations")
	for _, name := range []string{"0001_init", "0002_agents", "0003_customers", "0004_tickets", "0008_documents"} {
		b, err := os.ReadFile(filepath.Join(root, name+".up.sql"))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		if _, err := pool.Exec(ctx, string(b)); err != nil {
			t.Fatalf("apply %s: %v", name, err)
		}
	}
	return pool
}

func freshStore(t *testing.T) *ObjectStore {
	t.Helper()
	endpoint := envOr(t, "DOCUMENT_TEST_MINIO_ENDPOINT")
	access := envOr(t, "DOCUMENT_TEST_MINIO_ACCESS_KEY")
	secret := envOr(t, "DOCUMENT_TEST_MINIO_SECRET_KEY")

	ctx := context.Background()
	store, err := NewObjectStore(ctx, MinioConfig{
		Endpoint: endpoint, AccessKey: access, SecretKey: secret,
		Bucket: "doctest", UseSSL: false,
	})
	if err != nil {
		t.Fatalf("minio: %v", err)
	}
	return store
}

func freshScanner(t *testing.T) *ClamAVScanner {
	t.Helper()
	addr := envOr(t, "DOCUMENT_TEST_CLAMAV_ADDR")
	return &ClamAVScanner{Addr: addr, DialTimeout: 5 * time.Second, OperationTimeout: 30 * time.Second}
}

func seedTenant(t *testing.T, ctx context.Context, pool *pgxpool.Pool) uuid.UUID {
	t.Helper()
	id := uuid.New()
	if _, err := pool.Exec(ctx,
		`INSERT INTO tenants(id, slug, display_name) VALUES($1,'acme','Acme')`,
		id); err != nil {
		t.Fatal(err)
	}
	return id
}

// stubInfectedScanner is the test seam for the AV-rejection path.
// ClamAV's EICAR signature anchors at offset 0 in many builds, which
// would force the test to send raw EICAR -- but Sniff() rightly
// rejects EICAR's text/plain content type. Splitting concerns keeps
// each test honest: the Clean path proves the live ClamAV wire
// works; the Infected path proves Service handles a !Clean verdict
// correctly.
type stubInfectedScanner struct{ sig string }

func (s stubInfectedScanner) Scan(_ context.Context, r io.Reader) (Verdict, error) {
	_, _ = io.Copy(io.Discard, r) // drain so the upstream LimitReader behaves
	return Verdict{Clean: false, Signature: s.sig, Raw: "stream: " + s.sig + " FOUND"}, nil
}

func TestService_Upload_CleanPDFEndToEnd(t *testing.T) {
	ctx := context.Background()
	pool := freshPool(t)
	store := freshStore(t)
	scanner := freshScanner(t) // real ClamAV

	tenant := seedTenant(t, ctx, pool)
	svc := &Service{Pool: pool, Store: store, Scanner: scanner, DEKID: "test-dek"}

	d, err := svc.Upload(ctx, UploadParams{
		TenantID:         tenant,
		Filename:         "receipt.pdf",
		ContentTypeClaim: "application/pdf",
		Body:             bytes.NewReader(pdfBody),
	})
	if err != nil {
		t.Fatalf("upload: %v", err)
	}
	if d.ScanStatus != "clean" {
		t.Fatalf("scan_status = %q, want clean", d.ScanStatus)
	}
	if d.ContentType != "application/pdf" {
		t.Errorf("content_type = %q, want application/pdf", d.ContentType)
	}
	if d.SizeBytes != int64(len(pdfBody)) {
		t.Errorf("size = %d, want %d", d.SizeBytes, len(pdfBody))
	}

	// Presigned download round-trip: GET the signed URL, confirm bytes
	// match the source. Proves the SSE-S3 PUT + presign flow end-to-end.
	url, err := svc.PresignDownload(ctx, tenant, d.ID, uuid.New())
	if err != nil {
		t.Fatalf("presign: %v", err)
	}
	res, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET signed url: %v", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(res.Body)
		t.Fatalf("signed GET status=%d body=%s", res.StatusCode, body)
	}
	got, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, pdfBody) {
		t.Errorf("downloaded bytes != uploaded (got %d, want %d)", len(got), len(pdfBody))
	}
}

func TestService_Upload_InfectedRowSurfacesRefusesPresign(t *testing.T) {
	// Stub scanner so we deterministically exercise the Service's
	// !verdict.Clean branch without depending on ClamAV's offset-0
	// EICAR matching behaviour.
	ctx := context.Background()
	pool := freshPool(t)
	store := freshStore(t)
	tenant := seedTenant(t, ctx, pool)

	svc := &Service{
		Pool: pool, Store: store, DEKID: "test-dek",
		Scanner: stubInfectedScanner{sig: "Test.Signature.42"},
	}

	d, err := svc.Upload(ctx, UploadParams{
		TenantID:         tenant,
		Filename:         "evil.pdf",
		ContentTypeClaim: "application/pdf",
		Body:             bytes.NewReader(pdfBody),
	})
	if err != nil {
		t.Fatalf("upload (should not error -- AV result rides on the row): %v", err)
	}
	if d.ScanStatus != "infected" {
		t.Fatalf("scan_status = %q, want infected", d.ScanStatus)
	}
	if d.ScanSignature != "Test.Signature.42" {
		t.Errorf("scan_signature = %q, want Test.Signature.42", d.ScanSignature)
	}

	// Presign refuses non-clean documents even with a real bucket.
	if _, err := svc.PresignDownload(ctx, tenant, d.ID, uuid.New()); err == nil {
		t.Error("PresignDownload should refuse infected docs")
	}
}

func TestService_Upload_RejectsDisallowedContentType(t *testing.T) {
	ctx := context.Background()
	pool := freshPool(t)
	store := freshStore(t)
	scanner := freshScanner(t)
	tenant := seedTenant(t, ctx, pool)

	svc := &Service{Pool: pool, Store: store, Scanner: scanner, DEKID: "test-dek"}
	pngHeader := []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n', 'p', 'a', 'd'}

	_, err := svc.Upload(ctx, UploadParams{
		TenantID:         tenant,
		Filename:         "img.png",
		ContentTypeClaim: "image/png",
		Body:             bytes.NewReader(pngHeader),
	})
	if !errors.Is(err, ErrContentTypeNotAllowed) {
		t.Fatalf("err = %v, want ErrContentTypeNotAllowed", err)
	}

	// And no document row was written for the rejected upload.
	var n int
	if err := pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM documents WHERE tenant_id = $1`, tenant).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("rejected upload created %d rows, want 0", n)
	}
}
