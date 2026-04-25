package dispatch_test

import (
	"context"
	"testing"

	badger "github.com/dgraph-io/badger/v4"

	"github.com/Aethernet-network/aethernet/internal/crypto"
	"github.com/Aethernet-network/aethernet/internal/dispatch"
	"github.com/Aethernet-network/aethernet/internal/dispatch/conformance"
	"github.com/Aethernet-network/aethernet/internal/escrow"
	"github.com/Aethernet-network/aethernet/internal/event"
	"github.com/Aethernet-network/aethernet/internal/ledger"
	"github.com/Aethernet-network/aethernet/internal/settlement"
	"github.com/Aethernet-network/aethernet/internal/settlement/derivation"
	"github.com/Aethernet-network/aethernet/internal/tasks"
	"github.com/Aethernet-network/aethernet/internal/taskverification"
)

// newTestTVLKConsumer builds a fresh TVConsensusLogicalKeyConsumer with
// all its dependencies. Mirrors newTestTVConsumer in
// tv_consensus_consumer_test.go for consistency.
func newTestTVLKConsumer(t *testing.T, activeWeight uint64) (
	*dispatch.TVConsensusLogicalKeyConsumer,
	*tasks.TaskManager,
	*ledger.TransferLedger,
	*escrow.Escrow,
	taskverification.Store,
) {
	t.Helper()
	tl := ledger.NewTransferLedger()
	_ = tl.FundAgent("poster", 1_000_000)
	tm := tasks.NewTaskManager()
	em := escrow.New(tl)
	store := newTVRoundStore(t)

	settler := settlement.NewVerificationConsensusSettler(
		tm, tl, em, &tvStubDAG{}, nil, "genesis:treasury", nil, true)
	settler.SetDAGReader(tvStubAnchorReader{})
	aw := func() uint64 { return activeWeight }
	c := dispatch.NewTVConsensusLogicalKeyConsumer(settler, store, tm, em, aw)
	return c, tm, tl, em, store
}

// setupTaskLK is the TVConsensusLogicalKeyConsumer analogue of
// setupTask (tv_consensus_consumer_test.go). Provisions a task +
// escrow and constructs a round with the given vote records, then
// saves it to the store. Returns the taskID and the round pointer
// so tests can mutate vote weights if needed.
func setupTaskLK(
	t *testing.T,
	tm *tasks.TaskManager,
	tl *ledger.TransferLedger,
	em *escrow.Escrow,
	store taskverification.Store,
	budget uint64,
	roundID taskverification.RoundID,
	votes []taskverification.TaskVerificationVoteRecord,
) (string, *taskverification.TaskVerificationRound) {
	t.Helper()
	task, err := tm.PostTask("poster", "test", "desc", "research", budget)
	if err != nil {
		t.Fatalf("PostTask: %v", err)
	}
	_ = tm.ClaimTask(task.ID, "worker")
	_ = tm.SubmitResult(task.ID, "worker", "sha256:test", "note", "")

	_ = tl.TransferFromBucket("poster", crypto.AgentID("escrow:"+task.ID), budget)
	_ = em.RegisterEscrowForTest(task.ID, "poster", budget, event.EventID("test-funding:"+task.ID))

	round := &taskverification.TaskVerificationRound{
		RoundID:  roundID,
		TaskID:   task.ID,
		WorkerID: "worker",
		PosterID: "poster",
		Category: "research",
		// F5 5B: round must be terminal at settler invocation. See
		// setupTask in tv_consensus_consumer_test.go for the full rationale.
		// Default to FinalizedAccept; tests that exercise reject/dispute
		// branches mutate the State + FinalVerdict explicitly after
		// setupTaskLK returns.
		State:        taskverification.RoundStateFinalizedAccept,
		FinalVerdict: taskverification.VerdictPass,
		Votes:        votes,
	}
	if err := store.SaveRound(context.Background(), round); err != nil {
		t.Fatalf("SaveRound: %v", err)
	}
	return task.ID, round
}

// makeLKConsensusEvent constructs a TaskVerificationConsensus event
// with the given (taskID, roundID, verdict). Used to exercise the
// Key() extractor under known payload shapes.
func makeLKConsensusEvent(t *testing.T, taskID, roundID, verdict string) *event.Event {
	t.Helper()
	ev, err := event.New(
		event.EventTypeTaskVerificationConsensus,
		nil,
		event.TaskVerificationConsensusPayload{
			Version:      1,
			RoundID:      roundID,
			TaskID:       taskID,
			WorkerID:     "worker",
			PosterID:     "poster",
			FinalVerdict: verdict,
			FinalScoreBP: 7000,
		},
		"validator-1",
		nil,
		0,
	)
	if err != nil {
		t.Fatalf("construct consensus event: %v", err)
	}
	return ev
}

