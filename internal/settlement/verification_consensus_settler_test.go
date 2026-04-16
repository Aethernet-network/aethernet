package settlement

import (
	"context"
	"testing"

	"github.com/Aethernet-network/aethernet/internal/crypto"
	"github.com/Aethernet-network/aethernet/internal/escrow"
	"github.com/Aethernet-network/aethernet/internal/escrow_testhelpers"
	"github.com/Aethernet-network/aethernet/internal/event"
	"github.com/Aethernet-network/aethernet/internal/ledger"
	"github.com/Aethernet-network/aethernet/internal/tasks"
	"github.com/Aethernet-network/aethernet/internal/taskverification"
)

func setupSettlerTest(t *testing.T, budget uint64) (
	*VerificationConsensusSettler,
	*tasks.TaskManager,
	*ledger.TransferLedger,
	*escrow.Escrow,
) {
	t.Helper()
	tl := ledger.NewTransferLedger()
	tm := tasks.NewTaskManager()
	em := escrow.New(tl)

	// Fund poster so escrow can be held.
	_ = tl.FundAgent("poster-1", budget*10)

	// Create and claim a task, then submit.
	_, _ = tm.PostTask("poster-1", "test task", "desc", "research", budget)
	allTasks := tm.Search(tasks.TaskStatusOpen, "", 0)
	if len(allTasks) == 0 {
		t.Fatal("no task created")
	}
	taskID := allTasks[0].ID
	_ = tm.ClaimTask(taskID, "worker-1")
	_ = tm.SubmitResult(taskID, "worker-1", "sha256:test", "note", "")

	// Fund+register via the quarantined test helper; the settler test path
	// pre-populates the escrow so the catch-up branch (which needs a DAG
	// scanner) is not exercised.
	_ = escrow_testhelpers.FundAndRegisterEscrowForTest(tl, em, taskID, "poster-1", budget)

	calc := NewGenerationLedgerCalculator(nil, func(_ event.EventID) float64 { return 1.0 })
	settler := NewVerificationConsensusSettler(tm, tl, em, nil, calc, "genesis:treasury", nil)

	return settler, tm, tl, em
}

func makeRoundWithVotes(taskID string, verdicts map[string]taskverification.Verdict) *taskverification.TaskVerificationRound {
	round := &taskverification.TaskVerificationRound{
		RoundID:               "round-test",
		TaskID:                taskID,
		WorkerID:              "worker-1",
		PosterID:              "poster-1",
		ParticipatingFamilies: map[string]uint64{},
		Votes:                 []taskverification.TaskVerificationVoteRecord{},
	}
	for vid, v := range verdicts {
		round.Votes = append(round.Votes, taskverification.TaskVerificationVoteRecord{
			ValidatorID:    crypto.AgentID(vid),
			Verdict:        v,
			ScoreBP:        7000,
			AnalyzerFamily: "heuristic",
			Stake:          100,
		})
	}
	return round
}

func TestSettle_Accept_73_23_2_2(t *testing.T) {
	budget := uint64(10000)
	settler, tm, tl, _ := setupSettlerTest(t, budget)
	allTasks := tm.Search(tasks.TaskStatusSubmitted, "", 0)
	taskID := allTasks[0].ID

	round := makeRoundWithVotes(taskID, map[string]taskverification.Verdict{
		"validator-1": taskverification.VerdictPass,
		"validator-2": taskverification.VerdictPass,
		"validator-3": taskverification.VerdictFail,
	})

	payload := &event.TaskVerificationConsensusPayload{
		RoundID:      "round-test",
		TaskID:       taskID,
		FinalVerdict: "pass",
		FinalScoreBP: 7500,
		WorkerID:     "worker-1",
		PosterID:     "poster-1",
	}

	result, err := settler.Settle(context.Background(), payload, round)
	if err != nil {
		t.Fatalf("Settle: %v", err)
	}
	if !result.Applied {
		t.Fatal("should be applied")
	}

	// Verify the math.
	expectedWorker := budget * 7300 / 10000 // 7300
	if result.WorkerPayout != expectedWorker {
		t.Errorf("WorkerPayout = %d; want %d", result.WorkerPayout, expectedWorker)
	}

	// Total distributed must equal budget.
	if result.TotalDistributed != budget {
		t.Errorf("TotalDistributed = %d; want %d", result.TotalDistributed, budget)
	}

	// Worker should have funds.
	workerBal, _ := tl.Balance("worker-1")
	if workerBal < expectedWorker {
		t.Errorf("worker balance = %d; want >= %d", workerBal, expectedWorker)
	}

	// Task should be completed.
	task, _ := tm.Get(taskID)
	if task.Status != tasks.TaskStatusCompleted {
		t.Errorf("task status = %s; want completed", task.Status)
	}
}

