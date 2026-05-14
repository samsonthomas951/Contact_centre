// Package whatsapp implements the Meta WhatsApp Cloud API connector
// described in §4 (Phase 2) of the technical plan.
//
// Auth: same X-Hub-Signature-256 HMAC-SHA256 scheme as Messenger /
// Instagram (see internal/connector/meta). Tokens are stored encrypted;
// the connector uses them to send via the Graph API endpoint
// `/{phone_number_id}/messages`.
//
// Per-message cost note (per §17): WhatsApp went per-message on
// 2025-07-01. Service messages within the 24h customer window are
// free, marketing/auth/utility templates outside the window cost
// $0.025–$0.04 per send in Kenya as of plan drafting. The Customer
// Window detector below is what gates which path a reply uses.
package whatsapp

import (
	"encoding/json"
	"time"
)

// Webhook is the top-level payload Meta sends. Modelled to be
// forward-compatible: fields not used by Phase 2 are tolerated.
type Webhook struct {
	Object string  `json:"object"`
	Entry  []Entry `json:"entry"`
}

// Entry is one Business Account's set of changes in a webhook delivery.
type Entry struct {
	ID      string   `json:"id"` // WABA id
	Changes []Change `json:"changes"`
}

// Change carries a Value when the `field` is "messages".
type Change struct {
	Field string `json:"field"`
	Value Value  `json:"value"`
}

// Value is the payload for `field=messages`. Statuses + Contacts +
// Messages live here.
type Value struct {
	MessagingProduct string    `json:"messaging_product"` // "whatsapp"
	Metadata         Metadata  `json:"metadata"`
	Contacts         []Contact `json:"contacts,omitempty"`
	Messages         []Message `json:"messages,omitempty"`
	Statuses         []Status  `json:"statuses,omitempty"`
}

// Metadata identifies the receiving Business phone number.
type Metadata struct {
	DisplayPhoneNumber string `json:"display_phone_number"`
	PhoneNumberID      string `json:"phone_number_id"`
}

// Contact is the customer profile attached to inbound messages.
type Contact struct {
	WaID    string  `json:"wa_id"` // E.164 without "+"
	Profile Profile `json:"profile"`
}

// Profile is the customer display name as Meta surfaces it.
type Profile struct {
	Name string `json:"name"`
}

// Message is one inbound message. Body lives under the type-named key
// (`text.body`, `image.caption`, etc.) -- we only normalise text + the
// generic envelope for Phase 2.
type Message struct {
	ID         string  `json:"id"`        // wamid.*
	From       string  `json:"from"`      // customer wa_id
	Timestamp  string  `json:"timestamp"` // unix seconds as string
	Type       string  `json:"type"`      // text|image|document|button|interactive|...
	Text       *Text   `json:"text,omitempty"`
	Image      *Media  `json:"image,omitempty"`
	Document   *Media  `json:"document,omitempty"`
	Context    *Context `json:"context,omitempty"`
}

// Text carries inbound text-message bodies.
type Text struct{ Body string `json:"body"` }

// Media is the shared shape for image/document/audio/video.
type Media struct {
	ID       string `json:"id"`
	MimeType string `json:"mime_type"`
	Sha256   string `json:"sha256,omitempty"`
	Caption  string `json:"caption,omitempty"`
	Filename string `json:"filename,omitempty"`
}

// Context links replies to the original message.
type Context struct {
	From string `json:"from,omitempty"`
	ID   string `json:"id,omitempty"`
}

// Status is a delivery/read receipt for one of our outbound messages.
type Status struct {
	ID          string `json:"id"`          // wamid we sent
	RecipientID string `json:"recipient_id"`
	Status      string `json:"status"`      // sent|delivered|read|failed
	Timestamp   string `json:"timestamp"`
}