// --- Shape tests ------------------------------------------------------------

func TestTVConsensusLKConsumer_Name(t *testing.T) {
	c, _, _, _, _ := newTestTVLKConsumer(t, 300)
	if c.Name() != "tv_consensus_settlement_lk" {
		t.Errorf("Name: got %q want tv_consensus_settlement_lk", c.Name())
	}
}

func TestTVConsensusLKConsumer_Interested(t *testing.T) {
	c, _, _, _, _ := newTestTVLKConsumer(t, 300)
	ev := makeLKConsensusEvent(t, "task-1", "round-1", "pass")
	if !c.Interested(ev) {
		t.Error("should be interested in TaskVerificationConsensus events")
	}
	other, _ := event.New(event.EventTypeTransfer, nil, event.TransferPayload{
		FromAgent: "a", ToAgent: "b", Amount: 1, Currency: "AET",
	}, "a", nil, 0)
	if c.Interested(other) {
		t.Error("should NOT be interested in Transfer events")
	}
}

func TestTVConsensusLKConsumer_Key_ExtractsRoundID(t *testing.T) {
	c, _, _, _, _ := newTestTVLKConsumer(t, 300)
	ev := makeLKConsensusEvent(t, "task-1", "round-xyz", "pass")
	key, err := c.Key(ev)
	if err != nil {
		t.Fatalf("Key: %v", err)
	}
	if key != "round-xyz" {
		t.Errorf("Key: got %q want round-xyz", key)
	}
}

// --- IsComplete tests -------------------------------------------------------

// passVotes constructs a uniform-stake vote set where each validator
// records a PASS vote with the given scoreBP. Stake is 100 per voter
// so the total active weight used in IsComplete is 100 * count.
func passVotes(count int, scoreBP uint64) []taskverification.TaskVerificationVoteRecord {
	out := make([]taskverification.TaskVerificationVoteRecord, count)
	for i := 0; i < count; i++ {
		out[i] = taskverification.TaskVerificationVoteRecord{
			ValidatorID:    crypto.AgentID([]byte{'v', byte('0' + i)}),
			Verdict:        taskverification.VerdictPass,
			ScoreBP:        scoreBP,
			AnalyzerFamily: "heuristic",
			Stake:          100,
		}
	}
	return out
}

// failVotes constructs a uniform-stake vote set where each validator
// records a FAIL vote.
func failVotes(count int) []taskverification.TaskVerificationVoteRecord {
	out := make([]taskverification.TaskVerificationVoteRecord, count)
	for i := 0; i < count; i++ {
		out[i] = taskverification.TaskVerificationVoteRecord{
			ValidatorID:    crypto.AgentID([]byte{'f', byte('0' + i)}),
			Verdict:        taskverification.VerdictFail,
			ScoreBP:        0,
			AnalyzerFamily: "embedding",
			Stake:          100,
		}
	}
	return out
}

// TestTVConsensusLKConsumer_IsComplete_PassSealed covers the
// mathematically-sealed accept case: 3/3 pass on a 3-validator
// cluster. Threshold ceil(2*300/3)=200; passWeight=300 >= 200;
// failWeight+maxRemaining = 0 + 0 = 0; 300 > 0 → sealed.
func TestTVConsensusLKConsumer_IsComplete_PassSealed(t *testing.T) {
	c, tm, tl, em, store := newTestTVLKConsumer(t, 300)
	_, _ = setupTaskLK(t, tm, tl, em, store, 10_000, "round-1", passVotes(3, 7000))

	complete, err := c.IsComplete(dispatch.RoundState{LogicalKey: "round-1"})
	if err != nil {
		t.Fatalf("IsComplete: %v", err)
	}
	if !complete {
		t.Error("expected passSealed=true on 3/3 pass")
	}
}

// TestTVConsensusLKConsumer_IsComplete_FailSealed covers the
// mathematically-sealed reject case: 3/3 fail on a 3-validator
// cluster.
func TestTVConsensusLKConsumer_IsComplete_FailSealed(t *testing.T) {
	c, tm, tl, em, store := newTestTVLKConsumer(t, 300)
	_, _ = setupTaskLK(t, tm, tl, em, store, 10_000, "round-1", failVotes(3))

	complete, err := c.IsComplete(dispatch.RoundState{LogicalKey: "round-1"})
	if err != nil {
		t.Fatalf("IsComplete: %v", err)
	}
	if !complete {
		t.Error("expected failSealed=true on 3/3 fail")
	}
}

