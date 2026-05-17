package email

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"testing"
	"time"
)

func TestVerifyMailgunSig_HappyPath(t *testing.T) {
	key := "test-signing-key"
	ts := fmt.Sprint(time.Now().Unix())
	tok := "abc123"
	mac := hmac.New(sha256.New, []byte(key))
	mac.Write([]byte(ts + tok))
	sig := hex.EncodeToString(mac.Sum(nil))

	if !verifyMailgunSig(key, ts, tok, sig) {
		t.Fatal("expected verify to pass")
	}
}

func TestVerifyMailgunSig_WrongKey(t *testing.T) {
	ts, tok := fmt.Sprint(time.Now().Unix()), "x"
	mac := hmac.New(sha256.New, []byte("real-key"))
	mac.Write([]byte(ts + tok))
	sig := hex.EncodeToString(mac.Sum(nil))

	if verifyMailgunSig("attacker-key", ts, tok, sig) {
		t.Fatal("expected verify to fail under wrong key")
	}
}

func TestVerifyMailgunSig_TamperedSig(t *testing.T) {
	key := "k"
	ts, tok := fmt.Sprint(time.Now().Unix()), "y"
	mac := hmac.New(sha256.New, []byte(key))
	mac.Write([]byte(ts + tok))
	sig := hex.EncodeToString(mac.Sum(nil))
	// Flip the last char.
	tampered := sig[:len(sig)-1] + flip(sig[len(sig)-1])
	if verifyMailgunSig(key, ts, tok, tampered) {
		t.Fatal("expected verify to fail on tampered signature")
	}
}

func TestWithinWindow(t *testing.T) {
	now := fmt.Sprint(time.Now().Unix())
	old := fmt.Sprint(time.Now().Add(-20 * time.Minute).Unix())
	if !withinWindow(now, 15*time.Minute) {
		t.Error("now should be inside the window")
	}
	if withinWindow(old, 15*time.Minute) {
		t.Error("20-minutes-ago should be outside a 15-minute window")
	}
}

func flip(b byte) string {
	if b == 'f' {
		return "0"
	}
	return string(b + 1)
}
