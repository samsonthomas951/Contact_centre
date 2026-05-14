package facebook

import (
	"encoding/json"
	"time"
)

// Webhook is the top-level payload Meta sends. We model only the fields
// the connector needs; unknown fields are ignored to keep us forward-
// compatible with Graph API additions.
type Webhook struct {
	Object string  `json:"object"`
	Entry  []Entry `json:"entry"`
}

// Entry is one Page's set of events in a webhook delivery.
type Entry struct {
	ID        string      `json:"id"` // Page id
	Time      int64       `json:"time"`
	Messaging []Messaging `json:"messaging,omitempty"`
}

// Messaging is one Messenger event — DM, postback, read, delivery.
type Messaging struct {
	Sender    Actor       `json:"sender"`
	Recipient Actor       `json:"recipient"`
	Timestamp int64       `json:"timestamp"`
	Message   *MessageBlk `json:"message,omitempty"`
	Postback  *Postback   `json:"postback,omitempty"`
	Read      *Read       `json:"read,omitempty"`
	Delivery  *Delivery   `json:"delivery,omitempty"`
}

// Actor is the polymorphic id pair Meta sends for sender/recipient.
type Actor struct {
	ID string `json:"id"`
}

// MessageBlk is the inbound message body. Quick-replies, attachments
// and reply_to are present in real payloads but trimmed to what the
// Phase-0 ingester needs.
type MessageBlk struct {
	MID  string `json:"mid"`
	Text string `json:"text,omitempty"`
}

// Postback is the structured "user clicked a button" event.
type Postback struct {
	MID     string `json:"mid"`
	Title   string `json:"title,omitempty"`
	Payload string `json:"payload,omitempty"`
}

// Read is "the user marked these messages read up to this watermark".
type Read struct {
	Watermark int64 `json:"watermark"`
}

// Delivery is "Meta delivered these mids to the user's device".
type Delivery struct {
	Mids      []string `json:"mids,omitempty"`
	Watermark int64    `json:"watermark"`
}

// InboundMessage is the canonical event the connector publishes onto
// NATS subject `ingress.fb.<event>`. Other services consume this; they
// never see the raw Meta envelope.
type InboundMessage struct {
	TenantID         string    `json:"tenant_id"`     // resolved by page_id lookup
	PageID           string    `json:"page_id"`
	Channel          string    `json:"channel"`       // always "fb" here
	Kind             string    `json:"kind"`          // message|postback|read|delivery
	CustomerExternal string    `json:"customer_external"` // Messenger PSID
	ConversationKey  string    `json:"conversation_key"`  // PSID is the thread on Messenger
	PlatformMsgID    string    `json:"platform_msg_id,omitempty"`
	Body             string    `json:"body,omitempty"`
	Postback         string    `json:"postback,omitempty"`
	OccurredAt       time.Time `json:"occurred_at"`
}

// Normalize fans Meta's batched webhook out into one InboundMessage per
// event. The caller supplies a resolver that maps page_id -> tenant_id;
// unrecognised pages produce a NotForUs flag so we drop the event with
// a 200 (Meta retries 5xx).
func Normalize(w Webhook, resolveTenant func(pageID string) (string, bool)) []InboundMessage {
	if w.Object != "page" {
		return nil
	}
	out := make([]InboundMessage, 0, len(w.Entry))
	for _, e := range w.Entry {
		tenant, ok := resolveTenant(e.ID)
		if !ok {
			continue
		}
		for _, m := range e.Messaging {
			base := InboundMessage{
				TenantID:         tenant,
				PageID:           e.ID,
				Channel:          "fb",
				CustomerExternal: m.Sender.ID,
				ConversationKey:  m.Sender.ID,
				OccurredAt:       time.UnixMilli(m.Timestamp).UTC(),
			}
			switch {
			case m.Message != nil:
				base.Kind = "message"
				base.PlatformMsgID = m.Message.MID
				base.Body = m.Message.Text
			case m.Postback != nil:
				base.Kind = "postback"
				base.PlatformMsgID = m.Postback.MID
				base.Postback = m.Postback.Payload
			case m.Read != nil:
				base.Kind = "read"
			case m.Delivery != nil:
				base.Kind = "delivery"
			default:
				continue
			}
			out = append(out, base)
		}
	}
	return out
}

// MarshalJSON makes InboundMessage easy to publish without re-importing
// encoding/json everywhere. Kept as a method so the JSON shape lives
// next to the type definition.
func (m InboundMessage) MarshalNATS() ([]byte, error) {
	return json.Marshal(m)
}
