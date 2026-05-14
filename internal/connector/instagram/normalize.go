// Package instagram implements the Meta Instagram Business connector
// described in §4.1 (Phase 2) of the technical plan.
//
// Two flow variants ("Paths") coexist on the Graph API:
//
//   Path 1 (FB-linked, legacy):  IG Business account linked to a
//     Facebook Page; permissions `instagram_basic`,
//     `instagram_manage_messages`, `instagram_manage_comments`.
//     Webhook arrives with object="instagram" and the same X-Hub-
//     Signature-256 scheme as Messenger.
//
//   Path 2 (direct IG login):    Newer; `instagram_business_*` scopes;
//     supports IG accounts without a linked Page.
//
// Both deliver the same payload shape on this webhook; the difference
// is in token issuance (handled by the onboarding flow, not here). We
// normalise both into a single ingress.ig.<kind> NATS event.
package instagram

import (
	"encoding/json"
	"time"
)

// Webhook is Meta's top-level shape for IG.
type Webhook struct {
	Object string  `json:"object"`
	Entry  []Entry `json:"entry"`
}

// Entry is one IG account's set of events.
type Entry struct {
	ID        string      `json:"id"`        // IG user id
	Time      int64       `json:"time"`      // ms since epoch
	Messaging []Messaging `json:"messaging,omitempty"`
	Changes   []Change    `json:"changes,omitempty"`
}

// Messaging is one DM event for an IG inbox.
type Messaging struct {
	Sender    Actor       `json:"sender"`
	Recipient Actor       `json:"recipient"`
	Timestamp int64       `json:"timestamp"`
	Message   *MessageBlk `json:"message,omitempty"`
	Read      *Read       `json:"read,omitempty"`
}

// Actor is the polymorphic id pair for sender/recipient.
type Actor struct {
	ID string `json:"id"`
}

// MessageBlk is the inbound message body.
type MessageBlk struct {
	MID         string        `json:"mid"`
	Text        string        `json:"text,omitempty"`
	Attachments []Attachment  `json:"attachments,omitempty"`
	IsEcho      bool          `json:"is_echo,omitempty"`
}

// Attachment is one image/video/file in a DM.
type Attachment struct {
	Type    string `json:"type"`            // image | video | audio | file | share
	Payload struct {
		URL string `json:"url,omitempty"`
	} `json:"payload"`
}

// Read is a "user read up to <watermark>" event.
type Read struct {
	MID string `json:"mid,omitempty"`
}

// Change carries field-typed events (comments, mentions, story_insights,
// etc.). For Phase 2 we care about `comments` and `mentions`.
type Change struct {
	Field string          `json:"field"`
	Value json.RawMessage `json:"value"`
}

// CommentValue is the payload of a `comments` change.
type CommentValue struct {
	ID        string `json:"id"`
	Text      string `json:"text"`
	MediaID   string `json:"media_id,omitempty"`
	From      struct {
		ID       string `json:"id"`
		Username string `json:"username"`
	} `json:"from"`
	CreatedTime int64 `json:"created_time"`
}

// MentionValue is the payload of a `mentions` change.
type MentionValue struct {
	CommentID string `json:"comment_id,omitempty"`
	MediaID   string `json:"media_id,omitempty"`
}

// InboundMessage is the canonical event published to NATS subject
// ingress.ig.<kind>. kind in {dm, read, comment, mention}.
type InboundMessage struct {
	TenantID         string    `json:"tenant_id"`
	IGUserID         string    `json:"ig_user_id"`
	Channel          string    `json:"channel"` // always "ig"
	Kind             string    `json:"kind"`
	CustomerExternal string    `json:"customer_external"`
	CustomerHandle   string    `json:"customer_handle,omitempty"`
	ConversationKey  string    `json:"conversation_key"`
	PlatformMsgID    string    `json:"platform_msg_id,omitempty"`
	MediaID          string    `json:"media_id,omitempty"`
	Body             string    `json:"body,omitempty"`
	OccurredAt       time.Time `json:"occurred_at"`
}

// MarshalNATS produces the on-wire JSON.
func (m InboundMessage) MarshalNATS() ([]byte, error) { return json.Marshal(m) }

// TenantResolver maps an IG user id to the internal tenant id.
type TenantResolver interface {
	ResolveTenant(igUserID string) (tenantID string, ok bool)
}

// Normalize fans an IG webhook delivery out into per-event events.
// Unknown IG accounts are dropped. Echo messages (messages we just
// sent, surfaced back) are dropped because they cause feedback loops.
func Normalize(w Webhook, resolve func(igUserID string) (string, bool)) []InboundMessage {
	if w.Object != "instagram" {
		return nil
	}
	out := []InboundMessage{}
	for _, e := range w.Entry {
		tenant, ok := resolve(e.ID)
		if !ok {
			continue
		}

		for _, m := range e.Messaging {
			if m.Message != nil && m.Message.IsEcho {
				// Our own outbound, echoed back -- drop.
				continue
			}
			out = append(out, messagingToInbound(tenant, e.ID, m))
		}

		for _, c := range e.Changes {
			ev := changeToInbound(tenant, e.ID, c)
			if ev != nil {
				out = append(out, *ev)
			}
		}
	}
	return out
}

func messagingToInbound(tenant, igUserID string, m Messaging) InboundMessage {
	base := InboundMessage{
		TenantID:         tenant,
		IGUserID:         igUserID,
		Channel:          "ig",
		CustomerExternal: m.Sender.ID,
		ConversationKey:  m.Sender.ID, // IG DM thread is keyed on user pair
		OccurredAt:       time.UnixMilli(m.Timestamp).UTC(),
	}
	switch {
	case m.Message != nil:
		base.Kind = "dm"
		base.PlatformMsgID = m.Message.MID
		base.Body = m.Message.Text
	case m.Read != nil:
		base.Kind = "read"
	default:
		base.Kind = "other"
	}
	return base
}

func changeToInbound(tenant, igUserID string, ch Change) *InboundMessage {
	switch ch.Field {
	case "comments":
		var v CommentValue
		if err := json.Unmarshal(ch.Value, &v); err != nil {
			return nil
		}
		return &InboundMessage{
			TenantID:         tenant,
			IGUserID:         igUserID,
			Channel:          "ig",
			Kind:             "comment",
			CustomerExternal: v.From.ID,
			CustomerHandle:   v.From.Username,
			ConversationKey:  v.MediaID, // comments thread by media
			PlatformMsgID:    v.ID,
			MediaID:          v.MediaID,
			Body:             v.Text,
			OccurredAt:       time.Unix(v.CreatedTime, 0).UTC(),
		}
	case "mentions":
		var v MentionValue
		if err := json.Unmarshal(ch.Value, &v); err != nil {
			return nil
		}
		return &InboundMessage{
			TenantID:        tenant,
			IGUserID:        igUserID,
			Channel:         "ig",
			Kind:            "mention",
			ConversationKey: v.MediaID,
			PlatformMsgID:   v.CommentID,
			MediaID:         v.MediaID,
			OccurredAt:      time.Now().UTC(),
		}
	}
	return nil
}
