package realtime

import (
	"encoding/json"
	"testing"
)

func TestNew_BuildsEnvelope(t *testing.T) {
	type payload struct {
		TicketID string `json:"ticket_id"`
	}
	e, err := New("ticket.assigned", "corr-1", payload{TicketID: "abc"})
	if err != nil {
		t.Fatal(err)
	}
	if e.Type != "ticket.assigned" {
		t.Errorf("Type = %q", e.Type)
	}
	if e.CorrelationID != "corr-1" {
		t.Errorf("CorrelationID = %q", e.CorrelationID)
	}
	if e.Ts.IsZero() {
		t.Error("Ts not set")
	}
	var p payload
	if err := json.Unmarshal(e.Payload, &p); err != nil || p.TicketID != "abc" {
		t.Errorf("payload mis-marshalled: %+v err=%v", p, err)
	}
}

func TestChannelKeys(t *testing.T) {
	if AgentChannel("a1") != "ws:agent:a1" {
		t.Error("AgentChannel format")
	}
	if TicketChannel("t1") != "ws:ticket:t1" {
		t.Error("TicketChannel format")
	}
	if SupervisorChannel("ten1") != "ws:tenant:ten1:supervisor" {
		t.Error("SupervisorChannel format")
	}
}

func TestBearerFromSubprotocol(t *testing.T) {
	cases := map[string]string{
		"":                                       "",
		"bearer.abc":                             "abc",
		"bearer.abc, json":                       "abc",
		"json, bearer.xyz":                       "xyz",
		"json":                                   "",
		"bearerprefix.no":                        "",
	}
	for in, want := range cases {
		if got := bearerFromSubprotocol(in); got != want {
			t.Errorf("bearerFromSubprotocol(%q) = %q, want %q", in, got, want)
		}
	}
}
