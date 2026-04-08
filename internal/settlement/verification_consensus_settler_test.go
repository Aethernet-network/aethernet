package settlement

import (
	"context"
	"testing"

	"github.com/Aethernet-network/aethernet/internal/crypto"
	"github.com/Aethernet-network/aethernet/internal/escrow"
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

	// Hold escrow.
	_ = em.Hold(taskID, "poster-1", budget)

	calc := NewGenerationLedgerCalculator(nil, func(_ event.EventID) float64 { return 1.0 })
	settler := NewVerificationConsensusSettler(tm, tl, em, calc, "genesis:treasury")

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
