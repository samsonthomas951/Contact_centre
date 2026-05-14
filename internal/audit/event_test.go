package audit

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
)

func mustJSON(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return b
}

func TestEvent_Validate(t *testing.T) {
	tenant := uuid.New()
	corr := uuid.New()

	cases := []struct {
		name string
		e    Event
		ok   bool
	}{
		{"ok", Event{TenantID: tenant, ActorType: "agent", Action: "ticket.assign", CorrelationID: corr}, true},
		{"no tenant", Event{ActorType: "system", Action: "x", CorrelationID: corr}, false},
		{"bad actor", Event{TenantID: tenant, ActorType: "robot", Action: "x", CorrelationID: corr}, false},
		{"no action", Event{TenantID: tenant, ActorType: "system", CorrelationID: corr}, false},
		{"no corr", Event{TenantID: tenant, ActorType: "system", Action: "x"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.e.Validate()
			if tc.ok && err != nil {
				t.Errorf("want ok, got %v", err)
			}
			if !tc.ok && err == nil {
				t.Error("want error, got nil")
			}
		})
	}
}

func TestComputeHash_StableAndSensitive(t *testing.T) {
	tenant := uuid.New()
	corr := uuid.New()
	e := Event{
		Seq:           42,
		Ts:            time.Unix(1700000000, 0).UTC(),
		TenantID:      tenant,
		ActorType:     "agent",
		Action:        "ticket.assign",
		CorrelationID: corr,
		Payload:       mustJSON(t, map[string]any{"agent": "a", "ticket": "t"}),
		PrevHash:      genesisHash(),
	}
	h1 := computeHash(&e)
	h2 := computeHash(&e)
	if !equalBytes(h1, h2) {
		t.Fatal("hash not stable across calls")
	}

	mutated := e
	mutated.Action = "ticket.reassign"
	if equalBytes(computeHash(&mutated), h1) {
		t.Fatal("hash unchanged after mutating action — chain would be defeatable")
	}

	mutated2 := e
	mutated2.Payload = mustJSON(t, map[string]any{"agent": "b", "ticket": "t"})
	if equalBytes(computeHash(&mutated2), h1) {
		t.Fatal("hash unchanged after mutating payload")
	}
}
