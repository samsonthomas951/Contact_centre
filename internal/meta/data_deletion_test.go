package meta

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
)

// helper: build a wire-shape signed_request that should verify.
func makeSigned(t *testing.T, secret string, sr SignedRequest) string {
	t.Helper()
	payload, err := json.Marshal(sr)
	if err != nil {
		t.Fatal(err)
	}
	enc := base64.RawURLEncoding.EncodeToString(payload)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(enc))
	sig := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	return sig + "." + enc
}

func TestParseSignedRequest_Valid(t *testing.T) {
	secret := "test-app-secret"
	want := SignedRequest{UserID: "PSID_001", Algorithm: "HMAC-SHA256", IssuedAt: 1700000000}
	tok := makeSigned(t, secret, want)

	got, err := ParseSignedRequest(tok, secret)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if got.UserID != want.UserID {
		t.Errorf("user_id = %q, want %q", got.UserID, want.UserID)
	}
}

func TestParseSignedRequest_BadSig(t *testing.T) {
	secret := "test-app-secret"
	tok := makeSigned(t, secret, SignedRequest{UserID: "PSID_001", Algorithm: "HMAC-SHA256"})
	// Tamper signature.
	parts := strings.SplitN(tok, ".", 2)
	tok = "AAAA" + parts[0][4:] + "." + parts[1]

	if _, err := ParseSignedRequest(tok, secret); err == nil {
		t.Fatal("expected signature mismatch, got nil")
	}
}

func TestParseSignedRequest_DifferentSecret(t *testing.T) {
	tok := makeSigned(t, "real-secret", SignedRequest{UserID: "X", Algorithm: "HMAC-SHA256"})
	if _, err := ParseSignedRequest(tok, "attacker-guess"); err == nil {
		t.Fatal("expected verify failure under wrong secret, got nil")
	}
}

func TestParseSignedRequest_Malformed(t *testing.T) {
	cases := []string{"", "nodot", "only.one.part.is.also.bad."}
	for _, c := range cases {
		if _, err := ParseSignedRequest(c, "s"); err == nil {
			t.Errorf("expected error on %q, got nil", c)
		}
	}
}

func TestParseSignedRequest_RejectsUnknownAlg(t *testing.T) {
	secret := "s"
	tok := makeSigned(t, secret, SignedRequest{UserID: "X", Algorithm: "SHA1"})
	if _, err := ParseSignedRequest(tok, secret); err == nil {
		t.Fatal("expected error on non-HMAC-SHA256 algorithm")
	}
}

func TestParseSignedRequest_NoSecret(t *testing.T) {
	if _, err := ParseSignedRequest("ignored.parts", ""); err == nil {
		t.Fatal("expected error with empty app secret")
	}
}
