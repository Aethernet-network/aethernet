package derivation

import (
	"testing"

	"github.com/Aethernet-network/aethernet/internal/crypto"
	"github.com/Aethernet-network/aethernet/internal/event"
	"github.com/Aethernet-network/aethernet/internal/taskverification"
)

// Dispute-path arithmetic regression tests.
//
// **Architectural note (F5 5B Shape 3, founder direction 2026-04-25)**:
// these tests exercise deriveDispute DIRECTLY rather than going through
// settler.Settle / DeriveSettlement. The DeriveSettlement switch under
// Shape 3 only handles TerminalAccept | TerminalReject (per founder
// direction item 1); TerminalDispute is unreachable through the
// LK-consumer-driven settlement path because the LK consumer's
// IsComplete seal-rule guarantees Apply only fires on
// passSealed || failSealed.
//
// deriveDispute is preserved as forward-compat scaffolding (per its
// own doc comment) for:
//   - This kind of unit-test coverage of the dispute-arithmetic
//     reference implementation
//   - A future workstream that wires round-disputed cases into the
//     canonical settlement path (Outcome.Verdict gains a Dispute
//     variant; DeriveSettlement switch + LK consumer extend together;
//     deriveDispute becomes reachable through the canonical path
//     without arithmetic changes)
//
// Production dispute path today: poster-initiated disputes
// (task.Status==TaskStatusDisputed) route through
// autovalidator.processDisputedTasks → escrow.ReleaseNet (one of the
// 11 non-settlement CanonicalSyntheticID callers per #134 audit).
// Round-disputed cases (deadline expiry without supermajority,
// round.State==RoundStateDisputed) are NOT routed to settlement at
// all — they wait for poster escalation via the manual dispute path.
//
// Pre-Shape-3 these tests lived in verification_consensus_settler_test.go
// as TestSettle_Dispute_50_50 and TestSettle_DisputeOddMicroAET; they
// constructed a Disputed round and called settler.Settle, asserting
// the 50/50 split via the round.State→deriveDispute switch in the
// pre-Shape-3 DeriveSettlement. Shape 3 removes that switch arm.
// These tests are converted per founder Option (a): same arithmetic
// assertion, target shifted to the architecturally correct layer
// (deriveDispute called directly).

const (
	disputeWorkerSharePerThousand = 7300 // 73.00% per v4.1 economic model
	disputeSharesDenom            = 10000
)

func makeDisputedRound(taskID string) *taskverification.TaskVerificationRound {
	return &taskverification.TaskVerificationRound{
		RoundID:  "round-dispute-test",
		TaskID:   taskID,
		WorkerID: "worker-1",
		PosterID: "poster-1",
		State:    taskverification.RoundStateDisputed,
	}
}

// TestDeriveDispute_50_50 (Shape 3 conversion of pre-existing
// TestSettle_Dispute_50_50): even budget, 73% worker portion split
// 50/50 between worker and poster, treasury absorbs the remainder.
func TestDeriveDispute_50_50(t *testing.T) {
	t.Parallel()

	const budget uint64 = 10000
	round := makeDisputedRound("task-1")

	records, status, _, _, err := deriveDispute(
		round,
		DerivationInputs{},
		budget,
		crypto.AgentID(round.PosterID),
		event.EventID("funding-ref"),
		crypto.AgentID("treasury"),
	)
	if err != nil {
		t.Fatalf("deriveDispute: %v", err)
	}
	if status != StatusDerived {
		t.Fatalf("status = %v; want StatusDerived", status)
	}

	workerPortion := budget * disputeWorkerSharePerThousand / disputeSharesDenom
	wantWorker := workerPortion / 2
	wantPoster := workerPortion - wantWorker
	wantTreasury := budget - workerPortion

	gotWorker, gotPoster, gotTreasury := summarizeDispute(records)

	if gotWorker != wantWorker {
		t.Errorf("worker amount = %d; want %d", gotWorker, wantWorker)
	}
	if gotPoster != wantPoster {
		t.Errorf("poster amount = %d; want %d", gotPoster, wantPoster)
	}
	if gotTreasury != wantTreasury {
		t.Errorf("treasury amount = %d; want %d", gotTreasury, wantTreasury)
	}
	if total := gotWorker + gotPoster + gotTreasury; total != budget {
		t.Errorf("total distributed = %d; want %d (conservation)", total, budget)
	}
}

// TestDeriveDispute_OddMicroAET (Shape 3 conversion of pre-existing
// TestSettle_DisputeOddMicroAET): odd budget, poster gets the extra
// µAET on the 50/50 split (workerAmount = workerPortion/2;
// posterAmount = workerPortion - workerAmount absorbs the odd µAET).
func TestDeriveDispute_OddMicroAET(t *testing.T) {
	t.Parallel()

	const budget uint64 = 10001
	round := makeDisputedRound("task-1")

	records, status, _, _, err := deriveDispute(
		round,
		DerivationInputs{},
		budget,
		crypto.AgentID(round.PosterID),
		event.EventID("funding-ref"),
		crypto.AgentID("treasury"),
	)
	if err != nil {
		t.Fatalf("deriveDispute: %v", err)
	}
	if status != StatusDerived {
		t.Fatalf("status = %v; want StatusDerived", status)
	}

	gotWorker, gotPoster, gotTreasury := summarizeDispute(records)

	if gotPoster < gotWorker {
		t.Errorf("poster (%d) should get >= worker (%d) on odd split", gotPoster, gotWorker)
	}
	if total := gotWorker + gotPoster + gotTreasury; total != budget {
		t.Errorf("total distributed = %d; want %d (conservation)", total, budget)
	}
}

// summarizeDispute extracts the worker / poster / treasury amounts
// from a deriveDispute record set. Reject-style + treasury-route checks
// share this helper.
func summarizeDispute(records []PayoutRecord) (worker, poster, treasury uint64) {
	for _, r := range records {
		switch r.Recipient.Role {
		case RoleWorker:
			worker += r.Amount.Value
		case RolePosterRefund:
			poster += r.Amount.Value
		case RoleTreasury:
			treasury += r.Amount.Value
		}
	}
	return
}
