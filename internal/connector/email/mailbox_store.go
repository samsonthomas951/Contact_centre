package email

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrMailboxNotFound is returned when a lookup misses.
var ErrMailboxNotFound = errors.New("email: mailbox not found")

// MailboxStore reads + writes email_mailboxes with on-the-fly
// encryption. The plaintext password fields on Mailbox are populated
// only inside this package; callers see them but should not log them.
type MailboxStore struct {
	Pool    *pgxpool.Pool
	Crypter Crypter
}

// NewMailboxStore wires the store.
func NewMailboxStore(pool *pgxpool.Pool, crypter Crypter) *MailboxStore {
	return &MailboxStore{Pool: pool, Crypter: crypter}
}

// GetByID loads a single mailbox by UUID with passwords decrypted.
func (s *MailboxStore) GetByID(ctx context.Context, id uuid.UUID) (*Mailbox, error) {
	return s.scanOne(ctx, `
		SELECT id, tenant_id, address, display_name,
		       smtp_host, smtp_port, smtp_username,
		       smtp_password_ct, smtp_password_dek_id,
		       imap_host, imap_port, imap_username,
		       imap_password_ct, imap_password_dek_id,
		       imap_last_seen_uid,
		       webhook_signing_key_ct, webhook_signing_key_dek_id,
		       active
		FROM email_mailboxes WHERE id = $1`, id)
}

// GetByAddress loads a single mailbox by its address (e.g.
// support@acme.co.ke). Useful for webhook ingress where the provider
// posts the recipient.
func (s *MailboxStore) GetByAddress(ctx context.Context, address string) (*Mailbox, error) {
	return s.scanOne(ctx, `
		SELECT id, tenant_id, address, display_name,
		       smtp_host, smtp_port, smtp_username,
		       smtp_password_ct, smtp_password_dek_id,
		       imap_host, imap_port, imap_username,
		       imap_password_ct, imap_password_dek_id,
		       imap_last_seen_uid,
		       webhook_signing_key_ct, webhook_signing_key_dek_id,
		       active
		FROM email_mailboxes WHERE address = $1 AND active`, address)
}