func TestSettle_AcceptValidatorVotedFailGetsNothing(t *testing.T) {
	budget := uint64(10000)
	settler, tm, _, _ := setupSettlerTest(t, budget)
	allTasks := tm.Search(tasks.TaskStatusSubmitted, "", 0)
	taskID := allTasks[0].ID

	round := makeRoundWithVotes(taskID, map[string]taskverification.Verdict{
		"validator-1": taskverification.VerdictPass,
		"validator-2": taskverification.VerdictPass,
		"validator-3": taskverification.VerdictFail,
	})

	payload := &event.TaskVerificationConsensusPayload{
		RoundID: "round-test", TaskID: taskID, FinalVerdict: "pass",
		WorkerID: "worker-1", PosterID: "poster-1",
	}

	result, _ := settler.Settle(context.Background(), payload, round)

	// validator-3 voted fail — should get nothing.
	if amt, ok := result.ValidatorPayouts["validator-3"]; ok && amt > 0 {
		t.Errorf("fail-voting validator got %d; want 0", amt)
	}
	// validator-1 and validator-2 should share the pool.
	if len(result.ValidatorPayouts) != 2 {
		t.Errorf("agreeing validators = %d; want 2", len(result.ValidatorPayouts))
	}
}

func TestSettle_Reject(t *testing.T) {
	budget := uint64(10000)
	settler, tm, tl, _ := setupSettlerTest(t, budget)
	allTasks := tm.Search(tasks.TaskStatusSubmitted, "", 0)
	taskID := allTasks[0].ID

	round := makeRoundWithVotes(taskID, map[string]taskverification.Verdict{
		"validator-1": taskverification.VerdictFail,
		"validator-2": taskverification.VerdictFail,
	})

	payload := &event.TaskVerificationConsensusPayload{
		RoundID: "round-test", TaskID: taskID, FinalVerdict: "fail",
		WorkerID: "worker-1", PosterID: "poster-1",
	}

	result, err := settler.Settle(context.Background(), payload, round)
	if err != nil {
		t.Fatalf("Settle: %v", err)
	}

	expectedPoster := budget * 7300 / 10000
	if result.PosterRefund != expectedPoster {
		t.Errorf("PosterRefund = %d; want %d", result.PosterRefund, expectedPoster)
	}
	if result.TotalDistributed != budget {
		t.Errorf("TotalDistributed = %d; want %d", result.TotalDistributed, budget)
	}

	task, _ := tm.Get(taskID)
	if task.Status != tasks.TaskStatusRejected {
		t.Errorf("task status = %s; want rejected", task.Status)
	}

	// Poster should get refund.
	posterBal, _ := tl.Balance("poster-1")
	if posterBal < expectedPoster {
		t.Errorf("poster balance after refund = %d; want >= %d", posterBal, expectedPoster)
	}
}

func TestSettle_Dispute_50_50(t *testing.T) {
	budget := uint64(10000)
	settler, tm, _, _ := setupSettlerTest(t, budget)
	allTasks := tm.Search(tasks.TaskStatusSubmitted, "", 0)
	taskID := allTasks[0].ID

	round := makeRoundWithVotes(taskID, map[string]taskverification.Verdict{})

	payload := &event.TaskVerificationConsensusPayload{
		RoundID: "round-test", TaskID: taskID, FinalVerdict: "abstain",
		WorkerID: "worker-1", PosterID: "poster-1",
	}

	result, err := settler.Settle(context.Background(), payload, round)
	if err != nil {
		t.Fatalf("Settle: %v", err)
	}

	workerPortion := budget * 7300 / 10000
	expectedWorker := workerPortion / 2
	expectedPoster := workerPortion - expectedWorker
	expectedTreasury := budget - workerPortion

	if result.WorkerPayout != expectedWorker {
		t.Errorf("WorkerPayout = %d; want %d", result.WorkerPayout, expectedWorker)
	}
	if result.PosterRefund != expectedPoster {
		t.Errorf("PosterRefund = %d; want %d", result.PosterRefund, expectedPoster)
	}
	if result.TreasuryAmount != expectedTreasury {
		t.Errorf("TreasuryAmount = %d; want %d", result.TreasuryAmount, expectedTreasury)
	}
	if result.TotalDistributed != budget {
		t.Errorf("TotalDistributed = %d; want %d", result.TotalDistributed, budget)
	}

	task, _ := tm.Get(taskID)
	if task.Status != tasks.TaskStatusDisputedResolved {
		t.Errorf("task status = %s; want disputed_resolved", task.Status)
	}
}

