// Package webhooks delivers tenant-subscribed events to brand-supplied
// URLs. Every POST is signed; transient failures are retried with
// exponential backoff; permanent failures (4xx other than 408/429)
// stop the delivery and let the brand's own monitoring pick it up.
//
// Signature scheme (intentionally identical to Meta's so a brand
// already using the FB integration can reuse their verification code):
//
//   X-Signature-256: sha256=<hex-HMAC-SHA256(rawBody, secret)>
//   X-Webhook-Id:     <uuid>      // subscription id
//   X-Event-Id:       <string>    // NATS msg id (dedupe key)
//   X-Event-Subject:  ticket.assigned
//   X-Event-Ts:       <RFC3339>
package webhooks

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
)

// SignatureHeader is the header we set on every outbound POST.
const SignatureHeader = "X-Signature-256"

// Sign returns the value of SignatureHeader for the given body + secret.
func Sign(body []byte, secret string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

// Verify is exported as a courtesy for brands consuming our webhooks
// in Go; it matches the Meta verifier semantics so the same call
// pattern works.
func Verify(body []byte, header, secret string) bool {
	const prefix = "sha256="
	if len(header) <= len(prefix) || header[:len(prefix)] != prefix {
		return false
	}
	got, err := hex.DecodeString(header[len(prefix):])
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return hmac.Equal(mac.Sum(nil), got)
}