// TestTVConsensusLKConsumer_IsComplete_Tied_NotSealed covers the
// critical negative case: a vote-weight tie with no remaining
// validators. threshold ceil(2*200/3)=134; passWeight=100 < 134;
// failWeight=100 < 134; neither sealed. Returns false.
//
// The F4B bug class manifests ON ties — if IsComplete were to return
// true here, the consumer would still pick per-node arrival order.
// The seal rule guarantees IsComplete=false on ties.
func TestTVConsensusLKConsumer_IsComplete_Tied_NotSealed(t *testing.T) {
	c, tm, tl, em, store := newTestTVLKConsumer(t, 200)
	votes := []taskverification.TaskVerificationVoteRecord{
		{ValidatorID: "v0", Verdict: taskverification.VerdictPass, Stake: 100, AnalyzerFamily: "heuristic"},
		{ValidatorID: "v1", Verdict: taskverification.VerdictFail, Stake: 100, AnalyzerFamily: "embedding"},
	}
	_, _ = setupTaskLK(t, tm, tl, em, store, 10_000, "round-tied", votes)

	complete, err := c.IsComplete(dispatch.RoundState{LogicalKey: "round-tied"})
	if err != nil {
		t.Fatalf("IsComplete: %v", err)
	}
	if complete {
		t.Error("expected IsComplete=false on 100-100 tie (seal rule protects against premature fire)")
	}
}

// TestTVConsensusLKConsumer_IsComplete_BelowThreshold covers the
// under-quorum case: only 1 of 3 validators has voted. No verdict
// has reached threshold yet; IsComplete=false.
func TestTVConsensusLKConsumer_IsComplete_BelowThreshold(t *testing.T) {
	c, tm, tl, em, store := newTestTVLKConsumer(t, 300)
	_, _ = setupTaskLK(t, tm, tl, em, store, 10_000, "round-1", passVotes(1, 7000))

	complete, err := c.IsComplete(dispatch.RoundState{LogicalKey: "round-1"})
	if err != nil {
		t.Fatalf("IsComplete: %v", err)
	}
	if complete {
		t.Error("expected IsComplete=false on 1/3 pass (below supermajority)")
	}
}

// TestTVConsensusLKConsumer_IsComplete_MissingRound_False covers
// the round-not-yet-projected case: IsComplete must return false
// (not error) so the dispatcher defers and retries on the next
// Admit after votes have populated.
func TestTVConsensusLKConsumer_IsComplete_MissingRound_False(t *testing.T) {
	c, _, _, _, _ := newTestTVLKConsumer(t, 300)
	complete, err := c.IsComplete(dispatch.RoundState{LogicalKey: "nope-no-such-round"})
	if err != nil {
		t.Errorf("IsComplete on missing round should not error; got %v", err)
	}
	if complete {
		t.Error("IsComplete should be false when round is missing")
	}
}

// TestTVConsensusLKConsumer_IsComplete_ZeroActiveWeight_False covers
// the bootstrap case: activeWeightFn returns 0 (no validator set
// loaded). Must NOT return complete=true (dividing by zero
// threshold would be a silent error).
func TestTVConsensusLKConsumer_IsComplete_ZeroActiveWeight_False(t *testing.T) {
	c, tm, tl, em, store := newTestTVLKConsumer(t, 0)
	_, _ = setupTaskLK(t, tm, tl, em, store, 10_000, "round-zero", passVotes(3, 7000))

	complete, err := c.IsComplete(dispatch.RoundState{LogicalKey: "round-zero"})
	if err != nil {
		t.Fatalf("IsComplete: %v", err)
	}
	if complete {
		t.Error("IsComplete with zero active weight should be false (defensive)")
	}
}

// --- DeriveOutcome tests ----------------------------------------------------

