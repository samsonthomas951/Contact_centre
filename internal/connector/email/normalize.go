package email

import (
	"net/mail"
	"strings"
	"time"

	"github.com/google/uuid"
)

// IngressEvent is the cross-channel shape the ticket service consumes.
// Kept structurally identical to internal/ticket.IngressEvent so the
// caller can json.Marshal directly without a translation step.
type IngressEvent struct {
	TenantID         string    `json:"tenant_id"`
	Channel          string    `json:"channel"`
	Kind             string    `json:"kind"`
	CustomerExternal string    `json:"customer_external"` // customer email, lowercased
	CustomerHandle   string    `json:"customer_handle,omitempty"`
	CustomerName     string    `json:"customer_name,omitempty"`
	CustomerEmail    string    `json:"customer_email,omitempty"`
	CustomerPhone    string    `json:"customer_phone,omitempty"`
	ConversationKey  string    `json:"conversation_key"`
	PlatformMsgID    string    `json:"platform_msg_id,omitempty"`
	Body             string    `json:"body,omitempty"`
	OccurredAt       time.Time `json:"occurred_at"`
}

// Normalize turns one InboundEmail into the cross-channel IngressEvent.
// Threading rule: conversation_key is the customer's address (so any
// reply chain from the same person joins the same ticket). The
// ticket service's openOrReopenTicket then decides reuse vs new ticket
// based on the latest ticket's state.
func Normalize(in InboundEmail) IngressEvent {
	addr := strings.ToLower(strings.TrimSpace(in.FromAddress))
	subj := strings.TrimSpace(in.Subject)
	body := strings.TrimSpace(in.BodyText)
	if body == "" {
		body = "(no body)"
	}
	if subj != "" {
		body = "Subject: " + subj + "\n\n" + body
	}
	return IngressEvent{
		TenantID:         in.TenantID.String(),
		Channel:          "email",
		Kind:             "message",
		CustomerExternal: addr,
		CustomerEmail:    addr,
		CustomerName:     in.FromName,
		ConversationKey:  addr,
		PlatformMsgID:    in.MessageID,
		Body:             body,
		OccurredAt:       in.OccurredAt,
	}
}

// ParseAddressList splits a comma-separated address header into bare
// addresses (lowercased, deduped, no display names). Used when a
// provider hands us the full To/Cc list and we need to pick which of
// our mailboxes was the recipient.
func ParseAddressList(header string) []string {
	addrs, err := mail.ParseAddressList(header)
	if err != nil {
		// Some providers pre-split; fall back to a comma split.
		out := []string{}
		for _, p := range strings.Split(header, ",") {
			a := strings.TrimSpace(strings.ToLower(p))
			if a != "" {
				out = append(out, a)
			}
		}
		return dedupe(out)
	}
	out := make([]string, 0, len(addrs))
	for _, a := range addrs {
		out = append(out, strings.ToLower(a.Address))
	}
	return dedupe(out)
}

// SplitReferences is the standard RFC 5322 References parser: tokens
// are angle-bracketed message ids separated by whitespace. We keep
// the angle brackets because that's what RFC 5322 demands.
func SplitReferences(header string) []string {
	header = strings.TrimSpace(header)
	if header == "" {
		return nil
	}
	parts := strings.Fields(header)
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if strings.HasPrefix(p, "<") && strings.HasSuffix(p, ">") {
			out = append(out, p)
		}
	}
	return out
}

// NewMessageID generates an RFC 5322 Message-ID using the configured
// mailbox domain. UUID-based to avoid clashes; the domain anchors it
// to our infra so DMARC/DKIM remain valid.
func NewMessageID(domain string) string {
	if domain == "" {
		domain = "local"
	}
	return "<" + uuid.NewString() + "@" + domain + ">"
}

func dedupe(in []string) []string {
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	return out
}