// InboundMessage is the normalised event the connector publishes to
// NATS subject `ingress.wa.<kind>`.
type InboundMessage struct {
	TenantID         string    `json:"tenant_id"`
	PhoneNumberID    string    `json:"phone_number_id"`
	Channel          string    `json:"channel"` // always "wa"
	Kind             string    `json:"kind"`    // message|status|context_reply
	CustomerExternal string    `json:"customer_external"` // wa_id
	CustomerName     string    `json:"customer_name,omitempty"`
	ConversationKey  string    `json:"conversation_key"`  // wa_id is the thread
	PlatformMsgID    string    `json:"platform_msg_id,omitempty"`
	Body             string    `json:"body,omitempty"`
	StatusKind       string    `json:"status_kind,omitempty"` // sent|delivered|read|failed
	MediaID          string    `json:"media_id,omitempty"`
	MediaMime        string    `json:"media_mime,omitempty"`
	ReplyToMsgID     string    `json:"reply_to_msg_id,omitempty"`
	OccurredAt       time.Time `json:"occurred_at"`
}

// MarshalNATS produces the on-wire JSON for publishing onto JetStream.
func (m InboundMessage) MarshalNATS() ([]byte, error) { return json.Marshal(m) }

// TenantResolver maps a WhatsApp phone_number_id to a tenant.
type TenantResolver interface {
	ResolveTenant(phoneNumberID string) (tenantID string, ok bool)
}

// Normalize fans a webhook delivery out into per-event InboundMessage
// values. Unknown phone-number IDs are dropped (connector pattern).
// Type=`text` carries the body; other media types surface their id +
// mime so the Doc Service can pull bytes on demand.
func Normalize(w Webhook, resolve func(phoneNumberID string) (string, bool)) []InboundMessage {
	if w.Object != "whatsapp_business_account" {
		return nil
	}
	out := []InboundMessage{}
	for _, e := range w.Entry {
		for _, ch := range e.Changes {
			if ch.Field != "messages" {
				continue
			}
			v := ch.Value
			tenant, ok := resolve(v.Metadata.PhoneNumberID)
			if !ok {
				continue
			}
			nameOf := contactNameLookup(v.Contacts)
			for _, m := range v.Messages {
				out = append(out, messageToInbound(tenant, v.Metadata.PhoneNumberID, m, nameOf))
			}
			for _, s := range v.Statuses {
				out = append(out, statusToInbound(tenant, v.Metadata.PhoneNumberID, s))
			}
		}
	}
	return out
}

func contactNameLookup(cs []Contact) map[string]string {
	m := make(map[string]string, len(cs))
	for _, c := range cs {
		m[c.WaID] = c.Profile.Name
	}
	return m
}

func messageToInbound(tenant, phoneID string, m Message, name map[string]string) InboundMessage {
	base := InboundMessage{
		TenantID:         tenant,
		PhoneNumberID:    phoneID,
		Channel:          "wa",
		Kind:             "message",
		CustomerExternal: m.From,
		CustomerName:     name[m.From],
		ConversationKey:  m.From,
		PlatformMsgID:    m.ID,
		OccurredAt:       parseUnixSecondsString(m.Timestamp),
	}
	if m.Context != nil {
		base.ReplyToMsgID = m.Context.ID
	}
	switch m.Type {
	case "text":
		if m.Text != nil {
			base.Body = m.Text.Body
		}
	case "image":
		if m.Image != nil {
			base.MediaID = m.Image.ID
			base.MediaMime = m.Image.MimeType
			base.Body = m.Image.Caption
		}
	case "document":
		if m.Document != nil {
			base.MediaID = m.Document.ID
			base.MediaMime = m.Document.MimeType
			base.Body = m.Document.Caption
		}
	}
	return base
}

func statusToInbound(tenant, phoneID string, s Status) InboundMessage {
	return InboundMessage{
		TenantID:         tenant,
		PhoneNumberID:    phoneID,
		Channel:          "wa",
		Kind:             "status",
		CustomerExternal: s.RecipientID,
		ConversationKey:  s.RecipientID,
		PlatformMsgID:    s.ID,
		StatusKind:       s.Status,
		OccurredAt:       parseUnixSecondsString(s.Timestamp),
	}
}

func parseUnixSecondsString(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	var n int64
	for _, c := range s {
		if c < '0' || c > '9' {
			return time.Time{}
		}
		n = n*10 + int64(c-'0')
	}
	return time.Unix(n, 0).UTC()
}
