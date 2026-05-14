package sla

import (
	"testing"
	"time"
)

var fixedNow = time.Date(2026, 5, 14, 12, 0, 0, 0, time.UTC)

func TestPolicyFor_FallsBackToDefault(t *testing.T) {
	p := PolicyFor(99)
	if p != DefaultPolicies[3] {
		t.Errorf("unknown priority should fall back to priority 3, got %+v", p)
	}
}

func TestDeadlines_PriorityScaling(t *testing.T) {
	created := fixedNow
	urgentFR, urgentRes := Deadlines(created, 1)
	lowFR, lowRes := Deadlines(created, 5)

	if !urgentFR.Before(lowFR) {
		t.Errorf("urgent first-response should be sooner than low: urgent=%s low=%s", urgentFR, lowFR)
	}
	if !urgentRes.Before(lowRes) {
		t.Errorf("urgent resolution should be sooner than low: urgent=%s low=%s", urgentRes, lowRes)
	}
}

func TestEvaluateFirstResponse_States(t *testing.T) {
	// Priority 3: 1h FR, warn at 80%.
	due := fixedNow.Add(1 * time.Hour)

	cases := []struct {
		name string
		now  time.Time
		fr   *time.Time
		want Risk
	}{
		{"already responded — None", fixedNow.Add(2 * time.Hour), &fixedNow, RiskNone},
		{"deep within SLA — None", fixedNow.Add(10 * time.Minute), nil, RiskNone},
		{"crossed warn threshold (80%)", fixedNow.Add(50 * time.Minute), nil, RiskAtRisk},
		{"after due — Breached", fixedNow.Add(70 * time.Minute), nil, RiskBreached},
		{"exactly at due — Breached", due, nil, RiskBreached},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := EvaluateFirstResponse(due, tc.fr, 3, tc.now)
			if got != tc.want {
				t.Errorf("got %s, want %s", got, tc.want)
			}
		})
	}
}

func TestEvaluateResolution_States(t *testing.T) {
	// Priority 3: 24h resolution.
	due := fixedNow.Add(24 * time.Hour)
	resolved := fixedNow

	if got := EvaluateResolution(due, &resolved, 3, fixedNow.Add(48*time.Hour)); got != RiskNone {
		t.Errorf("resolved ticket should be RiskNone forever, got %s", got)
	}
	if got := EvaluateResolution(due, nil, 3, fixedNow.Add(20*time.Hour)); got != RiskAtRisk {
		t.Errorf("80%% mark should be at_risk, got %s", got)
	}
	if got := EvaluateResolution(due, nil, 3, fixedNow.Add(25*time.Hour)); got != RiskBreached {
		t.Errorf("past due should be breached, got %s", got)
	}
}

func TestRisk_String(t *testing.T) {
	cases := map[Risk]string{
		RiskNone:     "ok",
		RiskAtRisk:   "at_risk",
		RiskBreached: "breached",
	}
	for r, want := range cases {
		if got := r.String(); got != want {
			t.Errorf("%d -> %s, want %s", r, got, want)
		}
	}
}
