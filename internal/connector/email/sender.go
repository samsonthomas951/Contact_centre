package email

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/smtp"
	"net/textproto"
	"strconv"
	"strings"
	"time"
)

// Sender ships outbound email via SMTP using the credentials carried
// on Mailbox. Designed so the worker only has to call SendText; all
// retry/classification policy lives below.
//
// Encryption choice:
//   * port 465 -> implicit TLS (SMTPS)
//   * any other port -> STARTTLS upgrade, mandatory
// We never send credentials in plaintext; an upstream relay that
// refuses STARTTLS aborts the send (SendErr.IsTransient == false).
type Sender struct {
	DialTimeout time.Duration // default 10s
	IOTimeout   time.Duration // default 30s
	// AllowPlaintext disables the mandatory STARTTLS upgrade. Set
	// only for local-dev SMTP catchers (Mailpit, MailHog) that don't
	// advertise TLS. Production MUST leave this false.
	AllowPlaintext bool
}

// NewSender returns a Sender with sane timeouts and TLS required.
func NewSender() *Sender {
	return &Sender{DialTimeout: 10 * time.Second, IOTimeout: 30 * time.Second}
}

// SendText sends a plain-text email and returns the Message-ID we
// generated. The Message-ID is what callers persist as
// platform_message_id so a later bounce notification can be matched
// back via email_outbound_log.
func (s *Sender) SendText(ctx context.Context, mb Mailbox, out OutboundEmail) (string, error) {
	if mb.SMTPHost == "" || mb.SMTPPort == 0 {
		return "", errors.New("email: mailbox SMTP not configured")
	}
	if out.ToAddress == "" {
		return "", errors.New("email: recipient required")
	}
	if strings.TrimSpace(out.BodyText) == "" {
		return "", errors.New("email: empty body")
	}

	domain := domainOf(mb.Address)
	msgID := NewMessageID(domain)

	mime, err := buildMessage(mb, out, msgID)
	if err != nil {
		return "", err
	}

	addr := net.JoinHostPort(mb.SMTPHost, strconv.Itoa(mb.SMTPPort))
	if err := s.dialAndSend(ctx, mb, out, addr, mime); err != nil {
		return "", err
	}
	slog.InfoContext(ctx, "email: sent",
		slog.String("from", mb.Address),
		slog.String("to", out.ToAddress),
		slog.String("message_id", msgID))
	return msgID, nil
}

// dialAndSend handles the dial/auth/data round-trip. Implicit TLS on
// 465; STARTTLS elsewhere. Auth is always PLAIN; restricting to PLAIN
// is fine because we force TLS first.
func (s *Sender) dialAndSend(ctx context.Context, mb Mailbox, out OutboundEmail, addr string, body []byte) error {
	dialer := &net.Dialer{Timeout: s.DialTimeout}
	var conn net.Conn
	var err error

	if mb.SMTPPort == 465 {
		// Implicit TLS.
		conn, err = tls.DialWithDialer(dialer, "tcp", addr, &tls.Config{
			ServerName: mb.SMTPHost, MinVersion: tls.VersionTLS12,
		})
	} else {
		conn, err = dialer.DialContext(ctx, "tcp", addr)
	}
	if err != nil {
		return &SendErr{Code: 0, Reason: "dial: " + err.Error(), Transient: true}
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(s.IOTimeout))

	c, err := smtp.NewClient(conn, mb.SMTPHost)
	if err != nil {
		return &SendErr{Code: 0, Reason: "smtp client: " + err.Error(), Transient: true}
	}
	defer func() { _ = c.Quit() }()

	if mb.SMTPPort != 465 {
		// STARTTLS unless we're already TLS. Refuse if the server
		// doesn't advertise it -- sending creds in cleartext would be
		// worse than a failed send. Exception: AllowPlaintext lets
		// the local-dev SMTP catchers (Mailpit, MailHog) work without
		// pretending to do TLS.
		ok, _ := c.Extension("STARTTLS")
		if !ok && !s.AllowPlaintext {
			return &SendErr{Code: 0, Reason: "server refused STARTTLS", Transient: false}
		}
		if ok {
			if err := c.StartTLS(&tls.Config{
				ServerName: mb.SMTPHost, MinVersion: tls.VersionTLS12,
			}); err != nil {
				return &SendErr{Code: 0, Reason: "starttls: " + err.Error(), Transient: true}
			}
		}
	}

	// Some demo/dev SMTP servers (Mailpit, MailHog) ignore AUTH.
	// Only attempt PLAIN if both creds are non-empty AND the server
	// advertises AUTH.
	if mb.SMTPUsername != "" && mb.SMTPPassword != "" {
		if ok, _ := c.Extension("AUTH"); ok {
			auth := smtp.PlainAuth("", mb.SMTPUsername, mb.SMTPPassword, mb.SMTPHost)
			if err := c.Auth(auth); err != nil {
				return classifyAuthErr(err)
			}
		}
	}

	if err := c.Mail(mb.Address); err != nil {
		return classifyDataErr(err, "MAIL FROM")
	}
	if err := c.Rcpt(out.ToAddress); err != nil {
		return classifyDataErr(err, "RCPT TO")
	}
	wc, err := c.Data()
	if err != nil {
		return classifyDataErr(err, "DATA")
	}
	if _, err := wc.Write(body); err != nil {
		return &SendErr{Code: 0, Reason: "write: " + err.Error(), Transient: true}
	}
	if err := wc.Close(); err != nil {
		return classifyDataErr(err, "DATA close")
	}
	return nil
}

