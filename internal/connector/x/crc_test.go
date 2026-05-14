package x

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"strings"
	"testing"
)

func sha256B64(body []byte, secret string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return "sha256=" + base64.StdEncoding.EncodeToString(mac.Sum(nil))
}

func TestCRCResponseToken_Format(t *testing.T) {
	tok := CRCResponseToken("hello", "shh")
	if !strings.HasPrefix(tok, "sha256=") {
		t.Fatalf("missing sha256= prefix: %q", tok)
	}
	want := sha256B64([]byte("hello"), "shh")
	if tok != want {
		t.Fatalf("got %q want %q", tok, want)
	}
}

func TestVerifyWebhookSignature(t *testing.T) {
	body := []byte(`{"for_user_id":"42","tweet_create_events":[]}`)
	good := sha256B64(body, "shh")

	cases := []struct {
		name   string
		body   []byte
		header string
		secret string
		ok     bool
	}{
		{"happy", body, good, "shh", true},
		{"wrong secret", body, good, "different", false},
		{"flipped body", []byte(`{"for_user_id":"99"}`), good, "shh", false},
		{"missing prefix", body, good[len("sha256="):], "shh", false},
		{"non-base64", body, "sha256=not!base64!", "shh", false},
		{"empty header", body, "", "shh", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := VerifyWebhookSignature(tc.body, tc.header, tc.secret); got != tc.ok {
				t.Errorf("got %v want %v", got, tc.ok)
			}
		})
	}
}
