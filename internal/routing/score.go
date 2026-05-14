// Package routing implements the hybrid skill+priority+least-busy
// algorithm described in §6 of the technical plan.
//
// A ticket is routed by:
//
//	1. eligibility filter — agent online, current_load < max_concurrent,
//	   skills ⊇ required_skills.
//	2. score = priority_weight × ticket.priority
//	         + 0.3 × (max_concurrent - current_load)        // least-busy bonus
//	         + 0.2 × freshness(last_assigned)               // round-robin tiebreaker
//	3. pick the highest score; queue with TTL when no eligible agent
//	   exists.
//
// The scoring constants live as exported var so deployments can tune
// them without recompiling — the next iteration moves them to a config
// table, but for Phase 1 the in-process knob is enough.
package routing

import (
	"slices"
	"time"

	"github.com/google/uuid"
)

// Constants from the plan §6. Higher score wins.
const (
	priorityWeightDefault = 1.0
	loadBonusWeight       = 0.3
	freshnessWeight       = 0.2
)

// PriorityWeight scales how much a ticket's nominal priority dominates
// the score. Bumping this above 1.0 makes urgent tickets effectively
// always go first; below 1.0 lets load and freshness compete more.
var PriorityWeight = priorityWeightDefault

// Agent is the routing-time view of an agent. The routing engine reads
// this from the agents + agent_status + agent_skills tables; this
// struct is the in-memory subset.
type Agent struct {
	ID            uuid.UUID
	Skills        []string
	MaxConcurrent int16
	CurrentLoad   int16
	Status        string    // online | away | break | offline
	LastAssigned  time.Time // zero value treated as "never"
}

// Ticket is the routing-time view of a ticket.
type Ticket struct {
	ID             uuid.UUID
	Priority       int16    // 1=urgent .. 5=low
	RequiredSkills []string
	CreatedAt      time.Time
}

// Eligible returns true when a is allowed to receive t. Mirrors §6's
// filter step. We return early on the cheapest checks first.
func Eligible(a Agent, t Ticket) bool {
	if a.Status != "online" {
		return false
	}
	if a.CurrentLoad >= a.MaxConcurrent {
		return false
	}
	for _, req := range t.RequiredSkills {
		if !slices.Contains(a.Skills, req) {
			return false
		}
	}
	return true
}

// Score computes a single agent's score for a ticket. Higher is better.
// The plan inverts ticket.priority so that priority=1 (urgent) yields
// a higher score than priority=5 (low); we encode that as (6 - priority).
func Score(a Agent, t Ticket, now time.Time) float64 {
	priorityComponent := PriorityWeight * float64(6-t.Priority)
	loadComponent := loadBonusWeight * float64(a.MaxConcurrent-a.CurrentLoad)
	freshness := freshnessOf(a.LastAssigned, now)
	freshnessComponent := freshnessWeight * freshness
	return priorityComponent + loadComponent + freshnessComponent
}

// freshnessOf returns a value in [0, 1] that grows with how long ago
// the agent was last assigned. Never-assigned agents get 1.0 (max
// freshness) so a fresh hire wins ties against a freshly-busy peer.
//
// We saturate at 30 minutes — past that the agent is effectively
// "fully fresh" and the actual elapsed time stops mattering, which
// avoids floating-point drift on long-idle agents.
func freshnessOf(last, now time.Time) float64 {
	if last.IsZero() {
		return 1.0
	}
	const cap = 30 * time.Minute
	d := now.Sub(last)
	if d <= 0 {
		return 0
	}
	if d >= cap {
		return 1.0
	}
	return float64(d) / float64(cap)
}

// Pick returns the highest-scoring eligible agent for the ticket, or
// `false` when nothing is eligible. Ties break on agent UUID so the
// outcome is deterministic across processes — important for the
// integration test and for replay during incident analysis.
func Pick(agents []Agent, t Ticket, now time.Time) (Agent, bool) {
	var (
		best     Agent
		bestScore float64
		found    bool
	)
	for _, a := range agents {
		if !Eligible(a, t) {
			continue
		}
		s := Score(a, t, now)
		switch {
		case !found, s > bestScore:
			best, bestScore, found = a, s, true
		case s == bestScore && a.ID.String() < best.ID.String():
			best = a
		}
	}
	return best, found
}
