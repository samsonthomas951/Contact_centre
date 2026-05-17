package whatsapp

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Crypter is the seam for envelope decryption. Production wires
// HashiCorp Vault transit; the demo wires facebook.DemoCrypter.
type Crypter interface {
	Decrypt(ctx context.Context, ciphertext []byte, dekID string) ([]byte, error)
}

// PGNumberStore loads + decrypts the system-user token for a given
// phone_number_id.
type PGNumberStore struct {
	Pool    *pgxpool.Pool
	Crypter Crypter
}

// NewPGNumberStore wires a store.
func NewPGNumberStore(pool *pgxpool.Pool, crypter Crypter) *PGNumberStore {
	return &PGNumberStore{Pool: pool, Crypter: crypter}
}

// Token loads + decrypts.
func (s *PGNumberStore) Token(ctx context.Context, phoneNumberID string) (string, error) {
	var (
		ct    []byte
		dekID string
	)
	err := s.Pool.QueryRow(ctx, `
		SELECT access_token_ct, dek_id
		FROM wa_phone_numbers
		WHERE phone_number_id = $1`, phoneNumberID).Scan(&ct, &dekID)
	if err != nil {
		return "", fmt.Errorf("wa numberstore: lookup %s: %w", phoneNumberID, err)
	}
	pt, err := s.Crypter.Decrypt(ctx, ct, dekID)
	if err != nil {
		return "", fmt.Errorf("wa numberstore: decrypt %s: %w", phoneNumberID, err)
	}
	return string(pt), nil
}
