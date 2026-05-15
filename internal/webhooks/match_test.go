package webhooks

import "testing"

func TestMatchSubject(t *testing.T) {
	cases := []struct {
		pattern, subject string
		want             bool
	}{
		// literal
		{"ticket.created", "ticket.created", true},
		{"ticket.created", "ticket.assigned", false},

		// single-token *
		{"ticket.*", "ticket.created", true},
		{"ticket.*", "ticket.created.urgent", false}, // * is one token only
		{"*.created", "ticket.created", true},
		{"*.created", "ticket.assigned", false},

		// tail >
		{"ticket.>", "ticket.created", true},
		{"ticket.>", "ticket.first_response.breached", true},
		{"ticket.>", "ticket", false}, // > requires >=1 trailing token
		{"sla.>", "sla.first_response.at_risk", true},

		// edge cases
		{"", "ticket.created", false},
		{"ticket.created", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.pattern+"/"+tc.subject, func(t *testing.T) {
			if got := MatchSubject(tc.pattern, tc.subject); got != tc.want {
				t.Errorf("MatchSubject(%q,%q) = %v want %v", tc.pattern, tc.subject, got, tc.want)
			}
		})
	}
}

func TestMatchAny(t *testing.T) {
	if !MatchAny(nil, "anything") {
		t.Error("nil patterns should match everything")
	}
	if !MatchAny([]string{"ticket.*", "sla.>"}, "sla.resolution.at_risk") {
		t.Error("should match second pattern")
	}
	if MatchAny([]string{"ticket.*"}, "message.new") {
		t.Error("non-matching set should reject")
	}
}

func TestSign_RoundTrip(t *testing.T) {
	body := []byte(`{"hello":"world"}`)
	hdr := Sign(body, "shh")
	if !Verify(body, hdr, "shh") {
		t.Fatal("verify rejected its own signature")
	}
	if Verify(body, hdr, "different") {
		t.Fatal("verify accepted wrong secret")
	}
	if Verify([]byte(`{"hello":"X"}`), hdr, "shh") {
		t.Fatal("verify accepted tampered body")
	}
	if Verify(body, "no-prefix-here", "shh") {
		t.Fatal("verify accepted malformed header")
	}
}
