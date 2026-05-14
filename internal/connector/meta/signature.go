// Package meta holds primitives that every Meta-family connector
// (Facebook Messenger, Instagram Business, WhatsApp Cloud API) reuses.
// The signature scheme is identical across all three: HMAC-SHA256 over
// the *raw* request body, hex-encoded, with the `sha256=` prefix, sent
// in the `X-Hub-Signature-256` header.
//
// Per §17 of the technical plan: middleware that re-encodes JSON
// between read and verify destroys byte equivalence and the
// verification silently fails open. Always read the raw body before any
// JSON parsing.
package meta

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
)

// SignatureHeader is the HTTP header Meta sets on every webhook delivery.
const SignatureHeader = "X-Hub-Signature-256"

// VerifySignature returns true when header is "sha256=<hex>" and the
// hex value equals HMAC-SHA256(rawBody, appSecret) in constant time.
// Returns false on any malformed input rather than panicking so the
// caller can convert "false" directly to a 401.
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
	return hmac.Equal(mac.Sum(nil), provided)
}