// buildMessage serialises an RFC 5322 message. Single-part text/plain
// with UTF-8; if you need HTML, multipart/alternative would slot in
// here with a second part.
func buildMessage(mb Mailbox, out OutboundEmail, msgID string) ([]byte, error) {
	var buf bytes.Buffer
	from := mb.Address
	if mb.DisplayName != "" {
		from = fmt.Sprintf(`"%s" <%s>`, escapeHeader(mb.DisplayName), mb.Address)
	}
	to := out.ToAddress
	if out.ToName != "" {
		to = fmt.Sprintf(`"%s" <%s>`, escapeHeader(out.ToName), out.ToAddress)
	}

	// Required.
	fmt.Fprintf(&buf, "From: %s\r\n", from)
	fmt.Fprintf(&buf, "To: %s\r\n", to)
	fmt.Fprintf(&buf, "Subject: %s\r\n", encodeHeader(out.Subject))
	fmt.Fprintf(&buf, "Date: %s\r\n", time.Now().UTC().Format(time.RFC1123Z))
	fmt.Fprintf(&buf, "Message-ID: %s\r\n", msgID)
	fmt.Fprintf(&buf, "MIME-Version: 1.0\r\n")
	fmt.Fprintf(&buf, "Content-Type: text/plain; charset=utf-8\r\n")
	fmt.Fprintf(&buf, "Content-Transfer-Encoding: 8bit\r\n")

	// Threading.
	if out.InReplyTo != "" {
		fmt.Fprintf(&buf, "In-Reply-To: %s\r\n", out.InReplyTo)
	}
	if len(out.ReferencesChain) > 0 {
		fmt.Fprintf(&buf, "References: %s\r\n", strings.Join(out.ReferencesChain, " "))
	}

	// Body separator.
	buf.WriteString("\r\n")
	buf.WriteString(strings.ReplaceAll(out.BodyText, "\n", "\r\n"))
	return buf.Bytes(), nil
}

// SendErr is the typed failure. Transient -> NATS NAK and retry;
// permanent -> Term + log + record bounce.
type SendErr struct {
	Code      int    // SMTP reply code; 0 for transport/dial failures
	Reason    string // human description
	Transient bool   // 4xx + transport errors
}

// Error implements error.
func (e *SendErr) Error() string {
	return fmt.Sprintf("email: send (code=%d transient=%v): %s",
		e.Code, e.Transient, e.Reason)
}

// IsTransient mirrors the per-channel pattern.
func (e *SendErr) IsTransient() bool { return e.Transient }

// classifyDataErr maps net/smtp errors (textproto.Error{Code, Msg})
// to our transient/permanent split.
func classifyDataErr(err error, op string) *SendErr {
	if err == nil {
		return nil
	}
	var te *textproto.Error
	if errors.As(err, &te) {
		return &SendErr{
			Code:      te.Code,
			Reason:    op + ": " + te.Msg,
			Transient: te.Code >= 400 && te.Code < 500,
		}
	}
	return &SendErr{Code: 0, Reason: op + ": " + err.Error(), Transient: true}
}

// classifyAuthErr treats SMTP auth as permanent (no point retrying a
// bad password) but flags transport errors as transient.
func classifyAuthErr(err error) *SendErr {
	var te *textproto.Error
	if errors.As(err, &te) {
		return &SendErr{Code: te.Code, Reason: "AUTH: " + te.Msg, Transient: false}
	}
	return &SendErr{Code: 0, Reason: "AUTH: " + err.Error(), Transient: true}
}

func domainOf(addr string) string {
	if i := strings.IndexByte(addr, '@'); i >= 0 {
		return addr[i+1:]
	}
	return ""
}

// escapeHeader is the minimum quoting needed inside a display-name
// quoted-string: backslash + double-quote.
func escapeHeader(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	return s
}

// encodeHeader leaves ASCII subjects alone and Q-encodes the rest so
// non-ASCII characters survive an SMTP server that's strict about
// 7-bit headers.
func encodeHeader(s string) string {
	if isASCII(s) {
		return s
	}
	// MIME encoded-word, base64. Wrapping is left to the SMTP server.
	return "=?utf-8?B?" + base64Std(s) + "?="
}

func isASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] > 127 {
			return false
		}
	}
	return true
}

func base64Std(s string) string {
	return base64.StdEncoding.EncodeToString([]byte(s))
}
