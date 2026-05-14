package audit

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrChainBroken is returned by Verify when a row's stored hash does not
// match the recomputed hash, or its prev_hash does not match the prior
// row's hash.
var ErrChainBroken = errors.New("audit: hash chain broken")

// Writer is the singleton appender for the audit ledger.
//
// Only one Writer should run per database — the chain is serialised by a
// transactional advisory lock so concurrent writers can't interleave
// rows and produce divergent hashes.
type Writer struct {
	pool *pgxpool.Pool

	mu       sync.Mutex
	prevHash []byte // cached after first write
	prevSeq  int64
	ready    bool
}

// NewWriter constructs a Writer bound to pool. Call Bootstrap once at
// startup to load the tail hash.
func NewWriter(pool *pgxpool.Pool) *Writer { return &Writer{pool: pool} }

// Bootstrap loads the last row's seq + hash so subsequent appends chain
// onto an empty ledger correctly. Safe to call multiple times.
func (w *Writer) Bootstrap(ctx context.Context) error {
	w.mu.Lock()
	defer w.mu.Unlock()

	row := w.pool.QueryRow(ctx,
		`SELECT seq, hash FROM audit_events ORDER BY ts DESC, seq DESC LIMIT 1`)
	var seq int64
	var hash []byte
	switch err := row.Scan(&seq, &hash); {
	case errors.Is(err, pgx.ErrNoRows):
		w.prevHash = genesisHash()
		w.prevSeq = 0
	case err != nil:
		return fmt.Errorf("audit: bootstrap: %w", err)
	default:
		w.prevHash = hash
		w.prevSeq = seq
	}
	w.ready = true
	return nil
}

// Append validates e, fills Ts / PrevHash / Hash, and INSERTs it inside
// a transaction guarded by a Postgres advisory lock so the chain stays
// linear under concurrent calls.
func (w *Writer) Append(ctx context.Context, e *Event) error {
	if err := e.Validate(); err != nil {
		return err
	}

	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.ready {
		return errors.New("audit: writer not bootstrapped")
	}

	tx, err := w.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return fmt.Errorf("audit: begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// Cluster-wide lock keyed to a fixed sentinel — only one Append
	// commits at a time even across processes.
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, advisoryLockKey); err != nil {
		return fmt.Errorf("audit: advisory lock: %w", err)
	}

	// Compute hash from the seq we *will* receive — read currval after
	// insert and recompute? Simpler: insert with a known seq via the
	// next sequence value, then we can compute deterministically.
	var nextSeq int64
	if err := tx.QueryRow(ctx,
		`SELECT nextval(pg_get_serial_sequence('audit_events','seq'))`).
		Scan(&nextSeq); err != nil {
		return fmt.Errorf("audit: nextval: %w", err)
	}

	e.Seq = nextSeq
	// Postgres TIMESTAMPTZ stores microseconds; truncating here keeps
	// the in-memory value byte-equivalent to what Verify() will read
	// back, otherwise the hash chain would always fail.
	e.Ts = time.Now().UTC().Truncate(time.Microsecond)
	e.PrevHash = w.prevHash
	e.Hash = computeHash(e)

	if _, err := tx.Exec(ctx, `
		INSERT INTO audit_events
		  (seq, ts, tenant_id, actor_type, actor_id, action,
		   resource_type, resource_id, correlation_id, payload,
		   prev_hash, hash)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)`,
		e.Seq, e.Ts, e.TenantID, e.ActorType, e.ActorID, e.Action,
		nullableText(e.ResourceType), nullableText(e.ResourceID),
		e.CorrelationID, e.Payload, e.PrevHash, e.Hash,
	); err != nil {
		return fmt.Errorf("audit: insert: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("audit: commit: %w", err)
	}

	w.prevHash = e.Hash
	w.prevSeq = e.Seq
	return nil
}

// Verify walks the chain in seq order and returns ErrChainBroken at the
// first inconsistency, naming the offending seq.
func (w *Writer) Verify(ctx context.Context) error {
	rows, err := w.pool.Query(ctx, `
		SELECT seq, ts, tenant_id, actor_type, actor_id, action,
		       resource_type, resource_id, correlation_id, payload,
		       prev_hash, hash
		FROM audit_events
		ORDER BY ts ASC, seq ASC`)
	if err != nil {
		return fmt.Errorf("audit: verify scan: %w", err)
	}
	defer rows.Close()

	expectedPrev := genesisHash()
	for rows.Next() {
		var e Event
		var rt, rid *string
		if err := rows.Scan(&e.Seq, &e.Ts, &e.TenantID, &e.ActorType, &e.ActorID,
			&e.Action, &rt, &rid, &e.CorrelationID, &e.Payload,
			&e.PrevHash, &e.Hash); err != nil {
			return fmt.Errorf("audit: verify row: %w", err)
		}
		if rt != nil {
			e.ResourceType = *rt
		}
		if rid != nil {
			e.ResourceID = *rid
		}

		if !equalBytes(e.PrevHash, expectedPrev) {
			return fmt.Errorf("%w at seq=%d (prev_hash mismatch)", ErrChainBroken, e.Seq)
		}
		if !equalBytes(e.Hash, computeHash(&e)) {
			return fmt.Errorf("%w at seq=%d (hash mismatch)", ErrChainBroken, e.Seq)
		}
		expectedPrev = e.Hash
	}
	return rows.Err()
}

const advisoryLockKey = int64(0x4155_4449_5400_0001) // "AUDIT\x00\x00\x01"

func equalBytes(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func nullableText(s string) any {
	if s == "" {
		return nil
	}
	return s
}
