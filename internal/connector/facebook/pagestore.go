package facebook

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5/pgxpool"
)

// PGPageStore is the production-shaped PageStore: it reads fb_pages and
// decrypts the access token via the injected Crypter. Both the gateway
// (OAuth handler) and outbound workers wire the same Crypter so a
// token encrypted by one process is readable by another.
//
// BUC observations are logged but not yet persisted -- the §4.1 quota
// dashboard reads from a separate Prometheus collector seeded by these
// log lines (jq filter on the message field).
type PGPageStore struct {
	Pool    *pgxpool.Pool
	Crypter Crypter
}

// NewPGPageStore wires a store to a pool + crypter.
func NewPGPageStore(pool *pgxpool.Pool, crypter Crypter) *PGPageStore {
	return &PGPageStore{Pool: pool, Crypter: crypter}
}

// Token loads + decrypts the access token for the given Page id.
func (s *PGPageStore) Token(ctx context.Context, pageID string) (string, error) {
	var (
		ct    []byte
		dekID string
	)
	err := s.Pool.QueryRow(ctx, `
		SELECT access_token_ct, access_token_dek_id
		FROM fb_pages
		WHERE page_id = $1`, pageID).Scan(&ct, &dekID)
	if err != nil {
		return "", fmt.Errorf("pagestore: lookup page %s: %w", pageID, err)
	}
	pt, err := s.Crypter.Decrypt(ctx, ct, dekID)
	if err != nil {
		return "", fmt.Errorf("pagestore: decrypt page %s: %w", pageID, err)
	}
	return string(pt), nil
}

// ObserveBUC currently just logs. The supervisor dashboard's quota
// gauges read from a Prometheus scraper that tails these lines;
// promoting to a real Counter is a §17 follow-up.
func (s *PGPageStore) ObserveBUC(ctx context.Context, pageID, header string) {
	slog.InfoContext(ctx, "fb: BUC observed",
		slog.String("page_id", pageID),
		slog.String("buc_header", header))
}
