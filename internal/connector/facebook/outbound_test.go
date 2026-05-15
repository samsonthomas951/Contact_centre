package facebook

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// staticPages is the test PageStore: returns a fixed token, records
// every BUC header observation it sees so the test can assert.
type staticPages struct {
	tok        string
	bucObserved []string
}

func (s *staticPages) Token(_ context.Context, _ string) (string, error) {
	return s.tok, nil
}
func (s *staticPages) ObserveBUC(_ context.Context, _, header string) {
	s.bucObserved = append(s.bucObserved, header)
}

func newGraphMock(t *testing.T, status int, body string, extra http.Header) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Sanity: this is a POST to /<version>/<page>/messages.
		if r.Method != http.MethodPost {
			t.Errorf("got %s, want POST", r.Method)
		}
		if !strings.Contains(r.URL.Path, "/messages") {
			t.Errorf("path = %s, want /messages", r.URL.Path)
		}
		if r.URL.Query().Get("access_token") == "" {
			t.Error("access_token missing from query")
		}
		// Round-trip the body through a struct to make sure it's
		// well-formed JSON shaped like Meta wants.
		var got struct {
			Recipient struct {
				ID string `json:"id"`
			} `json:"recipient"`
			Message struct {
				Text string `json:"text"`
			} `json:"message"`
			MessagingType string `json:"messaging_type"`
		}
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Errorf("body decode: %v", err)
		}
		if got.Recipient.ID == "" || got.Message.Text == "" {
			t.Errorf("bad payload: %+v", got)
		}
		for k, vv := range extra {
			for _, v := range vv {
				w.Header().Add(k, v)
			}
		}
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}))
}

func TestSender_SendText_HappyPath(t *testing.T) {
	pages := &staticPages{tok: "PAGE_TOKEN"}
	srv := newGraphMock(t, 200,
		`{"message_id":"mid.OUTBOUND_001","recipient_id":"PSID_X"}`,
		http.Header{"X-Business-Use-Case-Usage": []string{`{"PAGE":[{"call_count":12,"total_cputime":0,"total_time":0}]}`}})
	defer srv.Close()

	s := NewSender(pages, nil)
	s.GraphHost = srv.URL
	s.GraphVersion = "v22.0"

	mid, err := s.SendText(context.Background(), "PAGE_KNOWN", "PSID_X", "Hello!")
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	if mid != "mid.OUTBOUND_001" {
		t.Errorf("returned mid = %q", mid)
	}
	if len(pages.bucObserved) != 1 {
		t.Errorf("BUC header not observed: %+v", pages.bucObserved)
	}
}

func TestSender_SendText_InvalidTokenIsPermanent(t *testing.T) {
	pages := &staticPages{tok: "BAD"}
	srv := newGraphMock(t, 401,
		`{"error":{"message":"Invalid OAuth access token","type":"OAuthException","code":190}}`,
		nil)
	defer srv.Close()

	s := NewSender(pages, nil)
	s.GraphHost = srv.URL

	_, err := s.SendText(context.Background(), "PAGE", "PSID", "x")
	if err == nil {
		t.Fatal("want error")
	}
	var ge *Error
	if !errors.As(err, &ge) {
		t.Fatalf("want *Error, got %T (%v)", err, err)
	}
	if ge.Code != 190 {
		t.Errorf("code = %d, want 190", ge.Code)
	}
	if ge.IsTransient() {
		t.Error("OAuth invalid token should NOT be transient")
	}
}

func TestSender_SendText_RateLimitIsTransient(t *testing.T) {
	pages := &staticPages{tok: "PAGE_TOKEN"}
	srv := newGraphMock(t, 400,
		`{"error":{"message":"App request limit reached","type":"OAuthException","code":4}}`,
		nil)
	defer srv.Close()

	s := NewSender(pages, nil)
	s.GraphHost = srv.URL

	_, err := s.SendText(context.Background(), "PAGE", "PSID", "x")
	var ge *Error
	if !errors.As(err, &ge) || ge.Code != 4 {
		t.Fatalf("want code=4 typed err, got %v", err)
	}
	if !ge.IsTransient() {
		t.Error("code 4 should be transient")
	}
}

func TestSender_SendText_5xxIsTransient(t *testing.T) {
	pages := &staticPages{tok: "PAGE_TOKEN"}
	srv := newGraphMock(t, 503, `{"error":{"message":"Service Unavailable"}}`, nil)
	defer srv.Close()

	s := NewSender(pages, nil)
	s.GraphHost = srv.URL

	_, err := s.SendText(context.Background(), "PAGE", "PSID", "x")
	var ge *Error
	if !errors.As(err, &ge) || !ge.IsTransient() {
		t.Errorf("503 should be transient typed err, got %v", err)
	}
}

func TestSender_SendText_RejectsEmptyBody(t *testing.T) {
	s := NewSender(&staticPages{tok: "x"}, nil)
	if _, err := s.SendText(context.Background(), "P", "P", "  \n"); err == nil {
		t.Fatal("empty body should reject")
	}
}
