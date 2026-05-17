package instagram

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Crypter is the seam for envelope decryption -- same shape as
// facebook.Crypter / whatsapp.Crypter. Production wires a Vault
// transit Crypter; the demo binary wires facebook.DemoCrypter (or a
// thin re-export).
type Crypter interface {
	Decrypt(ctx context.Context, ciphertext []byte, dekID string) ([]byte, error)
}

// PGTokenStore loads the access token for an IG Business account.
//
// Path 1 (FB-linked): the IG account rides the linked fb_pages row's
// access token. Path 2 (own token): decrypt the local access_token_ct.
// The OAuth handler currently only writes Path 1 rows, so the SQL
// favours the Path 1 join and falls back to Path 2 when present.
type PGTokenStore struct {
	Pool    *pgxpool.Pool
	Crypter Crypter
}

// NewPGTokenStore wires a store.
func NewPGTokenStore(pool *pgxpool.Pool, crypter Crypter) *PGTokenStore {
	return &PGTokenStore{Pool: pool, Crypter: crypter}
}

// Token returns the plaintext access token for the given IG user id.
func (s *PGTokenStore) Token(ctx context.Context, igUserID string) (string, error) {
	var (
		flow     string
		fbCT     []byte
		fbDek    string
		igCT     []byte
		igDek    string
		hasPath1 bool
		hasPath2 bool
	)
	err := s.Pool.QueryRow(ctx, `
		SELECT
		  ig.flow,
		  COALESCE(p.access_token_ct,      ''::bytea) AS fb_ct,
		  COALESCE(p.access_token_dek_id,  '')        AS fb_dek,
		  COALESCE(ig.access_token_ct,     ''::bytea) AS ig_ct,
		  COALESCE(ig.dek_id,              '')        AS ig_dek,
		  p.access_token_ct IS NOT NULL  AS has_path1,
		  ig.access_token_ct IS NOT NULL AS has_path2
		FROM ig_accounts ig
		LEFT JOIN fb_pages p ON p.page_id = ig.fb_page_id
		WHERE ig.ig_user_id = $1`, igUserID).
		Scan(&flow, &fbCT, &fbDek, &igCT, &igDek, &hasPath1, &hasPath2)
	if err != nil {
		return "", fmt.Errorf("ig tokenstore: lookup %s: %w", igUserID, err)
	}

	var ct []byte
	var dek string
	switch {
	case flow == "path1" && hasPath1:
		ct, dek = fbCT, fbDek
	case flow == "path2" && hasPath2:
		ct, dek = igCT, igDek
	default:
		return "", fmt.Errorf("ig tokenstore: no usable token for %s (flow=%s)", igUserID, flow)
	}
	pt, err := s.Crypter.Decrypt(ctx, ct, dek)
	if err != nil {
		return "", fmt.Errorf("ig tokenstore: decrypt %s: %w", igUserID, err)
	}
	return string(pt), nil
}
