// Package audit owns the append-only, hash-chained event ledger described
// in §8 of the technical plan. It is the only writer to the audit_events
// table; every other service publishes Event values to NATS subject
// `audit.events` and the audit consumer persists them.
//
// Tamper evidence comes from two layers:
//
//  1. Each row's `hash` is sha256(seq || ts || actor || action ||
//     payload || prev_hash); flipping any field invalidates the chain.
//  2. Every N rows, a Merkle root is computed across that window and
//     signed with an Ed25519 key held in HashiCorp Vault. The roots
//     are stored in `audit_anchors` and emailed daily to the DPO inbox
//     so an attacker who controls the audit DB cannot also rewrite the
//     anchors without compromising Vault and the DPO mailbox.
package audit

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// Event is the canonical wire and storage shape of an audit row.
//
// Producers fill everything except Seq, Ts, PrevHash and Hash — those are
// assigned by the writer when the row is appended.
type Event struct {
	Seq           int64           `json:"seq,omitempty"`
	Ts            time.Time       `json:"ts,omitzero"`
	TenantID      uuid.UUID       `json:"tenant_id"`
	ActorType     string          `json:"actor_type"` // agent|system|customer|connector
	ActorID       *uuid.UUID      `json:"actor_id,omitempty"`
	Action        string          `json:"action"` // e.g. "ticket.assign"
	ResourceType  string          `json:"resource_type,omitempty"`
	ResourceID    string          `json:"resource_id,omitempty"`
	CorrelationID uuid.UUID       `json:"correlation_id"`
	Payload       json.RawMessage `json:"payload,omitempty"`
	PrevHash      []byte          `json:"prev_hash,omitempty"`
	Hash          []byte          `json:"hash,omitempty"`
}

// Validate checks the producer-supplied fields. Returns the first error
// found so callers can surface a single actionable message.
func (e *Event) Validate() error {
	if e.TenantID == uuid.Nil {
		return fmt.Errorf("audit: tenant_id required")
	}
	switch e.ActorType {
	case "agent", "system", "customer", "connector":
	default:
		return fmt.Errorf("audit: invalid actor_type %q", e.ActorType)
	}
	if e.Action == "" {
		return fmt.Errorf("audit: action required")
	}
	if e.CorrelationID == uuid.Nil {
		return fmt.Errorf("audit: correlation_id required")
	}
	return nil
}

// computeHash returns sha256 over the canonical byte representation of
// the row plus the previous row's hash. Any change to any field changes
// this row's hash, which breaks every subsequent link.
func computeHash(e *Event) []byte {
	h := sha256.New()

	var seq [8]byte
	binary.BigEndian.PutUint64(seq[:], uint64(e.Seq))
	h.Write(seq[:])

	var ts [8]byte
	binary.BigEndian.PutUint64(ts[:], uint64(e.Ts.UnixNano()))
	h.Write(ts[:])

	h.Write(e.TenantID[:])
	h.Write([]byte(e.ActorType))
	if e.ActorID != nil {
		h.Write(e.ActorID[:])
	}
	h.Write([]byte(e.Action))
	h.Write([]byte(e.ResourceType))
	h.Write([]byte(e.ResourceID))
	h.Write(e.CorrelationID[:])
	if len(e.Payload) > 0 {
		h.Write(e.Payload)
	}
	h.Write(e.PrevHash)

	return h.Sum(nil)
}

// genesisHash is the conventional starting value for the chain — 32
// zero bytes. The first audit row's prev_hash equals this.
func genesisHash() []byte { return make([]byte, sha256.Size) }
