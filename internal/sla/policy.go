// Package sla owns the first-response and resolution timers described
// in §6 of the technical plan. The model is straightforward:
//
//   - When a ticket is created, derive its first-response and resolution
//     deadlines from a per-tenant policy (defaults baked in here for
//     Phase 1).
//   - Persist deadlines into tickets.sla_first_response_due and
//     tickets.sla_resolution_due so external dashboards can query
//     "tickets at risk" without re-walking policy.
//   - A scheduler scans for tickets approaching breach and emits
//     warnings; on actual breach, raise priority + re-route + notify
//     supervisor.
//
// We use Postgres rather than River for this Phase-1 implementation —
// River-backed scheduling lands when the surface grows. A simple
// SELECT ... FOR UPDATE SKIP LOCKED loop on the tickets table is
// sufficient at <100 agents and avoids a second job-queue dependency.
package sla

import (
	"time"

	"github.com/google/uuid"
)

// Policy is the per-tenant SLA contract. Numbers in this struct are
// the exact targets the customer signed up for; the scheduler uses
// them verbatim.
type Policy struct {
	// FirstResponse is how long after ticket creation an agent must
	// have sent at least one outbound message before the ticket is in
	// breach.
	FirstResponse time.Duration
	// Resolution is how long after creation the ticket must reach
	// state=resolved (or beyond).
	Resolution time.Duration
	// WarnAt fires the supervisor "at risk" notification when this
	// fraction of the deadline has elapsed. 0.8 = warn at 80%.
	WarnAt float64
}

// DefaultPolicies maps ticket priority (1=urgent .. 5=low) to the
// default SLA. Tighter at higher priority. Tenants override via a
// future tenants_sla_policies table; for Phase 1 these defaults apply
// across the board.
var DefaultPolicies = map[int16]Policy{
	1: {FirstResponse: 5 * time.Minute, Resolution: 1 * time.Hour, WarnAt: 0.8},
	2: {FirstResponse: 15 * time.Minute, Resolution: 4 * time.Hour, WarnAt: 0.8},
	3: {FirstResponse: 1 * time.Hour, Resolution: 24 * time.Hour, WarnAt: 0.8},
	4: {FirstResponse: 4 * time.Hour, Resolution: 72 * time.Hour, WarnAt: 0.8},
	5: {FirstResponse: 24 * time.Hour, Resolution: 168 * time.Hour, WarnAt: 0.8},
}

// PolicyFor returns the deadline policy for the given priority. Falls
// back to priority 3 (default) when an unknown priority is seen so a
// migration that adds priority 6 doesn't crash the scheduler.
func PolicyFor(priority int16) Policy {
	if p, ok := DefaultPolicies[priority]; ok {
		return p
	}
	return DefaultPolicies[3]
}

// Deadlines computes the absolute due times for a ticket created at
// createdAt with the given priority.
func Deadlines(createdAt time.Time, priority int16) (firstResponseDue, resolutionDue time.Time) {
	p := PolicyFor(priority)
	return createdAt.Add(p.FirstResponse), createdAt.Add(p.Resolution)
}

// Risk classifies a ticket against its deadline.
type Risk int

const (
	RiskNone Risk = iota
	RiskAtRisk      // past WarnAt threshold but not yet breached
	RiskBreached
)

// String returns a stable label for logging / metrics.
func (r Risk) String() string {
	switch r {
	case RiskAtRisk:
		return "at_risk"
	case RiskBreached:
		return "breached"
	default:
		return "ok"
	}
}

// EvaluateFirstResponse returns the risk for a ticket whose deadline
// is `due`, given a `firstResponseAt` (nil when no agent has replied
// yet) and the current time `now`.
func EvaluateFirstResponse(due time.Time, firstResponseAt *time.Time, priority int16, now time.Time) Risk {
	if firstResponseAt != nil {
		// Already responded; first-response SLA can no longer breach.
		return RiskNone
	}
	if now.After(due) || now.Equal(due) {
		return RiskBreached
	}
	p := PolicyFor(priority)
	warnAt := due.Add(-time.Duration(float64(p.FirstResponse) * (1 - p.WarnAt)))
	if !now.Before(warnAt) {
		return RiskAtRisk
	}
	return RiskNone
}

// EvaluateResolution mirrors EvaluateFirstResponse for the resolution
// deadline. Once the ticket is resolved, risk is None permanently.
func EvaluateResolution(due time.Time, resolvedAt *time.Time, priority int16, now time.Time) Risk {
	if resolvedAt != nil {
		return RiskNone
	}
	if now.After(due) || now.Equal(due) {
		return RiskBreached
	}
	p := PolicyFor(priority)
	warnAt := due.Add(-time.Duration(float64(p.Resolution) * (1 - p.WarnAt)))
	if !now.Before(warnAt) {
		return RiskAtRisk
	}
	return RiskNone
}

// Event is what the scheduler emits when it detects an SLA risk
// transition. Consumers (notification, routing escalation, audit) read
// from NATS subject `sla.<event_type>`.
type Event struct {
	TicketID  uuid.UUID `json:"ticket_id"`
	TenantID  uuid.UUID `json:"tenant_id"`
	Kind      string    `json:"kind"`        // first_response | resolution
	Risk      string    `json:"risk"`        // at_risk | breached
	Priority  int16     `json:"priority"`
	OccurredAt time.Time `json:"occurred_at"`
}
