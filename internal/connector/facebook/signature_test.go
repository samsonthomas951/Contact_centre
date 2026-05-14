package facebook

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"testing"
)

func sign(body []byte, secret string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

func TestVerifySignature_HappyPath(t *testing.T) {
	body := []byte(`{"object":"page","entry":[]}`)
	header := sign(body, "shh")
	if !VerifySignature(body, header, "shh") {
		t.Fatal("valid signature rejected")
	}
}

func TestVerifySignature_Rejects(t *testing.T) {
	body := []byte(`{"x":1}`)
	good := sign(body, "shh")

	cases := []struct {
		name   string
		body   []byte
		header string
		secret string
	}{
		{"wrong secret", body, good, "different"},
		{"flipped body", []byte(`{"x":2}`), good, "shh"},
		{"missing prefix", body, good[len("sha256="):], "shh"},
		{"non-hex value", body, "sha256=zzzz", "shh"},
		{"empty header", body, "", "shh"},
		{"truncated", body, good[:20], "shh"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if VerifySignature(tc.body, tc.header, tc.secret) {
				t.Errorf("invalid signature accepted: %+v", tc)
			}
		})
	}
}
