// Package email is the email channel connector: webhook + IMAP intake,
// SMTP outbound, RFC 5322 threading via In-Reply-To / References, and
// bounce classification. Mirrors the shape of the other connectors
// (facebook, instagram, whatsapp) so the ticket service consumes
// inbound and the outbound worker fan-out works without per-channel
// special cases.
package email

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// Mailbox is the per-tenant email account row. Plaintext password
// fields are populated by MailboxStore after decryption; the
// ciphertext + dek_id fields are not exposed past the store boundary.
type Mailbox struct {
	ID                uuid.UUID
	TenantID          uuid.UUID
	Address           string
	DisplayName       string
	SMTPHost          string
	SMTPPort          int
	SMTPUsername      string
	SMTPPassword      string // decrypted in memory
	IMAPHost          string
	IMAPPort          int
	IMAPUsername      string
	IMAPPassword      string // decrypted in memory
	IMAPLastSeenUID   int64
	WebhookSigningKey string // decrypted; empty when webhook disabled
	Active            bool
}

// Crypter is the seam for envelope decryption. Same shape as the other
// connectors' Crypters; production wires Vault transit, demo wires
// facebook.DemoCrypter.
type Crypter interface {
	Encrypt(ctx context.Context, plaintext []byte) ([]byte, string, error)
	Decrypt(ctx context.Context, ciphertext []byte, dekID string) ([]byte, error)
}

// InboundEmail is the normalised inbound shape (from webhook or IMAP).
// One InboundEmail -> one IngressEvent on JetStream.
type InboundEmail struct {
	MailboxID       uuid.UUID // which of our mailboxes received it
	TenantID        uuid.UUID
	FromAddress     string    // RFC 5322 addr-spec; lowercased
	FromName        string    // display name, may be empty
	Subject         string
	BodyText        string    // text/plain part, or HTML-stripped fallback
	MessageID       string    // remote Message-ID (used for dedupe)
	InReplyTo       string    // optional; the first reference in References
	References      []string  // full chain (most-recent last)
	OccurredAt      time.Time // Date header, or now() if missing
}

// OutboundEmail is what the SMTP sender ships. ReplyTo is what we
// write into In-Reply-To; ReferencesChain is the chain we write to
// References. Both come from the most-recent inbound message on the
// thread; the sender generates a fresh Message-ID per send.
type OutboundEmail struct {
	MailboxID       uuid.UUID
	ToAddress       string
	ToName          string // optional display name for the To: header
	Subject         string
	BodyText        string
	InReplyTo       string   // RFC 5322; written verbatim into In-Reply-To
	ReferencesChain []string // written into References as space-separated
}