// TestTVConsensusLKConsumer_DeriveOutcome_PassVerdict verifies the
// weighted-mean ScoreBP computation and the pass-verdict
// participant enumeration.
func TestTVConsensusLKConsumer_DeriveOutcome_PassVerdict(t *testing.T) {
	c, tm, tl, em, store := newTestTVLKConsumer(t, 300)
	votes := []taskverification.TaskVerificationVoteRecord{
		{ValidatorID: "v-alpha", Verdict: taskverification.VerdictPass, ScoreBP: 6000, Stake: 100, AnalyzerFamily: "heuristic"},
		{ValidatorID: "v-beta", Verdict: taskverification.VerdictPass, ScoreBP: 7000, Stake: 100, AnalyzerFamily: "embedding"},
		{ValidatorID: "v-gamma", Verdict: taskverification.VerdictPass, ScoreBP: 8000, Stake: 100, AnalyzerFamily: "heuristic"},
	}
	_, _ = setupTaskLK(t, tm, tl, em, store, 10_000, "round-1", votes)

	outcome, err := c.DeriveOutcome(dispatch.RoundState{LogicalKey: "round-1"})
	if err != nil {
		t.Fatalf("DeriveOutcome: %v", err)
	}
	if outcome.Verdict != dispatch.VerdictAccept {
		t.Errorf("Verdict: got %q want accept", outcome.Verdict)
	}
	// Weighted mean (6000 + 7000 + 8000) / 3 = 7000.
	if outcome.ScoreBP != 7000 {
		t.Errorf("ScoreBP: got %d want 7000", outcome.ScoreBP)
	}
	// Participant order: alpha, beta, gamma (lex).
	if len(outcome.ParticipatingIDs) != 3 {
		t.Fatalf("ParticipatingIDs length: got %d want 3", len(outcome.ParticipatingIDs))
	}
	if outcome.ParticipatingIDs[0] != "v-alpha" ||
		outcome.ParticipatingIDs[1] != "v-beta" ||
		outcome.ParticipatingIDs[2] != "v-gamma" {
		t.Errorf("ParticipatingIDs: got %v want [alpha,beta,gamma]", outcome.ParticipatingIDs)
	}
}

// TestTVConsensusLKConsumer_DeriveOutcome_FailVerdict verifies the
// reject-side derivation: ScoreBP=0, fail voters enumerated.
func TestTVConsensusLKConsumer_DeriveOutcome_FailVerdict(t *testing.T) {
	c, tm, tl, em, store := newTestTVLKConsumer(t, 300)
	votes := []taskverification.TaskVerificationVoteRecord{
		{ValidatorID: "v-one", Verdict: taskverification.VerdictFail, Stake: 100, AnalyzerFamily: "heuristic"},
		{ValidatorID: "v-two", Verdict: taskverification.VerdictFail, Stake: 100, AnalyzerFamily: "embedding"},
		{ValidatorID: "v-three", Verdict: taskverification.VerdictFail, Stake: 100, AnalyzerFamily: "heuristic"},
	}
	_, _ = setupTaskLK(t, tm, tl, em, store, 10_000, "round-1", votes)

	outcome, err := c.DeriveOutcome(dispatch.RoundState{LogicalKey: "round-1"})
	if err != nil {
		t.Fatalf("DeriveOutcome: %v", err)
	}
	if outcome.Verdict != dispatch.VerdictReject {
		t.Errorf("Verdict: got %q want reject", outcome.Verdict)
	}
	if outcome.ScoreBP != 0 {
		t.Errorf("ScoreBP: got %d want 0 (reject path has no score)", outcome.ScoreBP)
	}
	if len(outcome.ParticipatingIDs) != 3 {
		t.Errorf("ParticipatingIDs length: got %d want 3", len(outcome.ParticipatingIDs))
	}
}

// --- Apply tests ------------------------------------------------------------

// TestTVConsensusLKConsumer_Apply_AcceptPath exercises the
// full settler invocation shape under the LK consumer. After Apply
// the ledger must show the v4.1 73/23/2/2 distribution identically
// to the old content-hash consumer's accept path.
func TestTVConsensusLKConsumer_Apply_AcceptPath(t *testing.T) {
	c, tm, tl, em, store := newTestTVLKConsumer(t, 300)
	budget := uint64(10_000)
	taskID, _ := setupTaskLK(t, tm, tl, em, store, budget, "round-1", passVotes(3, 7000))

	outcome := dispatch.Outcome{
		Verdict:          dispatch.VerdictAccept,
		ScoreBP:          7000,
		ParticipatingIDs: []crypto.AgentID{"v0", "v1", "v2"},
	}
	if err := c.Apply(context.Background(), event.EventID("trigger"), "round-1", outcome); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	workerBal, _ := tl.Balance(crypto.AgentID("worker"))
	if workerBal != budget*7300/10000 {
		t.Errorf("worker balance: got %d want %d", workerBal, budget*7300/10000)
	}
	task, _ := tm.Get(taskID)
	if task.Status != tasks.TaskStatusCompleted {
		t.Errorf("task status after accept: got %s want completed", task.Status)
	}
	// Conservation.
	var totalValidators uint64
	// Only v0 voted pass (with distinct ID); other validator IDs
	// in the passVotes helper share bytes but via a single-seat
	// distribution totalValidators will match budget*2300/10000.
	for _, id := range []crypto.AgentID{"v0", "v1", "v2"} {
		bal, _ := tl.Balance(id)
		totalValidators += bal
	}
	treasuryBal, _ := tl.Balance(crypto.AgentID("genesis:treasury"))
	total := workerBal + totalValidators + treasuryBal
	if total != budget {
		t.Errorf("conservation: total %d != budget %d (worker=%d validators=%d treasury=%d)",
			total, budget, workerBal, totalValidators, treasuryBal)
	}
}

