package dsr

import "testing"

func TestKind_Validate(t *testing.T) {
	for _, k := range []Kind{KindAccess, KindRectification, KindErasure,
		KindRestriction, KindPortability, KindObjection} {
		if err := k.Validate(); err != nil {
			t.Errorf("valid kind %q rejected: %v", k, err)
		}
	}
	if err := Kind("explain_consciousness").Validate(); err == nil {
		t.Error("invalid kind accepted")
	}
}

func TestTransition(t *testing.T) {
	legal := [][2]State{
		{StateReceived, StateInProgress},
		{StateReceived, StateRejected},
		{StateReceived, StateWithdrawn},
		{StateInProgress, StateFulfilled},
		{StateInProgress, StatePartiallyFulfilled},
		{StateInProgress, StateRejected},
		{StateInProgress, StateWithdrawn},
	}
	for _, p := range legal {
		if err := Transition(p[0], p[1]); err != nil {
			t.Errorf("legal %s->%s rejected: %v", p[0], p[1], err)
		}
	}

	illegal := [][2]State{
		{StateReceived, StateFulfilled},      // must go via in_progress
		{StateFulfilled, StateInProgress},    // terminal
		{StateRejected, StateInProgress},     // terminal
		{StateWithdrawn, StateFulfilled},     // terminal
	}
	for _, p := range illegal {
		if err := Transition(p[0], p[1]); err == nil {
			t.Errorf("illegal %s->%s accepted", p[0], p[1])
		}
	}
}
