package x

import (
	"encoding/json"
	"testing"
)

const samplePayload = `{
  "for_user_id": "ACCT_KNOWN",
  "tweet_create_events": [
    {
      "id_str": "T1",
      "created_at": "Wed Jan 15 12:00:00 +0000 2025",
      "text": "Hey @brand my account is locked",
      "user": {"id_str":"USR1","screen_name":"alice","name":"Alice"},
      "entities": {"user_mentions":[{"id_str":"ACCT_KNOWN","screen_name":"brand"}]}
    },
    {
      "id_str": "T2",
      "created_at": "Wed Jan 15 12:01:00 +0000 2025",
      "text": "@brand any update?",
      "user": {"id_str":"USR1","screen_name":"alice","name":"Alice"},
      "in_reply_to_status_id_str": "T1",
      "in_reply_to_user_id_str": "ACCT_KNOWN"
    }
  ],
  "direct_message_events": [
    {
      "id": "DM1",
      "created_timestamp": "1736942400000",
      "type": "message_create",
      "message_create": {
        "sender_id": "USR1",
        "target": {"recipient_id":"ACCT_KNOWN"},
        "message_data": {"text":"Hi I cant log in"}
      }
    }
  ],
  "favorite_events": [
    {
      "id": "FAV1",
      "created_at": "Wed Jan 15 13:00:00 +0000 2025",
      "favorited_status": {"id_str":"OUR_TWEET_42"},
      "user": {"id_str":"USR2","screen_name":"bob","name":"Bob"}
    }
  ],
  "follow_events": [
    {
      "type": "follow",
      "created_timestamp": "1736946000000",
      "target": {"id_str":"ACCT_KNOWN","screen_name":"brand","name":"Brand"},
      "source": {"id_str":"USR3","screen_name":"carol","name":"Carol"}
    }
  ]
}`

func TestNormalize_HappyPath(t *testing.T) {
	var w AAAWebhook
	if err := json.Unmarshal([]byte(samplePayload), &w); err != nil {
		t.Fatal(err)
	}
	resolve := func(acct string) (string, bool) {
		if acct == "ACCT_KNOWN" {
			return "tenant-acme", true
		}
		return "", false
	}
	got := Normalize(w, resolve)
	if len(got) != 5 {
		t.Fatalf("len = %d, want 5 (2 tweets + 1 DM + 1 favorite + 1 follow); got %+v", len(got), got)
	}

	// First tweet has no in_reply_to => "mention".
	if got[0].Kind != "mention" || got[0].PlatformMsgID != "T1" || got[0].ConversationKey != "T1" {
		t.Errorf("event[0] mis-normalised: %+v", got[0])
	}
	// Second tweet replies to T1 => "reply" with conversation_key=T1.
	if got[1].Kind != "reply" || got[1].ConversationKey != "T1" {
		t.Errorf("event[1] mis-normalised: %+v", got[1])
	}
	// DM: conversation_key is order-independent.
	if got[2].Kind != "dm" || got[2].Body != "Hi I cant log in" {
		t.Errorf("dm mis-normalised: %+v", got[2])
	}
	if got[2].ConversationKey != conversationKey("USR1", "ACCT_KNOWN") {
		t.Errorf("dm conversation_key = %s, want canonical pair", got[2].ConversationKey)
	}
	if got[3].Kind != "favorite" || got[3].ConversationKey != "OUR_TWEET_42" {
		t.Errorf("favorite mis-normalised: %+v", got[3])
	}
	if got[4].Kind != "follow" || got[4].CustomerHandle != "carol" {
		t.Errorf("follow mis-normalised: %+v", got[4])
	}

	for _, e := range got {
		if e.TenantID != "tenant-acme" || e.Channel != "x" || e.AccountID != "ACCT_KNOWN" {
			t.Errorf("metadata: %+v", e)
		}
		if e.OccurredAt.IsZero() {
			t.Errorf("occurred_at zero: %+v", e)
		}
	}
}

func TestNormalize_UnknownAccountDropped(t *testing.T) {
	w := AAAWebhook{ForUserID: "WHO?"}
	if got := Normalize(w, func(string) (string, bool) { return "", false }); got != nil {
		t.Errorf("want nil for unknown account, got %v", got)
	}
}

func TestConversationKey_OrderIndependent(t *testing.T) {
	a, b := "100", "200"
	if conversationKey(a, b) != conversationKey(b, a) {
		t.Fatal("conversation_key should be order-independent")
	}
}

func TestParseMillisString(t *testing.T) {
	got := parseMillisString("1736942400000")
	if got.IsZero() {
		t.Fatal("zero time")
	}
	if !parseMillisString("not-a-number").IsZero() {
		t.Fatal("non-numeric input should yield zero time")
	}
}
