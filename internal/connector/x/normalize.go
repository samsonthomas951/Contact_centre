package x

import (
	"encoding/json"
	"time"
)

// AAAWebhook is the Account Activity API event envelope. Only the
// fields we actually consume are modelled; X frequently adds new
// top-level event types.
type AAAWebhook struct {
	ForUserID            string             `json:"for_user_id"`
	UserHasBlocked       *bool              `json:"user_has_blocked,omitempty"`
	TweetCreateEvents    []TweetEvent       `json:"tweet_create_events,omitempty"`
	DirectMessageEvents  []DMEvent          `json:"direct_message_events,omitempty"`
	FavoriteEvents       []FavoriteEvent    `json:"favorite_events,omitempty"`
	FollowEvents         []FollowEvent      `json:"follow_events,omitempty"`
	BlockEvents          []json.RawMessage  `json:"block_events,omitempty"`
	MuteEvents           []json.RawMessage  `json:"mute_events,omitempty"`
}

// TweetEvent covers mentions, replies, and quote-tweets directed at the
// subscribed account.
type TweetEvent struct {
	ID                string        `json:"id_str"`
	CreatedAt         string        `json:"created_at"`
	Text              string        `json:"text,omitempty"`
	FullText          string        `json:"full_text,omitempty"`
	User              TweetUser     `json:"user"`
	InReplyToStatusID string        `json:"in_reply_to_status_id_str,omitempty"`
	InReplyToUserID   string        `json:"in_reply_to_user_id_str,omitempty"`
	QuotedStatus      *TweetEvent   `json:"quoted_status,omitempty"`
	Entities          *Entities     `json:"entities,omitempty"`
}

// TweetUser is the author of a TweetEvent. Sub-set of v1.1 user object.
type TweetUser struct {
	ID         string `json:"id_str"`
	ScreenName string `json:"screen_name"`
	Name       string `json:"name"`
}

// Entities surface mentions and URLs in tweet text.
type Entities struct {
	UserMentions []struct {
		ID         string `json:"id_str"`
		ScreenName string `json:"screen_name"`
	} `json:"user_mentions,omitempty"`
}

// DMEvent is one direct-message event in either direction.
type DMEvent struct {
	ID               string  `json:"id"`
	CreatedTimestamp string  `json:"created_timestamp"` // ms since epoch as string
	Type             string  `json:"type"`              // "message_create"
	MessageCreate    *struct {
		SenderID string `json:"sender_id"`
		Target   struct {
			RecipientID string `json:"recipient_id"`
		} `json:"target"`
		MessageData struct {
			Text string `json:"text"`
		} `json:"message_data"`
	} `json:"message_create,omitempty"`
}

// FavoriteEvent is "user liked one of our posts".
type FavoriteEvent struct {
	ID              string `json:"id"`
	CreatedAt       string `json:"created_at"`
	FavoritedStatus struct {
		ID string `json:"id_str"`
	} `json:"favorited_status"`
	User TweetUser `json:"user"`
}

// FollowEvent is a follow/unfollow notification.
type FollowEvent struct {
	Type             string `json:"type"` // "follow" | "unfollow"
	CreatedTimestamp string `json:"created_timestamp"`
	Target           TweetUser `json:"target"`
	Source           TweetUser `json:"source"`
}

// InboundMessage is the canonical NATS event. Mirrors facebook's shape
// so downstream services see one type per channel without bespoke
// fan-out logic.
type InboundMessage struct {
	TenantID         string    `json:"tenant_id"`
	AccountID        string    `json:"account_id"` // subscribed user id
	Channel          string    `json:"channel"`    // always "x"
	Kind             string    `json:"kind"`       // mention|reply|quote|dm|favorite|follow
	CustomerExternal string    `json:"customer_external"` // X user id
	CustomerHandle   string    `json:"customer_handle,omitempty"`
	ConversationKey  string    `json:"conversation_key"`  // DM convo id or root tweet id
	PlatformMsgID    string    `json:"platform_msg_id,omitempty"`
	Body             string    `json:"body,omitempty"`
	OccurredAt       time.Time `json:"occurred_at"`
}

// MarshalNATS produces the on-wire JSON for publishing onto JetStream.
func (m InboundMessage) MarshalNATS() ([]byte, error) { return json.Marshal(m) }

// TenantResolver maps a subscribed X account id to the internal tenant id.
type TenantResolver interface {
	ResolveTenant(accountID string) (tenantID string, ok bool)
}

