// Package x implements the X (Twitter) connector described in §4.2 of
// the technical plan.
//
// Two distinct authentication patterns coexist in this package:
//
//  1. Webhook intake (Account Activity API). X periodically GETs the
//     webhook URL with a `crc_token` query parameter; we must respond
//     with HMAC-SHA256(crc_token, consumer_secret) base64-encoded under
//     the `response_token` JSON key. Failing the CRC unregisters the
//     webhook silently. POST deliveries carry a `x-twitter-webhooks-
//     signature` header that we verify the same way.
//
//  2. Outbound replies. Implemented in oauth.go. v2 endpoints accept
//     OAuth 2.0 PKCE bearer tokens; legacy AAA management endpoints
//     still need OAuth 1.0a HMAC-SHA1 — we keep both paths because
//     subscribing/unsubscribing the webhook URL is OAuth1-only.
package x

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
)

// CRCResponseToken returns the value to put in the JSON body of the GET
// CRC response: `{"response_token":"sha256=<base64>"}`.
//
// Per X's developer docs the format is:
//   sha256= base64( HMAC-SHA256(crc_token, consumer_secret) )
func CRCResponseToken(crcToken, consumerSecret string) string {
	mac := hmac.New(sha256.New, []byte(consumerSecret))
	mac.Write([]byte(crcToken))
	return "sha256=" + base64.StdEncoding.EncodeToString(mac.Sum(nil))
}

// VerifyWebhookSignature checks the x-twitter-webhooks-signature header
// against an HMAC-SHA256 of the raw POST body using the consumer
// secret. Header format mirrors CRCResponseToken's output.
//
// Always verify against the *raw* body — see §17 ("Webhook signature
// edge case"). Constant-time comparison.
func VerifyWebhookSignature(rawBody []byte, header, consumerSecret string) bool {
	const prefix = "sha256="
	if len(header) <= len(prefix) || header[:len(prefix)] != prefix {
		return false
	}
	provided, err := base64.StdEncoding.DecodeString(header[len(prefix):])
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, []byte(consumerSecret))
	mac.Write(rawBody)
	return hmac.Equal(mac.Sum(nil), provided)
}