// TestTVConsensusLKConsumer_Apply_TerminalTask_NoOp covers the
// pre-check 1 short-circuit: if the task is already terminal (e.g.,
// recovered from a prior instance's Apply crash after terminal
// transition but before admission-record persist), Apply must no-op
// and must not attempt a second settlement.
func TestTVConsensusLKConsumer_Apply_TerminalTask_NoOp(t *testing.T) {
	c, tm, tl, em, store := newTestTVLKConsumer(t, 300)
	taskID, _ := setupTaskLK(t, tm, tl, em, store, 10_000, "round-1", passVotes(3, 7000))

	// Manually drive the task to terminal.
	if err := tm.ApplyVerificationConsensusResolution(taskID, "pass", 7000, "round-1"); err != nil {
		t.Fatalf("manual terminal transition: %v", err)
	}

	// Record balances — Apply must not change them.
	beforeWorker, _ := tl.Balance(crypto.AgentID("worker"))
	beforeTreasury, _ := tl.Balance(crypto.AgentID("genesis:treasury"))

	outcome := dispatch.Outcome{Verdict: dispatch.VerdictAccept, ScoreBP: 7000}
	if err := c.Apply(context.Background(), event.EventID("trigger"), "round-1", outcome); err != nil {
		t.Fatalf("Apply on terminal task should not error: %v", err)
	}

	afterWorker, _ := tl.Balance(crypto.AgentID("worker"))
	afterTreasury, _ := tl.Balance(crypto.AgentID("genesis:treasury"))
	if beforeWorker != afterWorker || beforeTreasury != afterTreasury {
		t.Errorf("terminal-pre-check failed: worker %d→%d treasury %d→%d",
			beforeWorker, afterWorker, beforeTreasury, afterTreasury)
	}
}

// TestTVConsensusLKConsumer_Apply_HasSettlementStarted_NoOp covers
// the pre-check 2 short-circuit: an escrow entry with any paid
// flag set causes Apply to skip without invoking the settler.
func TestTVConsensusLKConsumer_Apply_HasSettlementStarted_NoOp(t *testing.T) {
	c, tm, tl, em, store := newTestTVLKConsumer(t, 300)
	taskID, _ := setupTaskLK(t, tm, tl, em, store, 10_000, "round-1", passVotes(3, 7000))

	// Drive a partial settlement via the F5 5B canonical applicator:
	// worker record (5000 ≤ 10000) succeeds; treasury record (20000 > 5000
	// remaining) fails. Result: entry persists with WorkerPaid=true and
	// no other flags. Equivalent state to the legacy ReleaseSettlement
	// partial-failure case (Plan v3 §0 decision 9 reframe; the
	// behavioral invariant tested — HasSettlementStarted pre-check
	// short-circuits subsequent Apply — is preserved).
	partialRecords := []derivation.PayoutRecord{
		{
			CanonicalID:       "test-lk-mid-settlement-worker",
			DerivationVersion: derivation.DerivationVersion,
			SettlementKey:     derivation.SettlementKey{TaskID: taskID, RoundID: "round-1", FundingReference: "funding-1"},
			Recipient:         derivation.Recipient{ID: "worker", Role: derivation.RoleWorker},
			Amount:            derivation.Amount{Value: 5000, Currency: derivation.CurrencyAET},
			Purpose:           derivation.Purpose{Tag: derivation.TagWorkerPayout},
		},
		{
			CanonicalID:       "test-lk-mid-settlement-treasury-fails",
			DerivationVersion: derivation.DerivationVersion,
			SettlementKey:     derivation.SettlementKey{TaskID: taskID, RoundID: "round-1", FundingReference: "funding-1"},
			Recipient:         derivation.Recipient{ID: "genesis:treasury", Role: derivation.RoleTreasury},
			Amount:            derivation.Amount{Value: 20000, Currency: derivation.CurrencyAET},
			Purpose:           derivation.Purpose{Tag: derivation.TagTreasuryRemainder},
		},
	}
	_ = em.ApplySettlementRecords(taskID, partialRecords)
	if !em.HasSettlementStarted(taskID) {
		t.Fatalf("test setup: HasSettlementStarted should be true after partial payout")
	}

	beforeWorker, _ := tl.Balance(crypto.AgentID("worker"))

	outcome := dispatch.Outcome{Verdict: dispatch.VerdictAccept, ScoreBP: 7000}
	if err := c.Apply(context.Background(), event.EventID("trigger"), "round-1", outcome); err != nil {
		t.Fatalf("Apply with HasSettlementStarted should not error: %v", err)
	}

	afterWorker, _ := tl.Balance(crypto.AgentID("worker"))
	if afterWorker != beforeWorker {
		t.Errorf("HasSettlementStarted pre-check failed: worker %d→%d", beforeWorker, afterWorker)
	}
}