func TestSettle_DisputeOddMicroAET(t *testing.T) {
	budget := uint64(10001)
	settler, tm, _, _ := setupSettlerTest(t, budget)
	allTasks := tm.Search(tasks.TaskStatusSubmitted, "", 0)
	taskID := allTasks[0].ID

	round := makeRoundWithVotes(taskID, map[string]taskverification.Verdict{})
	payload := &event.TaskVerificationConsensusPayload{
		RoundID: "round-test", TaskID: taskID, FinalVerdict: "abstain",
		WorkerID: "worker-1", PosterID: "poster-1",
	}

	result, _ := settler.Settle(context.Background(), payload, round)

	// Extra micro-AET goes to poster on odd splits.
	if result.PosterRefund < result.WorkerPayout {
		t.Errorf("poster (%d) should get >= worker (%d) on odd split", result.PosterRefund, result.WorkerPayout)
	}
	if result.TotalDistributed != budget {
		t.Errorf("TotalDistributed = %d; want %d", result.TotalDistributed, budget)
	}
}

func TestSettle_Idempotent(t *testing.T) {
	budget := uint64(10000)
	settler, tm, _, _ := setupSettlerTest(t, budget)
	allTasks := tm.Search(tasks.TaskStatusSubmitted, "", 0)
	taskID := allTasks[0].ID

	round := makeRoundWithVotes(taskID, map[string]taskverification.Verdict{
		"v1": taskverification.VerdictPass,
	})
	payload := &event.TaskVerificationConsensusPayload{
		RoundID: "round-test", TaskID: taskID, FinalVerdict: "pass",
		WorkerID: "worker-1", PosterID: "poster-1",
	}

	_, _ = settler.Settle(context.Background(), payload, round)
	// Second call should be idempotent.
	result2, err := settler.Settle(context.Background(), payload, round)
	if err != nil {
		t.Fatalf("second Settle should not error: %v", err)
	}
	if !result2.AlreadyApplied {
		t.Error("second application should be AlreadyApplied")
	}
}

func TestSettle_TotalDistributionEqualsBudget(t *testing.T) {
	// Test across all three verdict types with various budgets.
	budgets := []uint64{1, 100, 999, 10000, 100001, 1000000}
	verdicts := []string{"pass", "fail", "abstain"}

	for _, budget := range budgets {
		for _, verdict := range verdicts {
			settler, tm, _, _ := setupSettlerTest(t, budget)
			allTasks := tm.Search(tasks.TaskStatusSubmitted, "", 0)
			taskID := allTasks[0].ID

			round := makeRoundWithVotes(taskID, map[string]taskverification.Verdict{
				"v1": taskverification.VerdictPass,
				"v2": taskverification.VerdictFail,
			})
			payload := &event.TaskVerificationConsensusPayload{
				RoundID: "round-test", TaskID: taskID, FinalVerdict: verdict,
				WorkerID: "worker-1", PosterID: "poster-1",
			}

			result, err := settler.Settle(context.Background(), payload, round)
			if err != nil {
				t.Fatalf("budget=%d verdict=%s: %v", budget, verdict, err)
			}
			if result.TotalDistributed != budget {
				t.Errorf("budget=%d verdict=%s: total=%d want=%d",
					budget, verdict, result.TotalDistributed, budget)
			}
		}
	}
}

// --- Q-weighted distribution tests ---

