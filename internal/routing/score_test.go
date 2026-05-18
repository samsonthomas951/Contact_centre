package routing

import (
	"testing"
	"time"

	"github.com/google/uuid"
)

// fixedTime is the test "now" so freshness math is deterministic.
var fixedTime = time.Date(2026, 5, 14, 10, 0, 0, 0, time.UTC)

func newAgent(id string, status string, load, maxConcurrent int16, skills []string, lastAssigned time.Time) Agent {
	return Agent{
		ID:            uuid.MustParse(id),
		Status:        status,
		CurrentLoad:   load,
		MaxConcurrent: maxConcurrent,
		Skills:        skills,
		LastAssigned:  lastAssigned,
	}
}

func TestEligible(t *testing.T) {
	tk := Ticket{Priority: 3, RequiredSkills: []string{"swahili"}}

	cases := []struct {
		name string
		a    Agent
		want bool
	}{
		{"online, has skill, has capacity", newAgent("11111111-0000-0000-0000-000000000001", "online", 0, 5, []string{"swahili", "english"}, time.Time{}), true},
		{"offline", newAgent("11111111-0000-0000-0000-000000000002", "offline", 0, 5, []string{"swahili"}, time.Time{}), false},
		{"away", newAgent("11111111-0000-0000-0000-000000000003", "away", 0, 5, []string{"swahili"}, time.Time{}), false},
		{"at capacity", newAgent("11111111-0000-0000-0000-000000000004", "online", 5, 5, []string{"swahili"}, time.Time{}), false},
		{"missing skill", newAgent("11111111-0000-0000-0000-000000000005", "online", 0, 5, []string{"english"}, time.Time{}), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Eligible(tc.a, tk); got != tc.want {
				t.Errorf("Eligible = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestScore_PriorityDominates(t *testing.T) {
	urgent := Ticket{Priority: 1}
	low    := Ticket{Priority: 5}
	a := newAgent("11111111-0000-0000-0000-000000000001", "online", 0, 5, nil, time.Time{})
	if Score(a, urgent, fixedTime) <= Score(a, low, fixedTime) {
		t.Fatal("urgent should outscore low")
	}
}

func TestScore_FreshnessTiebreaker(t *testing.T) {
	tk := Ticket{Priority: 3}
	fresh := newAgent("11111111-0000-0000-0000-000000000001", "online", 1, 5, nil, fixedTime.Add(-1*time.Hour))
	stale := newAgent("11111111-0000-0000-0000-000000000002", "online", 1, 5, nil, fixedTime.Add(-1*time.Second))
	if Score(fresh, tk, fixedTime) <= Score(stale, tk, fixedTime) {
		t.Fatal("longer-idle agent should outscore freshly-busy peer")
	}
}

func TestScore_LoadBonus(t *testing.T) {
	tk := Ticket{Priority: 3}
	idle := newAgent("11111111-0000-0000-0000-000000000001", "online", 0, 5, nil, time.Time{})
	busy := newAgent("11111111-0000-0000-0000-000000000002", "online", 4, 5, nil, time.Time{})
	if Score(idle, tk, fixedTime) <= Score(busy, tk, fixedTime) {
		t.Fatal("less-loaded agent should outscore busier peer at equal priority+freshness")
	}
}

func TestPick_TieDeterministic(t *testing.T) {
	tk := Ticket{Priority: 3}
	now := fixedTime
	// Both never-assigned, both same load: scores tie.
	a := newAgent("11111111-0000-0000-0000-000000000001", "online", 0, 5, nil, time.Time{})
	b := newAgent("11111111-0000-0000-0000-000000000002", "online", 0, 5, nil, time.Time{})
	for i := range 10 {
		got, ok := Pick([]Agent{b, a}, tk, now)
		if !ok {
			t.Fatal("no pick")
		}
		if got.ID != a.ID {
			t.Fatalf("non-deterministic pick: got %v want %v (iteration %d)", got.ID, a.ID, i)
		}
	}
}

func TestPick_NoneEligible(t *testing.T) {
	tk := Ticket{Priority: 3, RequiredSkills: []string{"unicorn"}}
	a := newAgent("11111111-0000-0000-0000-000000000001", "online", 0, 5, []string{"horse"}, time.Time{})
	if _, ok := Pick([]Agent{a}, tk, fixedTime); ok {
		t.Fatal("expected no pick when no agent has the skill")
	}
}
