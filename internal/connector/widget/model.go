// Package widget implements the embeddable web widget connector
// described in §15 of the technical plan.
//
// Visitor identity model: a long-lived first-party cookie carries a
// UUID. The first connect from that UUID creates a widget_visitors
// row; subsequent connects reuse it. When the visitor enters an email
// or phone, the visitor row is linked to a customers row (creating
// one if absent) so the rest of the platform sees the same shape it
// does for FB/X/WA/IG customers.
//
// Identification flow + consent banner are §15 + DPA s.29 requirements.
package widget

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

// ClientFrame is one inbound WS frame from the browser. The JS client
// sends a JSON line per action.
type ClientFrame struct {
	Type    string          `json:"type"`             // hello | message | identify | typing
	Body    string          `json:"body,omitempty"`
	Email   string          `json:"email,omitempty"`
	Phone   string          `json:"phone,omitempty"`
	Name    string          `json:"name,omitempty"`
	Payload json.RawMessage `json:"payload,omitempty"`
}

// ServerFrame is one outbound WS frame back to the browser.
type ServerFrame struct {
	Type    string          `json:"type"`             // hello_ack | agent_message | system | error
	Body    string          `json:"body,omitempty"`
	Ts      time.Time       `json:"ts,omitzero"`
	From    string          `json:"from,omitempty"`   // agent display name (no PII beyond first name)
	Payload json.RawMessage `json:"payload,omitempty"`
}

// HelloPayload is what the JS sends on `type=hello`. The visitor_id is
// generated client-side and stored in a cookie; user_agent is set by
// the server from r.UserAgent() and never trusted from the client.
type HelloPayload struct {
	VisitorID    uuid.UUID `json:"visitor_id"`
	SiteEmbedKey string    `json:"embed_key"`
}

// InboundMessage is the normalised NATS event the connector publishes
// onto subject `ingress.widget.<kind>`. Mirrors the other connectors'
// shape so the Ticket Service routes it without bespoke widget logic.
type InboundMessage struct {
	TenantID         string    `json:"tenant_id"`
	SiteID           string    `json:"site_id"`
	Channel          string    `json:"channel"` // always "widget"
	Kind             string    `json:"kind"`    // message | identified | typing
	CustomerExternal string    `json:"customer_external"` // visitor_id
	CustomerEmail    string    `json:"customer_email,omitempty"`
	CustomerPhone    string    `json:"customer_phone,omitempty"`
	CustomerName     string    `json:"customer_name,omitempty"`
	ConversationKey  string    `json:"conversation_key"`  // visitor_id is the thread
	Body             string    `json:"body,omitempty"`
	OccurredAt       time.Time `json:"occurred_at"`
}

// MarshalNATS produces the on-wire JSON for publishing onto JetStream.
func (m InboundMessage) MarshalNATS() ([]byte, error) { return json.Marshal(m) }
