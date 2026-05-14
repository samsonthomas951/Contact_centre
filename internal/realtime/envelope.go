// Package realtime is the WebSocket fan-out described in §10 of the
// technical plan. The service is stateless: every WS instance subscribes
// to per-agent Redis pub/sub channels for the agents it currently
// holds; ticket service / routing engine / SLA scheduler publish to
// those channels and any instance holding the agent's socket forwards.
//
// No sticky sessions required (per WebSocket.org's Go scaling guide
// quoted in §9): "Instead of relying on clients always reaching the
// same server, store connection and session state externally (Redis,
// a database, or another shared store). This way, any server can
// handle any reconnecting client."
package realtime

import (
	"encoding/json"
	"fmt"
	"time"
)

// Envelope is the on-wire shape of every message pushed to the agent
// browser. Adding fields is non-breaking for the JS client because
// unknown keys are ignored.
type Envelope struct {
	Type          string          `json:"type"`           // ticket.assigned | message.new | presence.update | sla.warning | system.notice
	Ts            time.Time       `json:"ts"`
	CorrelationID string          `json:"correlation_id,omitempty"`
	Payload       json.RawMessage `json:"payload"`
}

// Marshal serialises the envelope to JSON; convenience for callers that
// don't want to import encoding/json.
func (e Envelope) Marshal() ([]byte, error) { return json.Marshal(e) }

// New builds an Envelope, marshalling the typed payload to RawMessage.
func New(kind, correlationID string, payload any) (Envelope, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return Envelope{}, fmt.Errorf("realtime: marshal payload: %w", err)
	}
	return Envelope{
		Type:          kind,
		Ts:            time.Now().UTC(),
		CorrelationID: correlationID,
		Payload:       body,
	}, nil
}

// AgentChannel is the Redis pub/sub key for a specific agent's socket.
func AgentChannel(agentID string) string { return "ws:agent:" + agentID }

// TicketChannel is the Redis pub/sub key for everyone watching one
// ticket. Use this when an event is per-ticket (a typing indicator)
// rather than per-agent.
func TicketChannel(ticketID string) string { return "ws:ticket:" + ticketID }

// SupervisorChannel is the per-tenant supervisor broadcast key.
func SupervisorChannel(tenantID string) string { return "ws:tenant:" + tenantID + ":supervisor" }
