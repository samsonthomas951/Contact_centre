// Package dsr implements the Data Subject Request lifecycle described
// in docs/compliance/dsr-playbook.md. Statutory clock is the Kenya DPA
// s.26(7) thirty-day window.
package dsr

import (
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/google/uuid"
)

// Kind enumerates the six categories of DPA s.26/s.40 rights.
type Kind string

const (
	KindAccess        Kind = "access"
	KindRectification Kind = "rectification"
	KindErasure       Kind = "erasure"
	KindRestriction   Kind = "restriction"
	KindPortability   Kind = "portability"
	KindObjection     Kind = "objection"
)

// State enumerates the request lifecycle. Mirrors the dsr_state enum.
type State string

const (
	StateReceived            State = "received"
	StateInProgress          State = "in_progress"
	StateFulfilled           State = "fulfilled"
	StatePartiallyFulfilled  State = "partially_fulfilled"
	StateRejected            State = "rejected"
	StateWithdrawn           State = "withdrawn"
)

// validKinds / validStates short-circuit DB round-trips for caller-
// supplied values.
var validKinds = map[Kind]struct{}{
	KindAccess: {}, KindRectification: {}, KindErasure: {},
	KindRestriction: {}, KindPortability: {}, KindObjection: {},
}

// transitions lists the edges of the state machine.
var transitions = map[State][]State{
	StateReceived:           {StateInProgress, StateRejected, StateWithdrawn},
	StateInProgress:         {StateFulfilled, StatePartiallyFulfilled, StateRejected, StateWithdrawn},
	StateFulfilled:          {}, // terminal
	StatePartiallyFulfilled: {}, // terminal
	StateRejected:           {}, // terminal
	StateWithdrawn:          {}, // terminal
}

// Validate checks Kind is known.
func (k Kind) Validate() error {
	if _, ok := validKinds[k]; !ok {
		return fmt.Errorf("dsr: invalid kind %q", k)
	}
	return nil
}

// CanTransition reports whether moving from -> to is legal.
func CanTransition(from, to State) bool {
	return slices.Contains(transitions[from], to)
}

// Transition is CanTransition that returns an error naming the illegal
// edge -- handy for surfacing 4xx with the edge string.
func Transition(from, to State) error {
	if !CanTransition(from, to) {
		return fmt.Errorf("dsr: illegal transition %s -> %s", from, to)
	}
	return nil
}

// Request mirrors the public columns of dsr_requests.
type Request struct {
	ID                  uuid.UUID  `json:"id"`
	TenantID            uuid.UUID  `json:"tenant_id"`
	CustomerID          *uuid.UUID `json:"customer_id,omitempty"`
	SubjectEmail        string     `json:"subject_email,omitempty"`
	SubjectPhone        string     `json:"subject_phone,omitempty"`
	SubjectExternalRef  string     `json:"subject_external_ref,omitempty"`
	Kind                Kind       `json:"kind"`
	State               State      `json:"state"`
	Reason              string     `json:"reason,omitempty"`
	ReceivedAt          time.Time  `json:"received_at"`
	DueAt               time.Time  `json:"due_at"`
	FulfilledAt         *time.Time `json:"fulfilled_at,omitempty"`
	ResolutionNote      string     `json:"resolution_note,omitempty"`
	AssignedToAgentID   *uuid.UUID `json:"assigned_to_agent_id,omitempty"`
	CreatedAt           time.Time  `json:"created_at"`
	UpdatedAt           time.Time  `json:"updated_at"`
}

// DueWindow is the statutory response window.
const DueWindow = 30 * 24 * time.Hour

// ErrSubjectRequired says no subject identifier was supplied.
var ErrSubjectRequired = errors.New("dsr: at least one subject identifier required (customer_id, email, phone, or external_ref)")
