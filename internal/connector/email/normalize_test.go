package email

import (
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestNormalize_BasicShape(t *testing.T) {
	tid := uuid.New()
	mid := uuid.New()
	got := Normalize(InboundEmail{
		MailboxID:   mid,
		TenantID:    tid,
		FromAddress: "Customer@Example.COM",
		FromName:    "Jane Doe",
		Subject:     "Help with order #42",
		BodyText:    "Hello team,\nWhere is my parcel?",
		MessageID:   "<abc@example.com>",
		OccurredAt:  time.Date(2026, 5, 18, 10, 0, 0, 0, time.UTC),
	})
	if got.TenantID != tid.String() {
		t.Errorf("tenant_id = %s, want %s", got.TenantID, tid)
	}
	if got.Channel != "email" {
		t.Errorf("channel = %s", got.Channel)
	}
	if got.CustomerExternal != "customer@example.com" {
		t.Errorf("from not lowercased: %s", got.CustomerExternal)
	}
	if got.ConversationKey != got.CustomerExternal {
		t.Errorf("conversation_key should equal customer external")
	}
	if got.Body == "" || !contains(got.Body, "Subject: Help with order #42") {
		t.Errorf("body should include subject prefix: %q", got.Body)
	}
}

func TestSplitReferences(t *testing.T) {
	got := SplitReferences("<a@x> <b@x>\n<c@x>")
	if len(got) != 3 || got[2] != "<c@x>" {
		t.Errorf("split refs = %v", got)
	}
	if got := SplitReferences(""); len(got) != 0 {
		t.Errorf("empty refs not empty: %v", got)
	}
	if got := SplitReferences("not-bracketed @x@x"); len(got) != 0 {
		t.Errorf("invalid tokens should be dropped: %v", got)
	}
}

func TestParseAddressList(t *testing.T) {
	got := ParseAddressList(`"Customer" <customer@example.com>, support@acme.co.ke`)
	if len(got) != 2 {
		t.Fatalf("got %v", got)
	}
	if got[0] != "customer@example.com" || got[1] != "support@acme.co.ke" {
		t.Errorf("parsed = %v", got)
	}
}

func TestNewMessageID_Stable(t *testing.T) {
	m1 := NewMessageID("acme.co.ke")
	m2 := NewMessageID("acme.co.ke")
	if m1 == m2 {
		t.Error("Message-IDs should be unique")
	}
	if m1[0] != '<' || m1[len(m1)-1] != '>' {
		t.Errorf("not angle-bracketed: %s", m1)
	}
	if !contains(m1, "@acme.co.ke>") {
		t.Errorf("missing domain: %s", m1)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
