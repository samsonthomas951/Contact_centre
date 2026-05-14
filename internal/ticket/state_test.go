package ticket

import "testing"

func TestState_Validate(t *testing.T) {
	if err := StateOpen.Validate(); err != nil {
		t.Fatalf("StateOpen rejected: %v", err)
	}
	if err := State("emergent").Validate(); err == nil {
		t.Fatal("invalid state accepted")
	}
}

func TestTransition(t *testing.T) {
	legal := [][2]State{
		{StateNew, StateOpen},
		{StateOpen, StatePending},
		{StateOpen, StateOnHold},
		{StateOpen, StateResolved},
		{StatePending, StateResolved},
		{StateOnHold, StateOpen},
		{StateResolved, StateClosed},
		{StateResolved, StateReopened},
		{StateClosed, StateReopened},
		{StateReopened, StateOpen},
	}
	for _, p := range legal {
		t.Run(string(p[0])+"->"+string(p[1]), func(t *testing.T) {
			if err := Transition(p[0], p[1]); err != nil {
				t.Errorf("legal %s->%s rejected: %v", p[0], p[1], err)
			}
		})
	}

	illegal := [][2]State{
		{StateNew, StateClosed},
		{StateOpen, StateClosed},      // must go via resolved
		{StateClosed, StateOpen},      // must go via reopened
		{StatePending, StateOnHold},   // not modeled
		{StateResolved, StateOpen},    // must reopen first
	}
	for _, p := range illegal {
		t.Run(string(p[0])+"->"+string(p[1])+"-illegal", func(t *testing.T) {
			if err := Transition(p[0], p[1]); err == nil {
				t.Errorf("illegal %s->%s accepted", p[0], p[1])
			}
		})
	}
}
