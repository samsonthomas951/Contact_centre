package voice

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
)

func TestATEvent_KindClassifier(t *testing.T) {
	cases := []struct {
		name string
		e    ATEvent
		want string
	}{
		{"first inbound", ATEvent{SessionID: "s", IsActive: true}, "ringing"},
		{"dtmf 1 consent", ATEvent{SessionID: "s", IsActive: true, DTMFDigits: "1"}, "consent"},
		{"dtmf 2 decline", ATEvent{SessionID: "s", IsActive: true, DTMFDigits: "2"}, "consent"},
		{"hangup", ATEvent{SessionID: "s", IsActive: false}, "ended"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.e.Kind(); got != tc.want {
				t.Errorf("got %q want %q", got, tc.want)
			}
		})
	}
}

func TestConsentPrompt_EscapesXML(t *testing.T) {
	out := ConsentPrompt("https://x.example/cb?a=1&b=<2>", "Press 1 if & only if")
	if strings.Contains(out, "&b=<2>") || !strings.Contains(out, "&amp;b=&lt;2&gt;") {
		t.Fatalf("XML not escaped in URL: %s", out)
	}
	if !strings.Contains(out, "&amp; only if") {
		t.Fatalf("XML not escaped in say text: %s", out)
	}
	if !strings.HasPrefix(out, `<?xml`) {
		t.Fatal("missing prolog")
	}
}

type stubNumbers struct {
	tenant, vnum, dial string
	ok                 bool
}

func (s stubNumbers) ResolveNumber(_ context.Context, _, _, secret string) (string, string, string, bool, error) {
	if !s.ok || secret == "" {
		return "", "", "", false, nil
	}
	return s.tenant, s.vnum, s.dial, true, nil
}

func TestWebhook_RingingReturnsConsentXML(t *testing.T) {
	r := chi.NewRouter()
	r.Post("/v1/voice/at/{secret}", (&WebhookHandler{
		Numbers:            stubNumbers{tenant: "t", vnum: "v", dial: "+254700111222", ok: true},
		ConsentCallbackURL: "https://app.example/v1/voice/at/abc",
	}).ServeHTTP)

	form := url.Values{}
	form.Set("sessionId", "ATSession001")
	form.Set("isActive", "1")
	form.Set("direction", "Inbound")
	form.Set("callerNumber", "+254799000111")
	form.Set("destinationNumber", "+254700111222")
	req := httptest.NewRequest(http.MethodPost, "/v1/voice/at/abc",
		strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("code=%d body=%q", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "<GetDigits") {
		t.Errorf("ringing should return GetDigits IVR: %s", rr.Body.String())
	}
	if rr.Header().Get("Content-Type") != "application/xml" {
		t.Errorf("Content-Type = %s", rr.Header().Get("Content-Type"))
	}
}

func TestWebhook_DTMF1BridgesWithRecording(t *testing.T) {
	r := chi.NewRouter()
	r.Post("/v1/voice/at/{secret}", (&WebhookHandler{
		Numbers: stubNumbers{tenant: "t", vnum: "v", dial: "+254700111222", ok: true},
	}).ServeHTTP)

	form := url.Values{}
	form.Set("sessionId", "ATSession001")
	form.Set("isActive", "1")
	form.Set("dtmfDigits", "1")
	form.Set("callerNumber", "+254799000111")
	form.Set("destinationNumber", "+254700111222")
	req := httptest.NewRequest(http.MethodPost, "/v1/voice/at/abc",
		strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	body := rr.Body.String()
	if !strings.Contains(body, `record="true"`) {
		t.Errorf("DTMF=1 should bridge with record=true: %s", body)
	}
	if !strings.Contains(body, "+254700111222") {
		t.Errorf("missing agent dial number: %s", body)
	}
}

func TestWebhook_DTMF2BridgesWithoutRecording(t *testing.T) {
	r := chi.NewRouter()
	r.Post("/v1/voice/at/{secret}", (&WebhookHandler{
		Numbers: stubNumbers{tenant: "t", vnum: "v", dial: "+254700111222", ok: true},
	}).ServeHTTP)

	form := url.Values{}
	form.Set("sessionId", "ATSession001")
	form.Set("isActive", "1")
	form.Set("dtmfDigits", "2")
	form.Set("callerNumber", "+254799000111")
	form.Set("destinationNumber", "+254700111222")
	req := httptest.NewRequest(http.MethodPost, "/v1/voice/at/abc",
		strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	if !strings.Contains(rr.Body.String(), `record="false"`) {
		t.Errorf("DTMF=2 should bridge with record=false: %s", rr.Body.String())
	}
}

func TestWebhook_UnknownSecretReturns403(t *testing.T) {
	r := chi.NewRouter()
	r.Post("/v1/voice/at/{secret}", (&WebhookHandler{
		Numbers: stubNumbers{ok: false},
	}).ServeHTTP)
	form := url.Values{"sessionId": []string{"S1"}}
	req := httptest.NewRequest(http.MethodPost, "/v1/voice/at/bogus",
		strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("code=%d want 403", rr.Code)
	}
}
