package document

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestParseVerdict(t *testing.T) {
	cases := []struct {
		raw       string
		clean     bool
		signature string
	}{
		{"stream: OK\x00", true, ""},
		{"stream: Win.Test.EICAR_HDB-1 FOUND\x00", false, "Win.Test.EICAR_HDB-1"},
		{"stream: ERROR\x00", false, ""},
	}
	for _, tc := range cases {
		v := parseVerdict(tc.raw)
		if v.Clean != tc.clean {
			t.Errorf("%q clean=%v want %v", tc.raw, v.Clean, tc.clean)
		}
		if v.Signature != tc.signature {
			t.Errorf("%q signature=%q want %q", tc.raw, v.Signature, tc.signature)
		}
	}
}

func TestScan_NoAddrIsErr(t *testing.T) {
	s := &ClamAVScanner{}
	_, err := s.Scan(context.Background(), strings.NewReader("hi"))
	if !errors.Is(err, ErrNoClamAV) {
		t.Fatalf("err = %v, want ErrNoClamAV", err)
	}
}
