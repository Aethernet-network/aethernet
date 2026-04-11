package roundpolicy

import "testing"

func TestEvaluate_AllVoted_FinalizeNow(t *testing.T) {
	e := NewRoundPolicyEvaluator()
	state := RoundState{
		TotalValidators: 5,
		VotedCount:      5,
		TerminalCount:   5,
		BackstopUnix:    2000,
	}
	d := e.Evaluate(state, 1000)
	if d != WaitDecisionFinalizeNow {
		t.Errorf("got %v, want FinalizeNow", d)
	}
}

func TestEvaluate_OutcomeSecured_FinalizeNow(t *testing.T) {
	e := NewRoundPolicyEvaluator()
	state := RoundState{
		TotalValidators:  5,
		VotedCount:       4,
		TerminalCount:    4,
		ActiveLeaseCount: 1,
		OutcomeSecured:   true,
		BackstopUnix:     2000,
	}
	d := e.Evaluate(state, 1000)
	if d != WaitDecisionFinalizeNow {
		t.Errorf("got %v, want FinalizeNow (outcome secured)", d)
	}
}

func TestEvaluate_ActiveLeases_WaitForActive(t *testing.T) {
	e := NewRoundPolicyEvaluator()
	state := RoundState{
		TotalValidators:    5,
		VotedCount:         2,
		TerminalCount:      2,
		ActiveLeaseCount:   2,
		StaleCount:         1,
		MaxLeaseExpiryUnix: 1030,
		BackstopUnix:       2000,
	}
	d := e.Evaluate(state, 1000)
	if d != WaitDecisionWaitForActive {
		t.Errorf("got %v, want WaitForActive", d)
	}
}

func TestEvaluate_NoActiveLeases_VotesSufficient_FinalizeNow(t *testing.T) {
	e := NewRoundPolicyEvaluator()
	// Votes satisfy BFT — OutcomeSecured = true.
	state := RoundState{
		TotalValidators: 5,
		VotedCount:      4,
		TerminalCount:   4,
		StaleCount:      1,
		OutcomeSecured:  true,
		BackstopUnix:    2000,
	}
	d := e.Evaluate(state, 1000)
	if d != WaitDecisionFinalizeNow {
		t.Errorf("got %v, want FinalizeNow", d)
	}
}

func TestEvaluate_NoActiveLeases_VotesInsufficient_WaitForBackstop(t *testing.T) {
	e := NewRoundPolicyEvaluator()
	state := RoundState{
		TotalValidators: 5,
		VotedCount:      2,
		TerminalCount:   2,
		StaleCount:      3,
		OutcomeSecured:  false,
		BackstopUnix:    2000,
	}
	d := e.Evaluate(state, 1000)
	if d != WaitDecisionWaitForBackstop {
		t.Errorf("got %v, want WaitForBackstop", d)
	}
}

func TestEvaluate_BackstopReached_Expired(t *testing.T) {
	e := NewRoundPolicyEvaluator()
	state := RoundState{
		TotalValidators: 5,
		VotedCount:      2,
		TerminalCount:   2,
		StaleCount:      3,
		OutcomeSecured:  false,
		BackstopUnix:    1000,
	}
	d := e.Evaluate(state, 1000) // now == backstop
	if d != WaitDecisionExpired {
		t.Errorf("got %v, want Expired", d)
	}
}

func TestEvaluate_MixedState_CorrectCategorization(t *testing.T) {
	e := NewRoundPolicyEvaluator()
	// 5 validators: 2 voted, 1 active lease, 1 stale, 1 signed-abstain (terminal)
	state := RoundState{
		TotalValidators:    5,
		VotedCount:         2,
		TerminalCount:      3, // 2 voted + 1 signed-abstain
		ActiveLeaseCount:   1,
		StaleCount:         1,
		MaxLeaseExpiryUnix: 1050,
		OutcomeSecured:     false,
		BackstopUnix:       2000,
	}
	d := e.Evaluate(state, 1000)
	if d != WaitDecisionWaitForActive {
		t.Errorf("got %v, want WaitForActive (1 active lease)", d)
	}
}

func TestEvaluate_SingleValidator_Voted_FinalizeNow(t *testing.T) {
	e := NewRoundPolicyEvaluator()
	state := RoundState{
		TotalValidators: 1,
		VotedCount:      1,
		TerminalCount:   1,
		BackstopUnix:    2000,
	}
	d := e.Evaluate(state, 1000)
	if d != WaitDecisionFinalizeNow {
		t.Errorf("got %v, want FinalizeNow", d)
	}
}

func TestEvaluate_AllStale_NoVotes_WaitForBackstop(t *testing.T) {
	e := NewRoundPolicyEvaluator()
	state := RoundState{
		TotalValidators: 5,
		VotedCount:      0,
		TerminalCount:   0,
		StaleCount:      5,
		OutcomeSecured:  false,
		BackstopUnix:    2000,
	}
	d := e.Evaluate(state, 1000)
	if d != WaitDecisionWaitForBackstop {
		t.Errorf("got %v, want WaitForBackstop (no votes, can't finalize)", d)
	}
}

func TestEvaluate_ActiveLeaseExpired_FallsToBackstop(t *testing.T) {
	e := NewRoundPolicyEvaluator()
	// Active lease count > 0 but MaxLeaseExpiryUnix is in the past.
	state := RoundState{
		TotalValidators:    5,
		VotedCount:         2,
		TerminalCount:      2,
		ActiveLeaseCount:   1,
		MaxLeaseExpiryUnix: 990, // expired
		OutcomeSecured:     false,
		BackstopUnix:       2000,
	}
	d := e.Evaluate(state, 1000)
	// Lease expired → falls through to step 4 (no active leases effectively).
	if d != WaitDecisionWaitForBackstop {
		t.Errorf("got %v, want WaitForBackstop (lease expired)", d)
	}
}

func TestWaitDecision_String(t *testing.T) {
	cases := []struct {
		d    WaitDecision
		want string
	}{
		{WaitDecisionFinalizeNow, "FinalizeNow"},
		{WaitDecisionWaitForActive, "WaitForActive"},
		{WaitDecisionWaitForBackstop, "WaitForBackstop"},
		{WaitDecisionExpired, "Expired"},
	}
	for _, tc := range cases {
		if got := tc.d.String(); got != tc.want {
			t.Errorf("String() = %q, want %q", got, tc.want)
		}
	}
}
