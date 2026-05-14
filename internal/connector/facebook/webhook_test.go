package facebook

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type fakeResolver map[string]string

func (f fakeResolver) ResolveTenant(pageID string) (string, bool) {
	t, ok := f[pageID]
	return t, ok
}

func TestWebhook_SubscriptionVerify(t *testing.T) {
	h := &WebhookHandler{VerifyTok: "secret-verify"}

	t.Run("good token echoes challenge", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet,
			"/v1/fb/webhook?hub.mode=subscribe&hub.verify_token=secret-verify&hub.challenge=ABC", nil)
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req)
		if rr.Code != http.StatusOK || rr.Body.String() != "ABC" {
			t.Errorf("verify: code=%d body=%q", rr.Code, rr.Body.String())
		}
	})

	t.Run("wrong token rejected", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet,
			"/v1/fb/webhook?hub.mode=subscribe&hub.verify_token=wrong&hub.challenge=ABC", nil)
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req)
		if rr.Code != http.StatusForbidden {
			t.Errorf("want 403, got %d", rr.Code)
		}
	})

	t.Run("non-subscribe mode rejected", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet,
			"/v1/fb/webhook?hub.mode=ping&hub.verify_token=secret-verify", nil)
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req)
		if rr.Code != http.StatusBadRequest {
			t.Errorf("want 400, got %d", rr.Code)
		}
	})
}

func TestWebhook_EventInvalidSignature(t *testing.T) {
	h := &WebhookHandler{
		AppSecret: "shh",
		Resolver:  fakeResolver{"PAGE": "tenant"},
	}
	body := strings.NewReader(`{"object":"page","entry":[]}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/fb/webhook", body)
	req.Header.Set(SignatureHeader, "sha256=deadbeef")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("want 401, got %d", rr.Code)
	}
}

func TestWebhook_EventGoodSigUnknownPageStill200(t *testing.T) {
	// We accept (200 OK) signed payloads even when the Page isn't in our
	// resolver — Meta retries 5xx, and silently dropping unknown Pages
	// is the right behaviour during onboarding/teardown.
	h := &WebhookHandler{
		AppSecret: "shh",
		Resolver:  fakeResolver{},
	}
	raw := []byte(`{"object":"page","entry":[{"id":"PAGE","time":1,"messaging":[]}]}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/fb/webhook", strings.NewReader(string(raw)))
	req.Header.Set(SignatureHeader, signFn(raw, "shh"))
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Errorf("want 200, got %d (body=%q)", rr.Code, rr.Body.String())
	}
}

// signFn duplicates the test helper from signature_test.go so we don't
// rely on cross-file unexported helpers.
func signFn(body []byte, secret string) string {
	return sign(body, secret)
}
