// Package facebook implements the Meta Messenger / Facebook Page
// connector described in §4.1 of the technical plan.
//
// The webhook intake MUST verify Meta's HMAC-SHA256 signature against
// the *raw* request body before any JSON parsing — middleware that
// re-encodes the body destroys byte equivalence and the verification
// silently fails open. See §17 ("Webhook signature edge case").
package facebook

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
)

// SignatureHeader is the HTTP header Meta sets on every webhook delivery.
const SignatureHeader = "X-Hub-Signature-256"

// VerifySignature returns true when header is "sha256=<hex>" and the hex
// value equals HMAC-SHA256(rawBody, appSecret) in constant time. It
// returns false on any malformed input rather than panicking, so a
// caller can convert "false" directly to a 401 response.
func VerifySignature(rawBody []byte, header, appSecret string) bool {
	const prefix = "sha256="
	if len(header) <= len(prefix) || header[:len(prefix)] != prefix {
		return false
	}
	provided, err := hex.DecodeString(header[len(prefix):])
	if err != nil {
		return false
	}

	mac := hmac.New(sha256.New, []byte(appSecret))
	mac.Write(rawBody)
	expected := mac.Sum(nil)

	return hmac.Equal(expected, provided)
}
