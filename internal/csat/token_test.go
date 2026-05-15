package csat

import (
	"encoding/base64"
	"testing"
)

func TestNewToken_LengthAndAlphabet(t *testing.T) {
	for range 100 {
		tok := NewToken()
		// 16 bytes -> ceil(16*8/6)=22 base64url chars (no padding).
		if len(tok) != 22 {
			t.Fatalf("token length = %d, want 22 (got %q)", len(tok), tok)
		}
		raw, err := base64.RawURLEncoding.DecodeString(tok)
		if err != nil {
			t.Fatalf("token not URL-safe base64: %v (%q)", err, tok)
		}
		if len(raw) != TokenSize {
			t.Fatalf("decoded length = %d, want %d", len(raw), TokenSize)
		}
	}
}

func TestNewToken_Unique(t *testing.T) {
	seen := make(map[string]struct{}, 1000)
	for i := range 1000 {
		tok := NewToken()
		if _, dup := seen[tok]; dup {
			t.Fatalf("duplicate token after %d draws: %q", i, tok)
		}
		seen[tok] = struct{}{}
	}
}
