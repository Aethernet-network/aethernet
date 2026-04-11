package roundpolicy

// RoundPolicyEvaluator implements the 5-step progress-aware finalization
// algorithm from docs/blobsync-design.md §6.4.
//
// This evaluator is GENERIC — it knows nothing about tasks, analyzers,
// diversity floors, or score thresholds. It only knows about terminal
// counts, active leases, and timing. The caller computes OutcomeSecured
// using task-specific verdict logic.
type RoundPolicyEvaluator struct{}

// NewRoundPolicyEvaluator creates an evaluator.
func NewRoundPolicyEvaluator() *RoundPolicyEvaluator {
	return &RoundPolicyEvaluator{}
}

// Evaluate returns the wait decision for the current round state.
// All times are unix seconds. The caller provides nowUnix — no internal
// time.Now() calls.
//
// Algorithm (5 steps):
//  1. All terminal → FinalizeNow
//  2. Outcome secured → FinalizeNow (don't wait for remaining)
//  3. Active leases exist → WaitForActive (bounded by lease expiry)
//  4. No active leases → check durable votes: if sufficient → FinalizeNow,
//     else if backstop not reached → WaitForBackstop
//  5. Backstop reached → Expired
func (e *RoundPolicyEvaluator) Evaluate(state RoundState, nowUnix int64) WaitDecision {
	// Step 1: all validators are terminal (voted + signed-abstain/failed).
	if state.TotalValidators > 0 && state.TerminalCount >= state.TotalValidators {
		return WaitDecisionFinalizeNow
	}

	// Step 2: outcome mathematically secured regardless of remaining votes.
	if state.OutcomeSecured {
		return WaitDecisionFinalizeNow
	}

	// Step 5 (checked before 3/4 because backstop overrides everything):
	// absolute backstop reached.
	if state.BackstopUnix > 0 && nowUnix >= state.BackstopUnix {
		return WaitDecisionExpired
	}

	// Step 3: active leases exist — validators are working.
	if state.ActiveLeaseCount > 0 && state.MaxLeaseExpiryUnix > nowUnix {
		return WaitDecisionWaitForActive
	}

	// Step 4: no active leases. Check if durable votes already satisfy
	// full-set BFT supermajority. OutcomeSecured was already checked in
	// step 2 and was false, so we know the current votes are insufficient.
	// Wait for backstop.
	if nowUnix < state.BackstopUnix {
		return WaitDecisionWaitForBackstop
	}

	// Fallback: backstop reached or no backstop set.
	return WaitDecisionExpired
}