// Normalize fans an AAA delivery out into per-event InboundMessage values.
// Unknown accounts are dropped (matching the FB connector's behaviour);
// unknown event types are dropped without erroring so X adding new
// event categories doesn't crash the pipeline.
//
// X explicitly warns that the same event can arrive twice when two
// subscribed users participate in the same interaction (e.g. mention).
// `ForUserID` is what disambiguates — we always key the resulting event
// on it, never on inferred recipients.
func Normalize(w AAAWebhook, resolve func(accountID string) (string, bool)) []InboundMessage {
	tenant, ok := resolve(w.ForUserID)
	if !ok {
		return nil
	}
	out := make([]InboundMessage, 0,
		len(w.TweetCreateEvents)+len(w.DirectMessageEvents)+
			len(w.FavoriteEvents)+len(w.FollowEvents))

	for _, t := range w.TweetCreateEvents {
		body := t.Text
		if t.FullText != "" {
			body = t.FullText
		}
		kind := classifyTweet(t)
		convKey := t.InReplyToStatusID
		if convKey == "" {
			convKey = t.ID
		}
		out = append(out, InboundMessage{
			TenantID:         tenant,
			AccountID:        w.ForUserID,
			Channel:          "x",
			Kind:             kind,
			CustomerExternal: t.User.ID,
			CustomerHandle:   t.User.ScreenName,
			ConversationKey:  convKey,
			PlatformMsgID:    t.ID,
			Body:             body,
			OccurredAt:       parseTwitterTime(t.CreatedAt),
		})
	}

	for _, d := range w.DirectMessageEvents {
		if d.MessageCreate == nil {
			continue
		}
		ts := parseMillisString(d.CreatedTimestamp)
		out = append(out, InboundMessage{
			TenantID:         tenant,
			AccountID:        w.ForUserID,
			Channel:          "x",
			Kind:             "dm",
			CustomerExternal: d.MessageCreate.SenderID,
			ConversationKey:  conversationKey(d.MessageCreate.SenderID, d.MessageCreate.Target.RecipientID),
			PlatformMsgID:    d.ID,
			Body:             d.MessageCreate.MessageData.Text,
			OccurredAt:       ts,
		})
	}

	for _, f := range w.FavoriteEvents {
		out = append(out, InboundMessage{
			TenantID:         tenant,
			AccountID:        w.ForUserID,
			Channel:          "x",
			Kind:             "favorite",
			CustomerExternal: f.User.ID,
			CustomerHandle:   f.User.ScreenName,
			ConversationKey:  f.FavoritedStatus.ID,
			PlatformMsgID:    f.ID,
			OccurredAt:       parseTwitterTime(f.CreatedAt),
		})
	}

	for _, fl := range w.FollowEvents {
		ts := parseMillisString(fl.CreatedTimestamp)
		out = append(out, InboundMessage{
			TenantID:         tenant,
			AccountID:        w.ForUserID,
			Channel:          "x",
			Kind:             fl.Type, // "follow" or "unfollow"
			CustomerExternal: fl.Source.ID,
			CustomerHandle:   fl.Source.ScreenName,
			ConversationKey:  fl.Source.ID,
			OccurredAt:       ts,
		})
	}

	return out
}

// classifyTweet labels a TweetEvent as reply, quote, or mention. The
// rules match common social-customer-care taxonomy: an explicit
// `in_reply_to_status_id` => reply; presence of `quoted_status` =>
// quote; otherwise it's a mention surfaced to us by AAA.
func classifyTweet(t TweetEvent) string {
	switch {
	case t.InReplyToStatusID != "":
		return "reply"
	case t.QuotedStatus != nil:
		return "quote"
	default:
		return "mention"
	}
}

// conversationKey returns a stable, order-independent key for the DM
// thread between two users.
func conversationKey(a, b string) string {
	if a < b {
		return a + ":" + b
	}
	return b + ":" + a
}

// parseTwitterTime parses the classic v1.1 "created_at" format. Returns
// zero time on parse failure (we still publish the event).
func parseTwitterTime(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	if t, err := time.Parse(time.RubyDate, s); err == nil {
		return t.UTC()
	}
	if t, err := time.Parse("Mon Jan 02 15:04:05 -0700 2006", s); err == nil {
		return t.UTC()
	}
	return time.Time{}
}

// parseMillisString parses an int-as-string milliseconds-since-epoch.
func parseMillisString(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	var ms int64
	for _, c := range s {
		if c < '0' || c > '9' {
			return time.Time{}
		}
		ms = ms*10 + int64(c-'0')
	}
	return time.UnixMilli(ms).UTC()
}