// --- RecoveryProbe tests ----------------------------------------------------

// TestTVConsensusLKConsumer_RecoveryProbe_Finalized_Completed
// verifies positive-evidence recovery: a round in
// RoundStateFinalizedAccept returns RecoveryCompleted.
func TestTVConsensusLKConsumer_RecoveryProbe_Finalized_Completed(t *testing.T) {
	c, tm, tl, em, store := newTestTVLKConsumer(t, 300)
	_, round := setupTaskLK(t, tm, tl, em, store, 10_000, "round-1", passVotes(3, 7000))
	// Transition the round to FinalizedAccept as positive evidence.
	round.State = taskverification.RoundStateFinalizedAccept
	if err := store.SaveRound(context.Background(), round); err != nil {
		t.Fatalf("SaveRound: %v", err)
	}

	status, err := c.RecoveryProbe(context.Background(), "round-1")
	if err != nil {
		t.Fatalf("RecoveryProbe: %v", err)
	}
	if status != dispatch.RecoveryCompleted {
		t.Errorf("RecoveryProbe: got %v want RecoveryCompleted", status)
	}
}

// TestTVConsensusLKConsumer_RecoveryProbe_Open_NotStarted verifies
// that an Open round (not yet finalized) returns RecoveryNotStarted
// so the dispatcher's next Admit drives a retry.
func TestTVConsensusLKConsumer_RecoveryProbe_Open_NotStarted(t *testing.T) {
	c, tm, tl, em, store := newTestTVLKConsumer(t, 300)
	_, round := setupTaskLK(t, tm, tl, em, store, 10_000, "round-open", passVotes(3, 7000))

	// Override setupTaskLK's default FinalizedAccept state — this test
	// specifically exercises the Open-round case, so the round must be
	// non-terminal. Save the override back to the store.
	round.State = taskverification.RoundStateOpen
	round.FinalVerdict = taskverification.Verdict(0)
	if err := store.SaveRound(context.Background(), round); err != nil {
		t.Fatalf("SaveRound: %v", err)
	}

	status, err := c.RecoveryProbe(context.Background(), "round-open")
	if err != nil {
		t.Fatalf("RecoveryProbe: %v", err)
	}
	if status != dispatch.RecoveryNotStarted {
		t.Errorf("RecoveryProbe: got %v want RecoveryNotStarted", status)
	}
}

// TestTVConsensusLKConsumer_RecoveryProbe_NoRound_NotStarted
// verifies that a missing round (not yet projected) returns
// RecoveryNotStarted rather than erroring.
func TestTVConsensusLKConsumer_RecoveryProbe_NoRound_NotStarted(t *testing.T) {
	c, _, _, _, _ := newTestTVLKConsumer(t, 300)
	status, err := c.RecoveryProbe(context.Background(), "nope")
	if err != nil {
		t.Fatalf("RecoveryProbe: %v", err)
	}
	if status != dispatch.RecoveryNotStarted {
		t.Errorf("RecoveryProbe: got %v want RecoveryNotStarted", status)
	}
}

// --- End-to-end dispatcher tests --------------------------------------------

