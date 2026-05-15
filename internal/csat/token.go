// Package csat owns the post-resolution survey workflow: generate a
// survey on ticket close, deliver the link over the original channel,
// receive the score on a public token-authenticated endpoint, roll
// the response into mart_tickets_daily.
//
// The link goes out over the same channel the customer used to reach
// us so we hit them where they're already engaged with the brand.
// Voice falls back to SMS where the carrier supports it.
package csat

import (
	"crypto/rand"
	"encoding/base64"
)

// TokenSize is the entropy of the survey access token in bytes.
// 16 bytes -> 22 base64url chars -> 128 bits, safe against guessing
// at the rate-limited public endpoint.
const TokenSize = 16

// NewToken returns a URL-safe base64 token of TokenSize bytes
// entropy. Suitable for embedding in the survey link path.
func NewToken() string {
	var b [TokenSize]byte
	_, _ = rand.Read(b[:])
	return base64.RawURLEncoding.EncodeToString(b[:])
}
