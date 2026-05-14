package ticket

import (
	"time"

	"github.com/google/uuid"
)

// Channel enumerates the inbound channels a conversation can come from.
type Channel string

const (
	ChannelFB     Channel = "fb"
	ChannelX      Channel = "x"
	ChannelWA     Channel = "wa"
	ChannelIG     Channel = "ig"
	ChannelWidget Channel = "widget"
	ChannelVoice  Channel = "voice"
)

// Direction labels who sent a message.
type Direction string

const (
	DirectionIn   Direction = "in"   // from customer
	DirectionOut  Direction = "out"  // from agent to customer
	DirectionNote Direction = "note" // internal-only note, not pushed to channel
)

// Ticket mirrors the public columns of the tickets table.
type Ticket struct {
	ID                  uuid.UUID  `json:"id"`
	TenantID            uuid.UUID  `json:"tenant_id"`
	ConversationID      uuid.UUID  `json:"conversation_id"`
	State               State      `json:"state"`
	Priority            int16      `json:"priority"`
	RequiredSkills      []string   `json:"required_skills"`
	AssignedAgentID     *uuid.UUID `json:"assigned_agent_id,omitempty"`
	SLAFirstResponseDue *time.Time `json:"sla_first_response_due,omitempty"`
	SLAResolutionDue    *time.Time `json:"sla_resolution_due,omitempty"`
	FirstResponseAt     *time.Time `json:"first_response_at,omitempty"`
	ResolvedAt          *time.Time `json:"resolved_at,omitempty"`
	ClosedAt            *time.Time `json:"closed_at,omitempty"`
	CreatedAt           time.Time  `json:"created_at"`
	UpdatedAt           time.Time  `json:"updated_at"`
}

// Message mirrors the public columns of the messages partitioned table.
type Message struct {
	ID                int64      `json:"id"`
	TenantID          uuid.UUID  `json:"tenant_id"`
	TicketID          uuid.UUID  `json:"ticket_id"`
	Direction         Direction  `json:"direction"`
	AgentID           *uuid.UUID `json:"agent_id,omitempty"`
	Body              string     `json:"body"`
	Attachments       []uuid.UUID `json:"attachments"`
	PlatformMessageID *string    `json:"platform_message_id,omitempty"`
	CreatedAt         time.Time  `json:"created_at"`
}
