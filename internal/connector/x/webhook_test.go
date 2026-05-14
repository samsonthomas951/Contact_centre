package x

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type fakeResolver map[string]string

func (f fakeResolver) ResolveTenant(acct string) (string, bool) {
	t, ok := f[acct]
	return t, ok
}

func TestWebhook_CRC(t *testing.T) {
	h := &WebhookHandler{ConsumerSecret: "shh"}

	t.Run("missing token", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/v1/x/webhook", nil)
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req)
		if rr.Code != http.StatusBadRequest {
			t.Fatalf("got %d", rr.Code)
		}
	})

	t.Run("echoes valid signed token", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/v1/x/webhook?crc_token=abc123", nil)
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("got %d", rr.Code)
		}
		var resp map[string]string
		if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
			t.Fatal(err)
		}
		want := CRCResponseToken("abc123", "shh")
		if resp["response_token"] != want {
			t.Errorf("response_token = %s, want %s", resp["response_token"], want)
		}
	})
}

func TestWebhook_EventInvalidSignature(t *testing.T) {
	h := &WebhookHandler{ConsumerSecret: "shh", Resolver: fakeResolver{}}
	req := httptest.NewRequest(http.MethodPost, "/v1/x/webhook",
		strings.NewReader(`{"for_user_id":"42"}`))
	req.Header.Set("x-twitter-webhooks-signature", "sha256=ZmFrZQ==")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("got %d", rr.Code)
	}
}

func TestWebhook_EventGoodSigUnknownTenantStill200(t *testing.T) {
	h := &WebhookHandler{ConsumerSecret: "shh", Resolver: fakeResolver{}}
	body := []byte(`{"for_user_id":"NOT_KNOWN"}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/x/webhook", strings.NewReader(string(body)))
	req.Header.Set("x-twitter-webhooks-signature", sha256B64(body, "shh"))
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("got %d (body=%q)", rr.Code, rr.Body.String())
	}
}