// TestTVConsensusLKConsumer_EndToEnd_OneApplyPerRoundID is the
// load-bearing F4B property: multiple byte-distinct TVConsensus
// events for the SAME RoundID produce exactly ONE Apply invocation
// via the dispatcher's logical-key admission. Different canonical
// hashes, different payload FinalVerdict values — all collapse to
// one admission record keyed by RoundID.
//
// This is the direct consumer-level analogue of the tied-weight
// harness's divergence reproduction. If this test fails, the F4B
// fix is not landing at the right layer.
func TestTVConsensusLKConsumer_EndToEnd_OneApplyPerRoundID(t *testing.T) {
	c, tm, tl, em, store := newTestTVLKConsumer(t, 300)
	taskID, _ := setupTaskLK(t, tm, tl, em, store, 10_000, "round-1", passVotes(3, 7000))

	// Build the dispatcher plumbing.
	d, adm := newTestDispatcherForLK(t)
	if err := d.RegisterLogicalKey(c); err != nil {
		t.Fatalf("RegisterLogicalKey: %v", err)
	}

	// Two byte-distinct events with DIFFERENT FinalVerdict (pass vs
	// fail) but SAME RoundID. Both project to the same LogicalKey;
	// only one Apply must fire.
	ev1 := makeLKConsensusEvent(t, taskID, "round-1", "pass")
	ev2 := makeLKConsensusEvent(t, taskID, "round-1", "fail")
	if ev1.ID == ev2.ID {
		t.Fatalf("test setup: events should be byte-distinct")
	}

	if err := d.Admit(context.Background(), ev1); err != nil {
		t.Fatalf("Admit ev1: %v", err)
	}
	if err := d.Admit(context.Background(), ev2); err != nil {
		t.Fatalf("Admit ev2: %v", err)
	}

	// Exactly one settlement landed: task terminal = completed, not
	// rejected. The canonical vote set is 3/3 pass, so the logical-
	// key path derives VerdictAccept — NOT whichever triggering
	// event arrived first.
	task, _ := tm.Get(taskID)
	if task.Status != tasks.TaskStatusCompleted {
		t.Errorf("task status: got %s want completed (canonical vote-derived verdict)", task.Status)
	}
	workerBal, _ := tl.Balance(crypto.AgentID("worker"))
	if workerBal != 10_000*7300/10000 {
		t.Errorf("worker balance: got %d want %d (single accept-path payout)", workerBal, 10_000*7300/10000)
	}

	// Exactly one admission record with StateApplied under the
	// logical-key prefix.
	storeKey := dispatch.LogicalAdmissionKey("tv_consensus_settlement_lk", "round-1")
	rec, err := adm.GetAdmission(storeKey)
	if err != nil {
		t.Fatalf("GetAdmission: %v", err)
	}
	if rec.State != dispatch.StateApplied {
		t.Errorf("State: got %v want applied", rec.State)
	}
	if rec.Strategy != dispatch.AdmissionStrategyLogicalKey {
		t.Errorf("Strategy: got %v want logical-key", rec.Strategy)
	}
}

// TestTVConsensusLKConsumer_EndToEnd_AdvisoryVerdictIgnored
// exercises C-17: the triggering event's FinalVerdict is advisory.
// The round's canonical vote set is 3/3 FAIL; even though the
// triggering event carries FinalVerdict="pass", the consumer must
// derive VerdictReject from the vote set and settle accordingly.
func TestTVConsensusLKConsumer_EndToEnd_AdvisoryVerdictIgnored(t *testing.T) {
	c, tm, tl, em, store := newTestTVLKConsumer(t, 300)
	taskID, round := setupTaskLK(t, tm, tl, em, store, 10_000, "round-f", failVotes(3))

	// Override setupTaskLK's default FinalizedAccept state — this test
	// exercises the reject path (3/3 FAIL votes). In production the
	// recognition fabric's TaskVerificationConsensusConsumer transitions
	// the round to FinalizedReject based on the canonical vote set; the
	// test mirrors that pre-Apply state.
	round.State = taskverification.RoundStateFinalizedReject
	round.FinalVerdict = taskverification.VerdictFail
	if err := store.SaveRound(context.Background(), round); err != nil {
		t.Fatalf("SaveRound (reject override): %v", err)
	}

	d, _ := newTestDispatcherForLK(t)
	if err := d.RegisterLogicalKey(c); err != nil {
		t.Fatalf("RegisterLogicalKey: %v", err)
	}

	// Triggering event carries "pass" in its payload — this is the
	// advisory field the old content-hash path would have trusted.
	ev := makeLKConsensusEvent(t, taskID, "round-f", "pass")

	if err := d.Admit(context.Background(), ev); err != nil {
		t.Fatalf("Admit: %v", err)
	}

	task, _ := tm.Get(taskID)
	if task.Status != tasks.TaskStatusRejected {
		t.Errorf("task status: got %s want rejected (advisory 'pass' must not override canonical 'fail' votes)",
			task.Status)
	}
	// Worker must have received NO balance (reject refunds poster).
	workerBal, _ := tl.Balance(crypto.AgentID("worker"))
	if workerBal != 0 {
		t.Errorf("worker balance: got %d want 0 (reject path pays poster, not worker)", workerBal)
	}
}

// --- Conformance ------------------------------------------------------------