func setupQWeightedSettler(t *testing.T, budget uint64, qFn ValidatorQScoreFn) (
	*VerificationConsensusSettler, *tasks.TaskManager,
) {
	t.Helper()
	tl := ledger.NewTransferLedger()
	tm := tasks.NewTaskManager()
	em := escrow.New(tl)
	_ = tl.FundAgent("poster-1", budget*10)
	_, _ = tm.PostTask("poster-1", "q-test", "desc", "research", budget)
	allTasks := tm.Search(tasks.TaskStatusOpen, "", 0)
	taskID := allTasks[0].ID
	_ = tm.ClaimTask(taskID, "worker-1")
	_ = tm.SubmitResult(taskID, "worker-1", "sha256:q", "note", "")
	_ = escrow_testhelpers.FundAndRegisterEscrowForTest(tl, em, taskID, "poster-1", budget)
	calc := NewGenerationLedgerCalculator(nil, func(_ event.EventID) float64 { return 1.0 })
	settler := NewVerificationConsensusSettler(tm, tl, em, nil, calc, "genesis:treasury", qFn)
	return settler, tm
}

func TestSettle_QWeighted_AllNeutral_EqualsEvenSplit(t *testing.T) {
	budget := uint64(10000)
	qFn := ValidatorQScoreFn(func(_ crypto.AgentID, _, _ string) float64 { return 1.0 })
	settler, tm := setupQWeightedSettler(t, budget, qFn)
	allTasks := tm.Search(tasks.TaskStatusSubmitted, "", 0)
	taskID := allTasks[0].ID

	round := makeRoundWithVotes(taskID, map[string]taskverification.Verdict{
		"v1": taskverification.VerdictPass,
		"v2": taskverification.VerdictPass,
	})
	payload := &event.TaskVerificationConsensusPayload{
		RoundID: "round-q", TaskID: taskID, FinalVerdict: "pass",
		WorkerID: "worker-1", PosterID: "poster-1",
	}

	result, err := settler.Settle(context.Background(), payload, round)
	if err != nil {
		t.Fatalf("Settle: %v", err)
	}
	validatorPool := budget * 2300 / 10000
	perValidator := validatorPool / 2
	for vid, amt := range result.ValidatorPayouts {
		if amt < perValidator-1 || amt > perValidator+1 {
			t.Errorf("validator %s got %d; want ~%d (equal split)", vid, amt, perValidator)
		}
	}
}

func TestSettle_QWeighted_HighAndLow(t *testing.T) {
	budget := uint64(10000)
	qFn := ValidatorQScoreFn(func(id crypto.AgentID, _, _ string) float64 {
		if id == "v1" {
			return 0.9
		}
		return 0.1
	})
	settler, tm := setupQWeightedSettler(t, budget, qFn)
	allTasks := tm.Search(tasks.TaskStatusSubmitted, "", 0)
	taskID := allTasks[0].ID

	round := makeRoundWithVotes(taskID, map[string]taskverification.Verdict{
		"v1": taskverification.VerdictPass,
		"v2": taskverification.VerdictPass,
	})
	payload := &event.TaskVerificationConsensusPayload{
		RoundID: "round-q2", TaskID: taskID, FinalVerdict: "pass",
		WorkerID: "worker-1", PosterID: "poster-1",
	}

	result, _ := settler.Settle(context.Background(), payload, round)
	if result.ValidatorPayouts["v1"] <= result.ValidatorPayouts["v2"] {
		t.Errorf("high-Q v1 (%d) should get more than low-Q v2 (%d)",
			result.ValidatorPayouts["v1"], result.ValidatorPayouts["v2"])
	}
	if result.TotalDistributed != budget {
		t.Errorf("total = %d; want %d", result.TotalDistributed, budget)
	}
}

func TestSettle_QWeighted_AllZero_FallsBackToEvenSplit(t *testing.T) {
	budget := uint64(10000)
	qFn := ValidatorQScoreFn(func(_ crypto.AgentID, _, _ string) float64 { return 0 })
	settler, tm := setupQWeightedSettler(t, budget, qFn)
	allTasks := tm.Search(tasks.TaskStatusSubmitted, "", 0)
	taskID := allTasks[0].ID

	round := makeRoundWithVotes(taskID, map[string]taskverification.Verdict{
		"v1": taskverification.VerdictPass,
		"v2": taskverification.VerdictPass,
	})
	payload := &event.TaskVerificationConsensusPayload{
		RoundID: "round-q3", TaskID: taskID, FinalVerdict: "pass",
		WorkerID: "worker-1", PosterID: "poster-1",
	}

	result, _ := settler.Settle(context.Background(), payload, round)
	if len(result.ValidatorPayouts) != 2 {
		t.Fatalf("expected 2 payouts; got %d", len(result.ValidatorPayouts))
	}
	if result.TotalDistributed != budget {
		t.Errorf("total = %d; want %d", result.TotalDistributed, budget)
	}
}

