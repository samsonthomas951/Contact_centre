package whatsapp

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func sign(body []byte, secret string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

type fakeResolver map[string]string

func (f fakeResolver) ResolveTenant(phoneID string) (string, bool) {
	t, ok := f[phoneID]
	return t, ok
}

const samplePayload = `{
  "object": "whatsapp_business_account",
  "entry": [{
    "id": "WABA_1",
    "changes": [{
      "field": "messages",
      "value": {
        "messaging_product": "whatsapp",
        "metadata": {"display_phone_number":"+254700111222","phone_number_id":"PHONE_1"},
        "contacts": [{"wa_id":"254799000111","profile":{"name":"Jane"}}],
        "messages": [
          {"id":"wamid.A","from":"254799000111","timestamp":"1736942400","type":"text",
           "text":{"body":"My order is late"}},
          {"id":"wamid.B","from":"254799000111","timestamp":"1736942460","type":"document",
           "document":{"id":"MEDIA_1","mime_type":"application/pdf","filename":"receipt.pdf","caption":"Here"}}
        ],
        "statuses": [
          {"id":"wamid.OUT","recipient_id":"254799000111","status":"delivered","timestamp":"1736942500"}
        ]
      }
    }]
  }]
}`

func TestNormalize_HappyPath(t *testing.T) {
	var w Webhook
	if err := json.Unmarshal([]byte(samplePayload), &w); err != nil {
		t.Fatal(err)
	}
	got := Normalize(w, fakeResolver{"PHONE_1": "tenant-acme"}.ResolveTenant)
	if len(got) != 3 {
		t.Fatalf("len=%d want 3 (2 messages + 1 status); got %+v", len(got), got)
	}
	if got[0].Kind != "message" || got[0].Body != "My order is late" {
		t.Errorf("[0] mis-normalised: %+v", got[0])
	}
	if got[0].CustomerName != "Jane" {
		t.Errorf("contact name not joined: %+v", got[0])
	}
	if got[1].Kind != "message" || got[1].MediaID != "MEDIA_1" || got[1].MediaMime != "application/pdf" {
		t.Errorf("media event mis-normalised: %+v", got[1])
	}
	if got[2].Kind != "status" || got[2].StatusKind != "delivered" {
		t.Errorf("status mis-normalised: %+v", got[2])
	}
}

func TestNormalize_UnknownPhoneIDDropped(t *testing.T) {
	var w Webhook
	_ = json.Unmarshal([]byte(samplePayload), &w)
	if got := Normalize(w, fakeResolver{}.ResolveTenant); len(got) != 0 {
		t.Fatalf("unknown phone id should be dropped, got %+v", got)
	}
}

func TestNormalize_RejectsNonWABAObject(t *testing.T) {
	w := Webhook{Object: "page"}
	if got := Normalize(w, fakeResolver{"x": "y"}.ResolveTenant); got != nil {
		t.Errorf("non-WABA object should yield nil, got %v", got)
	}
}

func TestWebhook_SubscriptionVerify(t *testing.T) {
	h := &WebhookHandler{VerifyTok: "secret"}
	req := httptest.NewRequest(http.MethodGet,
		"/v1/wa/webhook?hub.mode=subscribe&hub.verify_token=secret&hub.challenge=ABC", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != 200 || rr.Body.String() != "ABC" {
		t.Errorf("verify failed: %d %q", rr.Code, rr.Body.String())
	}
}

func TestWebhook_EventInvalidSignature(t *testing.T) {
	h := &WebhookHandler{AppSecret: "shh", Resolver: fakeResolver{}}
	req := httptest.NewRequest(http.MethodPost, "/v1/wa/webhook",
		strings.NewReader(`{"object":"whatsapp_business_account","entry":[]}`))
	req.Header.Set("X-Hub-Signature-256", "sha256=deadbeef")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("want 401, got %d", rr.Code)
	}
}

func TestWebhook_EventGoodSigUnknownPhoneStill200(t *testing.T) {
	h := &WebhookHandler{AppSecret: "shh", Resolver: fakeResolver{}}
	body := []byte(`{"object":"whatsapp_business_account","entry":[{"id":"x","changes":[]}]}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/wa/webhook", strings.NewReader(string(body)))
	req.Header.Set("X-Hub-Signature-256", sign(body, "shh"))
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rr.Code)
	}
}

func TestRecommend_WindowOpenVsClosed(t *testing.T) {
	now := time.Now()
	if Recommend(now.Add(-1*time.Hour), now) != StrategyService {
		t.Error("inside 24h window should be service")
	}
	if Recommend(now.Add(-25*time.Hour), now) != StrategyTemplate {
		t.Error("past 24h window should require template")
	}
	if Recommend(time.Time{}, now) != StrategyTemplate {
		t.Error("never-replied window should require template")
	}
}