// TestTVConsensusLKConsumer_Conformance runs the baseline Type E
// behavioral suite. The consumer is wrapped in a factory that sets
// up a minimal, deterministic round so IsComplete returns true for
// the synthetic event the suite generates. Conformance validates
// the interface-shape properties (per-key Apply, determinism of
// IsComplete/DeriveOutcome) without specifying the consumer's
// internal semantics.
func TestTVConsensusLKConsumer_Conformance(t *testing.T) {
	conformance.RunLogicalKeyConformance(t, func() (dispatch.LogicalKeyConsumer, func()) {
		tl := ledger.NewTransferLedger()
		_ = tl.FundAgent("poster", 1_000_000)
		tm := tasks.NewTaskManager()
		em := escrow.New(tl)

		opts := badger.DefaultOptions("").WithInMemory(true).WithLogger(nil)
		db, _ := badger.Open(opts)
		store := taskverification.NewBadgerStore(db)
		settler := settlement.NewVerificationConsensusSettler(
			tm, tl, em, &tvStubDAG{}, nil, "genesis:treasury", nil, true)
		c := dispatch.NewTVConsensusLogicalKeyConsumer(settler, store, tm, em, func() uint64 { return 300 })
		return c, func() { db.Close() }
	})
}

// --- Shared test helpers (LK dispatcher) ------------------------------------

// newTestDispatcherForLK builds a fresh Dispatcher with an in-
// memory AdmissionStore and a stub DAG, returning the dispatcher
// and the store so tests can inspect admission records directly.
// Mirrors the helper in logical_key_test.go's dispatcher_test.go
// counterpart but lives in _test.go of the current package so the
// tv_consensus_lk tests are self-contained.
func newTestDispatcherForLK(t *testing.T) (*dispatch.Dispatcher, dispatch.AdmissionStore) {
	t.Helper()
	store := &memAdmissionStoreLK{records: make(map[string]*dispatch.AdmissionRecord)}
	d := dispatch.NewDispatcher(store, stubDAGLK{}, func() uint64 { return 1 })
	return d, store
}

// memAdmissionStoreLK is the local in-memory admission store used
// by the tv_consensus_lk tests. Mirrors memAdmissionStore in the
// cross_node harness but kept package-local to avoid cross-package
// test coupling.
type memAdmissionStoreLK struct {
	records map[string]*dispatch.AdmissionRecord
}

func (m *memAdmissionStoreLK) GetAdmission(key string) (*dispatch.AdmissionRecord, error) {
	rec, ok := m.records[key]
	if !ok {
		return nil, errNotFoundLK
	}
	cp := *rec
	if rec.Consumers != nil {
		cp.Consumers = make(map[string]dispatch.PerConsumerStatus, len(rec.Consumers))
		// safe: iteration order does not affect canonical state (deep-copy identity preservation)
		for k, v := range rec.Consumers {
			cp.Consumers[k] = v
		}
	}
	return &cp, nil
}

func (m *memAdmissionStoreLK) PutAdmission(key string, rec *dispatch.AdmissionRecord) error {
	cp := *rec
	if rec.Consumers != nil {
		cp.Consumers = make(map[string]dispatch.PerConsumerStatus, len(rec.Consumers))
		// safe: iteration order does not affect canonical state (deep-copy identity preservation)
		for k, v := range rec.Consumers {
			cp.Consumers[k] = v
		}
	}
	m.records[key] = &cp
	return nil
}

func (m *memAdmissionStoreLK) DeleteAdmission(key string) error {
	delete(m.records, key)
	return nil
}

func (m *memAdmissionStoreLK) AllAdmissions() ([]*dispatch.AdmissionRecord, error) {
	out := make([]*dispatch.AdmissionRecord, 0, len(m.records))
	// safe: iteration order does not affect canonical state (commutative caller aggregates)
	for _, rec := range m.records {
		out = append(out, rec)
	}
	return out, nil
}

// errNotFoundLK mirrors the BadgerDB "Key not found" sentinel shape
// the dispatcher's isNotFound predicate checks.
var errNotFoundLK = &notFoundErrorLK{}

type notFoundErrorLK struct{}

func (*notFoundErrorLK) Error() string { return "Key not found" }

// stubDAGLK is a no-tips DAG so the dispatcher's currentAnchor
// returns empty and VerifyAnchor passes unconditionally.
type stubDAGLK struct{}

func (stubDAGLK) Tips() []event.EventID { return nil }
func (stubDAGLK) IsAncestor(_, _ event.EventID) (bool, error) {
	return false, nil
}
func (stubDAGLK) Get(_ event.EventID) (*event.Event, error) {
	return nil, errNotFoundLK
}