// stubSettlerScanner satisfies DAGScanner for catch-up tests.
type stubSettlerScanner struct{ events []*event.Event }

func (s *stubSettlerScanner) All() []*event.Event { return s.events }

// TestVerificationConsensusSettler_EscrowCatchUp_UsesRegisterEscrow exercises
// the C1-2 migration: when the settler runs on a peer node where the escrow
// is not yet locked, it uses LookupEscrowLockTransfer + RegisterEscrow rather
// than calling the legacy Hold path. The canonical Transfer has already moved
// funds; the catch-up path records metadata only.
func TestVerificationConsensusSettler_EscrowCatchUp_UsesRegisterEscrow(t *testing.T) {
	budget := uint64(10000)
	tl := ledger.NewTransferLedger()
	tm := tasks.NewTaskManager()
	em := escrow.New(tl)

	// Seed poster with budget and DIRECTLY move into the escrow bucket,
	// simulating what the canonical Transfer has already done on the producer.
	_ = tl.FundAgent("poster-1", budget*10)

	_, _ = tm.PostTask("poster-1", "catch-up", "desc", "research", budget)
	taskID := tm.Search(tasks.TaskStatusOpen, "", 0)[0].ID
	_ = tm.ClaimTask(taskID, "worker-1")
	_ = tm.SubmitResult(taskID, "worker-1", "sha256:test", "note", "")

	// Move funds into bucket (simulates canonical Transfer's side effect on
	// this node, but the escrow manager hasn't registered metadata yet).
	_ = tl.TransferFromBucket("poster-1", crypto.AgentID("escrow:"+taskID), budget)

	// Build the canonical escrow-lock Transfer event and seed the DAG scanner.
	evt, err := event.New(event.EventTypeTransfer, nil,
		event.TransferPayload{
			Version:   1,
			FromAgent: "poster-1",
			ToAgent:   "escrow:" + taskID,
			Amount:    budget,
			Currency:  "AET",
			Reason:    "escrow-lock",
			TaskID:    taskID,
		},
		"poster-1", nil, 0)
	if err != nil {
		t.Fatalf("construct transfer: %v", err)
	}
	scanner := &stubSettlerScanner{events: []*event.Event{evt}}

	// The escrow manager needs the DAGReader so RegisterEscrow can validate.
	em.SetDAGReader(&applicatorStub{events: map[event.EventID]*event.Event{evt.ID: evt}})

	calc := NewGenerationLedgerCalculator(nil, func(_ event.EventID) float64 { return 1.0 })
	settler := NewVerificationConsensusSettler(tm, tl, em, scanner, calc, "genesis:treasury", nil)

	posterBalBefore, _ := tl.Balance(crypto.AgentID("poster-1"))

	round := makeRoundWithVotes(taskID, map[string]taskverification.Verdict{
		"validator-1": taskverification.VerdictPass,
		"validator-2": taskverification.VerdictPass,
	})
	payload := &event.TaskVerificationConsensusPayload{
		RoundID:      "round-catchup",
		TaskID:       taskID,
		FinalVerdict: "pass",
		FinalScoreBP: 9000,
		WorkerID:     "worker-1",
		PosterID:     "poster-1",
	}
	result, err := settler.Settle(context.Background(), payload, round)
	if err != nil {
		t.Fatalf("Settle: %v", err)
	}
	if !result.Applied {
		t.Fatal("should be applied")
	}

	// The catch-up must NOT have debited poster a second time.
	posterBalAfter, _ := tl.Balance(crypto.AgentID("poster-1"))
	if posterBalAfter != posterBalBefore {
		t.Errorf("poster balance changed during catch-up: before=%d after=%d", posterBalBefore, posterBalAfter)
	}

	// Settlement completed successfully — the escrow entry was registered
	// via catch-up (RegisterEscrow) and then released (ReleaseSettlement
	// deletes the entry on completion). Verify via the result.
	if !result.Applied {
		t.Error("settlement should have been applied")
	}
	if result.WorkerPayout == 0 {
		t.Error("worker payout should be non-zero on accept")
	}
}

// applicatorStub satisfies escrow.DAGReader for the catch-up test.
type applicatorStub struct{ events map[event.EventID]*event.Event }

func (s *applicatorStub) Get(id event.EventID) (*event.Event, error) {
	if e, ok := s.events[id]; ok {
		return e, nil
	}
	return nil, context.Canceled // non-nil error; sentinel unused
}
