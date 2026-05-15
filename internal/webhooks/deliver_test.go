package webhooks

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestBackoffFor_Schedule(t *testing.T) {
	cases := map[int]time.Duration{
		1: 1 * time.Second,
		2: 4 * time.Second,
		3: 16 * time.Second,
		4: 64 * time.Second,
		5: 256 * time.Second, // 4*64 = 256s, under 5min cap
		6: 5 * time.Minute,   // 4*256=1024 -> capped
		7: 5 * time.Minute,
	}
	for attempt, want := range cases {
		if got := BackoffFor(attempt); got != want {
			t.Errorf("BackoffFor(%d) = %s want %s", attempt, got, want)
		}
	}
}

func TestDeliver_Classification(t *testing.T) {
	cases := []struct {
		name       string
		status     int
		want       Classification
		delivered  bool
	}{
		{"200 OK", 200, ClassifyDelivered, true},
		{"204 No Content", 204, ClassifyDelivered, true},
		{"301 redirect (treated permanent)", 301, ClassifyPermanent, false},
		{"401", 401, ClassifyPermanent, false},
		{"404", 404, ClassifyPermanent, false},
		{"408 timeout (transient)", 408, ClassifyTransient, false},
		{"410 Gone (permanent)", 410, ClassifyPermanent, false},
		{"429 Too Many", 429, ClassifyTransient, false},
		{"500 Server Err (transient)", 500, ClassifyTransient, false},
		{"503", 503, ClassifyTransient, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.ReadAll(r.Body)
				w.WriteHeader(tc.status)
			}))
			defer srv.Close()

			d := &Deliverer{HTTP: srv.Client(), Pool: nil}
			out, err := d.Deliver(context.Background(),
				Subscription{ID: uuid.New(), URL: srv.URL, Secret: "shh"},
				Event{ID: "ev1", Subject: "ticket.created",
					Ts: time.Now(), Body: []byte(`{}`)},
				1)
			if err != nil {
				t.Fatal(err)
			}
			if out.Class != tc.want {
				t.Errorf("class = %v want %v", out.Class, tc.want)
			}
			if out.Delivered != tc.delivered {
				t.Errorf("delivered = %v want %v", out.Delivered, tc.delivered)
			}
			if out.StatusCode != tc.status {
				t.Errorf("status = %d want %d", out.StatusCode, tc.status)
			}
		})
	}
}

func TestDeliver_TransportErrorIsTransient(t *testing.T) {
	d := &Deliverer{
		HTTP: &http.Client{Timeout: 100 * time.Millisecond},
		Pool: nil,
	}
	out, err := d.Deliver(context.Background(),
		Subscription{ID: uuid.New(), URL: "http://127.0.0.1:1/never", Secret: "x"},
		Event{ID: "ev2", Subject: "ticket.assigned",
			Ts: time.Now(), Body: []byte(`{}`)},
		1)
	if err != nil {
		t.Fatal(err)
	}
	if out.Class != ClassifyTransient {
		t.Errorf("connection refused should be Transient, got %v", out.Class)
	}
	if out.Delivered {
		t.Error("transport err should not be delivered=true")
	}
}

func TestDeliver_SignsRequest(t *testing.T) {
	body := []byte(`{"answer":42}`)
	want := Sign(body, "shhh")

	got := ""
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get(SignatureHeader)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	d := &Deliverer{HTTP: srv.Client(), Pool: nil}
	if _, err := d.Deliver(context.Background(),
		Subscription{ID: uuid.New(), URL: srv.URL, Secret: "shhh"},
		Event{ID: "e", Subject: "x.y", Ts: time.Now(), Body: body},
		1); err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Errorf("X-Signature-256 = %q want %q", got, want)
	}
}
