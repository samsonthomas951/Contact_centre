package instagram

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func sign(body []byte, secret string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

type fakeResolver map[string]string

func (f fakeResolver) ResolveTenant(igUserID string) (string, bool) {
	t, ok := f[igUserID]
	return t, ok
}

const samplePayload = `{
  "object": "instagram",
  "entry": [{
    "id": "IG_KNOWN",
    "time": 1736942400000,
    "messaging": [
      {
        "sender":{"id":"USR1"},
        "recipient":{"id":"IG_KNOWN"},
        "timestamp": 1736942400123,
        "message":{"mid":"m.1","text":"Hi"}
      },
      {
        "sender":{"id":"IG_KNOWN"},
        "recipient":{"id":"USR1"},
        "timestamp": 1736942401000,
        "message":{"mid":"m.echo","text":"our reply","is_echo":true}
      },
      {
        "sender":{"id":"USR1"},
        "recipient":{"id":"IG_KNOWN"},
        "timestamp": 1736942402000,
        "read":{"mid":"m.1"}
      }
    ],
    "changes": [
      {
        "field": "comments",
        "value": {
          "id": "c.1",
          "text": "Looks great!",
          "media_id": "MEDIA_42",
          "from": {"id":"USR2","username":"bob"},
          "created_time": 1736942403
        }
      },
      {
        "field": "mentions",
        "value": {"comment_id":"c.2","media_id":"MEDIA_42"}
      }
    ]
  },
  {
    "id": "IG_UNKNOWN",
    "time": 0,
    "messaging": [{"sender":{"id":"x"},"recipient":{"id":"IG_UNKNOWN"},"timestamp":0,"message":{"mid":"x"}}]
  }]
}`

func TestNormalize_HappyPath(t *testing.T) {
	var w Webhook
	if err := json.Unmarshal([]byte(samplePayload), &w); err != nil {
		t.Fatal(err)
	}
	got := Normalize(w, fakeResolver{"IG_KNOWN": "tenant-acme"}.ResolveTenant)
	// Expected: 1 dm + 1 read + 1 comment + 1 mention = 4. Echo dropped.
	// Unknown IG account event dropped.
	if len(got) != 4 {
		t.Fatalf("len=%d want 4 (echo + unknown dropped); got %+v", len(got), got)
	}
	if got[0].Kind != "dm" || got[0].Body != "Hi" {
		t.Errorf("[0] mis-normalised: %+v", got[0])
	}
	if got[1].Kind != "read" {
		t.Errorf("[1] mis-normalised: %+v", got[1])
	}
	if got[2].Kind != "comment" || got[2].MediaID != "MEDIA_42" || got[2].CustomerHandle != "bob" {
		t.Errorf("[2] mis-normalised: %+v", got[2])
	}
	if got[3].Kind != "mention" || got[3].MediaID != "MEDIA_42" || got[3].PlatformMsgID != "c.2" {
		t.Errorf("[3] mis-normalised: %+v", got[3])
	}
}

func TestNormalize_RejectsNonInstagramObject(t *testing.T) {
	if got := Normalize(Webhook{Object: "page"},
		fakeResolver{"x": "y"}.ResolveTenant); got != nil {
		t.Errorf("non-instagram object should yield nil, got %v", got)
	}
}

func TestWebhook_SubscriptionVerify(t *testing.T) {
	h := &WebhookHandler{VerifyTok: "secret"}
	req := httptest.NewRequest(http.MethodGet,
		"/v1/ig/webhook?hub.mode=subscribe&hub.verify_token=secret&hub.challenge=XYZ", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != 200 || rr.Body.String() != "XYZ" {
		t.Errorf("verify failed: %d %q", rr.Code, rr.Body.String())
	}
}

func TestWebhook_EventInvalidSignature(t *testing.T) {
	h := &WebhookHandler{AppSecret: "shh", Resolver: fakeResolver{}}
	req := httptest.NewRequest(http.MethodPost, "/v1/ig/webhook",
		strings.NewReader(`{"object":"instagram","entry":[]}`))
	req.Header.Set("X-Hub-Signature-256", "sha256=deadbeef")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("want 401 got %d", rr.Code)
	}
}

func TestWebhook_EventGoodSigUnknownIDStill200(t *testing.T) {
	h := &WebhookHandler{AppSecret: "shh", Resolver: fakeResolver{}}
	body := []byte(`{"object":"instagram","entry":[{"id":"IG","time":1,"messaging":[]}]}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/ig/webhook", strings.NewReader(string(body)))
	req.Header.Set("X-Hub-Signature-256", sign(body, "shh"))
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rr.Code)
	}
}
