package document

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	"github.com/minio/minio-go/v7/pkg/encrypt"
)

// ObjectStore wraps a MinIO client. The bucket layout is one bucket per
// deployment ("docs"); per-tenant prefixing keeps a single bucket
// manageable for Phase-1 single-VPS scale and avoids the per-bucket
// overhead at thousands-of-tenants scale.
type ObjectStore struct {
	cli    *minio.Client
	bucket string
}

// MinioConfig is what the document service loads at startup.
type MinioConfig struct {
	Endpoint        string `env:"MINIO_ENDPOINT" required:"true"` // e.g. "minio:9000"
	AccessKey       string `env:"MINIO_ACCESS_KEY" required:"true"`
	SecretKey       string `env:"MINIO_SECRET_KEY" required:"true"`
	Bucket          string `env:"MINIO_BUCKET" default:"docs"`
	UseSSL          bool   `env:"MINIO_USE_SSL" default:"false"`
	PresignedURLBase string `env:"MINIO_PRESIGN_BASE"` // optional override (CDN host)
}

// NewObjectStore connects to MinIO and ensures the bucket exists.
func NewObjectStore(ctx context.Context, cfg MinioConfig) (*ObjectStore, error) {
	cli, err := minio.New(cfg.Endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(cfg.AccessKey, cfg.SecretKey, ""),
		Secure: cfg.UseSSL,
	})
	if err != nil {
		return nil, fmt.Errorf("document: minio client: %w", err)
	}

	exists, err := cli.BucketExists(ctx, cfg.Bucket)
	if err != nil {
		return nil, fmt.Errorf("document: bucket exists: %w", err)
	}
	if !exists {
		if err := cli.MakeBucket(ctx, cfg.Bucket, minio.MakeBucketOptions{}); err != nil {
			return nil, fmt.Errorf("document: make bucket: %w", err)
		}
	}
	return &ObjectStore{cli: cli, bucket: cfg.Bucket}, nil
}

// Put uploads body to the bucket with SSE-S3 enabled (AES-256-GCM) and
// the supplied content-type. Returns the canonical object key.
func (s *ObjectStore) Put(ctx context.Context, key string, body io.Reader, size int64, contentType string) error {
	_, err := s.cli.PutObject(ctx, s.bucket, key, body, size, minio.PutObjectOptions{
		ContentType:          contentType,
		ServerSideEncryption: encrypt.NewSSE(), // SSE-S3 / AES-256
	})
	return err
}

// PresignURL returns a time-limited GET URL for key. Per §7, ttl must be
// ≤ 5 minutes for production reads; we cap it here so a misconfigured
// caller can't ask for a 1-day URL.
const MaxPresignTTL = 5 * time.Minute

// PresignURL mints a presigned GET URL bounded to MaxPresignTTL. The
// agentID is recorded as an x-amz-meta-agent-id query parameter so the
// access shows up in the bucket access log.
func (s *ObjectStore) PresignURL(ctx context.Context, key, agentID string, ttl time.Duration) (string, error) {
	if ttl <= 0 || ttl > MaxPresignTTL {
		ttl = MaxPresignTTL
	}
	q := url.Values{}
	q.Set("response-x-amz-meta-agent-id", agentID)
	u, err := s.cli.PresignedGetObject(ctx, s.bucket, key, ttl, q)
	if err != nil {
		return "", fmt.Errorf("document: presign: %w", err)
	}
	return u.String(), nil
}

// Delete removes an object. The metadata row's audit chain entry is
// written by the caller (see §7 right-to-erasure).
func (s *ObjectStore) Delete(ctx context.Context, key string) error {
	return s.cli.RemoveObject(ctx, s.bucket, key, minio.RemoveObjectOptions{})
}

// BucketName returns the configured bucket. Used by the metadata layer
// to populate documents.bucket on insert.
func (s *ObjectStore) BucketName() string { return s.bucket }
