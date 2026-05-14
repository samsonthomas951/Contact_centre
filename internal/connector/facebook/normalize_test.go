package facebook

import (
	"encoding/json"
	"testing"
)

const samplePayload = `{
  "object": "page",
  "entry": [
    {
      "id": "PAGE_KNOWN",
      "time": 1700000000000,
      "messaging": [
        {
          "sender":    {"id": "PSID_1"},
          "recipient": {"id": "PAGE_KNOWN"},
          "timestamp": 1700000000123,
          "message":   {"mid": "mid.1", "text": "hello"}
        },
        {
          "sender":    {"id": "PSID_1"},
          "recipient": {"id": "PAGE_KNOWN"},
          "timestamp": 1700000001000,
          "postback":  {"mid": "mid.2", "title": "Talk to human", "payload": "AGENT_REQUEST"}
        }
      ]
    },
    {
      "id": "PAGE_UNKNOWN",
      "time": 1700000000000,
      "messaging": [
        {"sender":{"id":"PSID_X"},"recipient":{"id":"PAGE_UNKNOWN"},"timestamp":1,"message":{"mid":"x","text":"x"}}
      ]
    }
  ]
}`

func TestNormalize_HappyPath(t *testing.T) {
	var w Webhook
	if err := json.Unmarshal([]byte(samplePayload), &w); err != nil {
		t.Fatal(err)
	}
	resolve := func(pageID string) (string, bool) {
		if pageID == "PAGE_KNOWN" {
			return "tenant-acme", true
		}
		return "", false
	}

	got := Normalize(w, resolve)
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2 (unknown page should be dropped)", len(got))
	}
	if got[0].Kind != "message" || got[0].Body != "hello" || got[0].PlatformMsgID != "mid.1" {
		t.Errorf("event[0] mis-normalised: %+v", got[0])
	}
	if got[1].Kind != "postback" || got[1].Postback != "AGENT_REQUEST" {
		t.Errorf("event[1] mis-normalised: %+v", got[1])
	}
	for _, e := range got {
		if e.TenantID != "tenant-acme" {
			t.Errorf("tenant not resolved: %+v", e)
		}
		if e.Channel != "fb" {
			t.Errorf("channel = %s, want fb", e.Channel)
		}
		if e.OccurredAt.IsZero() {
			t.Errorf("occurred_at zero")
		}
	}
}

func TestNormalize_RejectsNonPageObject(t *testing.T) {
	w := Webhook{Object: "user"}
	if got := Normalize(w, func(string) (string, bool) { return "x", true }); got != nil {
		t.Errorf("non-page object should produce nil, got %v", got)
	}
}
