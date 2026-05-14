// Package ticket owns the ticket lifecycle and message store described
// in §6 of the technical plan. Tickets are the system of record for a
// customer interaction; the state machine is enforced in code so callers
// can't move a ticket through an illegal transition (e.g. closed → open
// without going through reopened).
package ticket

import (
	"fmt"
	"slices"
)

// State enumerates the values stored in the ticket_state Postgres enum.
type State string

const (
	StateNew      State = "new"
	StateOpen     State = "open"
	StatePending  State = "pending"
	StateOnHold   State = "on_hold"
	StateResolved State = "resolved"
	StateClosed   State = "closed"
	StateReopened State = "reopened"
)

// validStates lists every legal value; consulted by Validate.
var validStates = map[State]struct{}{
	StateNew:      {},
	StateOpen:     {},
	StatePending:  {},
	StateOnHold:   {},
	StateResolved: {},
	StateClosed:   {},
	StateReopened: {},
}

// Validate returns nil when s is a known state.
func (s State) Validate() error {
	if _, ok := validStates[s]; !ok {
		return fmt.Errorf("ticket: invalid state %q", s)
	}
	return nil
}

// transitions encodes the state-machine edges from §6:
//   new       -> open
//   open      -> pending | on_hold | resolved
//   pending   -> open | resolved
//   on_hold   -> open | resolved
//   resolved  -> closed | reopened
//   closed    -> reopened
//   reopened  -> open
var transitions = map[State][]State{
	StateNew:      {StateOpen},
	StateOpen:     {StatePending, StateOnHold, StateResolved},
	StatePending:  {StateOpen, StateResolved},
	StateOnHold:   {StateOpen, StateResolved},
	StateResolved: {StateClosed, StateReopened},
	StateClosed:   {StateReopened},
	StateReopened: {StateOpen},
}

// CanTransition reports whether moving from -> to is legal.
func CanTransition(from, to State) bool {
	return slices.Contains(transitions[from], to)
}

// Transition returns an error naming the illegal edge so callers can
// surface a useful 4xx rather than a generic 400.
func Transition(from, to State) error {
	if err := from.Validate(); err != nil {
		return err
	}
	if err := to.Validate(); err != nil {
		return err
	}
	if !CanTransition(from, to) {
		return fmt.Errorf("ticket: illegal transition %s -> %s", from, to)
	}
	return nil
}