// ListAll returns every active mailbox (used by the IMAP poller to
// iterate work across tenants).
func (s *MailboxStore) ListAll(ctx context.Context) ([]Mailbox, error) {
	rows, err := s.Pool.Query(ctx, `
		SELECT id, tenant_id, address, display_name,
		       smtp_host, smtp_port, smtp_username,
		       smtp_password_ct, smtp_password_dek_id,
		       imap_host, imap_port, imap_username,
		       imap_password_ct, imap_password_dek_id,
		       imap_last_seen_uid,
		       webhook_signing_key_ct, webhook_signing_key_dek_id,
		       active
		FROM email_mailboxes WHERE active
		ORDER BY tenant_id, address`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Mailbox{}
	for rows.Next() {
		m, err := s.scanRow(ctx, rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *m)
	}
	return out, rows.Err()
}

// ListByTenant returns mailboxes a given tenant owns.
func (s *MailboxStore) ListByTenant(ctx context.Context, tenantID uuid.UUID) ([]Mailbox, error) {
	rows, err := s.Pool.Query(ctx, `
		SELECT id, tenant_id, address, display_name,
		       smtp_host, smtp_port, smtp_username,
		       smtp_password_ct, smtp_password_dek_id,
		       imap_host, imap_port, imap_username,
		       imap_password_ct, imap_password_dek_id,
		       imap_last_seen_uid,
		       webhook_signing_key_ct, webhook_signing_key_dek_id,
		       active
		FROM email_mailboxes WHERE tenant_id = $1
		ORDER BY created_at DESC`, tenantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Mailbox{}
	for rows.Next() {
		m, err := s.scanRow(ctx, rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *m)
	}
	return out, rows.Err()
}

// CreateParams is the input for Create.
type CreateParams struct {
	TenantID          uuid.UUID
	Address           string
	DisplayName       string
	SMTPHost          string
	SMTPPort          int
	SMTPUsername      string
	SMTPPassword      string // plaintext; encrypted on the way in
	IMAPHost          string
	IMAPPort          int
	IMAPUsername      string
	IMAPPassword      string // plaintext; optional
	WebhookSigningKey string // plaintext; optional
}

// Create encrypts every secret and inserts the row, returning the
// freshly-loaded Mailbox (with plaintexts re-populated for the
// caller's convenience).
func (s *MailboxStore) Create(ctx context.Context, p CreateParams) (*Mailbox, error) {
	smtpCT, smtpDek, err := s.Crypter.Encrypt(ctx, []byte(p.SMTPPassword))
	if err != nil {
		return nil, fmt.Errorf("email: encrypt smtp: %w", err)
	}
	var imapCT, hookCT []byte
	var imapDek, hookDek string
	if p.IMAPPassword != "" {
		imapCT, imapDek, err = s.Crypter.Encrypt(ctx, []byte(p.IMAPPassword))
		if err != nil {
			return nil, fmt.Errorf("email: encrypt imap: %w", err)
		}
	}
	if p.WebhookSigningKey != "" {
		hookCT, hookDek, err = s.Crypter.Encrypt(ctx, []byte(p.WebhookSigningKey))
		if err != nil {
			return nil, fmt.Errorf("email: encrypt webhook: %w", err)
		}
	}

	var id uuid.UUID
	err = s.Pool.QueryRow(ctx, `
		INSERT INTO email_mailboxes
		  (tenant_id, address, display_name,
		   smtp_host, smtp_port, smtp_username, smtp_password_ct, smtp_password_dek_id,
		   imap_host, imap_port, imap_username, imap_password_ct, imap_password_dek_id,
		   webhook_signing_key_ct, webhook_signing_key_dek_id)
		VALUES ($1, $2, $3,
		        $4, $5, $6, $7, $8,
		        NULLIF($9,''), NULLIF($10,0), NULLIF($11,''), $12, NULLIF($13,''),
		        $14, NULLIF($15,''))
		RETURNING id`,
		p.TenantID, p.Address, p.DisplayName,
		p.SMTPHost, p.SMTPPort, p.SMTPUsername, smtpCT, smtpDek,
		p.IMAPHost, p.IMAPPort, p.IMAPUsername, imapCT, imapDek,
		hookCT, hookDek,
	).Scan(&id)
	if err != nil {
		return nil, err
	}
	return s.GetByID(ctx, id)
}

// Delete hard-deletes a mailbox. CASCADE removes its webhook dedupe
// rows.
func (s *MailboxStore) Delete(ctx context.Context, tenantID, id uuid.UUID) error {
	ct, err := s.Pool.Exec(ctx,
		`DELETE FROM email_mailboxes WHERE tenant_id = $1 AND id = $2`,
		tenantID, id)
	if err != nil {
		return err
	}
	if ct.RowsAffected() == 0 {
		return ErrMailboxNotFound
	}
	return nil
}

// AdvanceIMAPCheckpoint records the last UID we've ingested so the
// next poller cycle skips it.
func (s *MailboxStore) AdvanceIMAPCheckpoint(ctx context.Context, id uuid.UUID, uid int64) error {
	_, err := s.Pool.Exec(ctx,
		`UPDATE email_mailboxes SET imap_last_seen_uid = $2 WHERE id = $1`,
		id, uid)
	return err
}

// RecordWebhookSeen returns true if the (mailbox, messageID) pair is a
// fresh delivery, false if we've already ingested it. Idempotency
// against provider retries.
func (s *MailboxStore) RecordWebhookSeen(ctx context.Context, mailboxID uuid.UUID, messageID string) (bool, error) {
	ct, err := s.Pool.Exec(ctx, `
		INSERT INTO email_webhook_events (mailbox_id, message_id)
		VALUES ($1, $2) ON CONFLICT DO NOTHING`, mailboxID, messageID)
	if err != nil {
		return false, err
	}
	return ct.RowsAffected() == 1, nil
}

// LogOutbound records a sent email by Message-ID so a later bounce
// notification can be matched back to it.
func (s *MailboxStore) LogOutbound(ctx context.Context, tenantID, mailboxID uuid.UUID, ticketID *uuid.UUID, messageID string) error {
	_, err := s.Pool.Exec(ctx, `
		INSERT INTO email_outbound_log (tenant_id, mailbox_id, ticket_id, message_id)
		VALUES ($1, $2, $3, $4) ON CONFLICT (message_id) DO NOTHING`,
		tenantID, mailboxID, ticketID, messageID)
	return err
}

// RecordBounce flags a sent message as bounced. Called by the bounce
// processor (provider webhook or SMTP 5xx classifier).
func (s *MailboxStore) RecordBounce(ctx context.Context, messageID, kind, reason string) error {
	_, err := s.Pool.Exec(ctx, `
		UPDATE email_outbound_log
		SET bounced = TRUE, bounce_kind = $2, bounce_reason = $3
		WHERE message_id = $1`, messageID, kind, reason)
	return err
}

// scanOne is the QueryRow variant of scanRow.
func (s *MailboxStore) scanOne(ctx context.Context, sqlText string, args ...any) (*Mailbox, error) {
	rows, err := s.Pool.Query(ctx, sqlText, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	if !rows.Next() {
		return nil, ErrMailboxNotFound
	}
	return s.scanRow(ctx, rows)
}

// scanRow decodes one mailbox row and decrypts the secret blobs.
// Every column that can be NULL (per the 0018 migration's NULLable
// IMAP + webhook fields) is scanned through a pointer to tolerate
// nulls without an explicit COALESCE in the SQL.
func (s *MailboxStore) scanRow(ctx context.Context, rows pgx.Rows) (*Mailbox, error) {
	var (
		m                   Mailbox
		smtpCT              []byte
		smtpDek             string
		imapCT              []byte
		hookCT              []byte
		imapHost, imapUser  *string
		imapPort            *int
		imapDek, hookDek    *string
	)
	if err := rows.Scan(
		&m.ID, &m.TenantID, &m.Address, &m.DisplayName,
		&m.SMTPHost, &m.SMTPPort, &m.SMTPUsername, &smtpCT, &smtpDek,
		&imapHost, &imapPort, &imapUser, &imapCT, &imapDek,
		&m.IMAPLastSeenUID,
		&hookCT, &hookDek,
		&m.Active,
	); err != nil {
		return nil, err
	}
	if imapHost != nil {
		m.IMAPHost = *imapHost
	}
	if imapPort != nil {
		m.IMAPPort = *imapPort
	}
	if imapUser != nil {
		m.IMAPUsername = *imapUser
	}

	pt, err := s.Crypter.Decrypt(ctx, smtpCT, smtpDek)
	if err != nil {
		return nil, fmt.Errorf("email: decrypt smtp: %w", err)
	}
	m.SMTPPassword = string(pt)

	if len(imapCT) > 0 && imapDek != nil {
		pt, err := s.Crypter.Decrypt(ctx, imapCT, *imapDek)
		if err != nil {
			return nil, fmt.Errorf("email: decrypt imap: %w", err)
		}
		m.IMAPPassword = string(pt)
	}
	if len(hookCT) > 0 && hookDek != nil {
		pt, err := s.Crypter.Decrypt(ctx, hookCT, *hookDek)
		if err != nil {
			return nil, fmt.Errorf("email: decrypt webhook: %w", err)
		}
		m.WebhookSigningKey = string(pt)
	}
	return &m, nil
}
