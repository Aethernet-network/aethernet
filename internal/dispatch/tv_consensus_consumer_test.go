package dispatch_test

import (
	"context"
	"sync"
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

type tvStubDAG struct{}

func (d *tvStubDAG) All() []*event.Event { return nil }

// tvStubAnchorReader satisfies derivation.AnchorReader for tests that
// do NOT exercise canonical-DAG-derived inputs (no real EpochBoundary
// events, no real activation event, no gen-ledger ancestors).
//
// CountAncestorsByType returns 0 (genesis epoch — no boundaries
// committed; rounds settle in epoch 0 with stub W/quality).
// IsAncestor returns false (no canonical ancestry; matches the
// empty-ReputationActivationEventID branch of the V-1 isActivated check).
// Get returns a synthetic empty-CausalRefs event for any ID so
// ReadAtAnchor's BFS terminates on the seed without traversing further.
type tvStubAnchorReader struct{}

func (tvStubAnchorReader) IsAncestor(_, _ event.EventID) (bool, error) { return false, nil }
func (tvStubAnchorReader) Get(id event.EventID) (*event.Event, error) {
	return &event.Event{ID: id, CausalRefs: nil}, nil
}
func (tvStubAnchorReader) CountAncestorsByType(_ event.EventID, _ event.EventType) (uint64, error) {
	return 0, nil
}

func newTVRoundStore(t *testing.T) taskverification.Store {
	t.Helper()
	opts := badger.DefaultOptions("").WithInMemory(true).WithLogger(nil)
	db, err := badger.Open(opts)
	if err != nil {
		t.Fatalf("open badger: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return taskverification.NewBadgerStore(db)
}

func newTestTVConsumer(t *testing.T) (*dispatch.TVConsensusConsumer, *tasks.TaskManager, *ledger.TransferLedger, *escrow.Escrow, taskverification.Store) {
	t.Helper()
	tl := ledger.NewTransferLedger()
	_ = tl.FundAgent("poster", 100_000)
	tm := tasks.NewTaskManager()
	em := escrow.New(tl)
	store := newTVRoundStore(t)

	settler := settlement.NewVerificationConsensusSettler(
		tm, tl, em, &tvStubDAG{}, nil, "genesis:treasury", nil, true)
	settler.SetDAGReader(tvStubAnchorReader{})
	consumer := dispatch.NewTVConsensusConsumer(settler, store, tm, em)
	return consumer, tm, tl, em, store
}

func setupTask(t *testing.T, tm *tasks.TaskManager, tl *ledger.TransferLedger, em *escrow.Escrow, budget uint64) (string, *taskverification.TaskVerificationRound) {
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
		RoundID:  "round-1",
		TaskID:   task.ID,
		WorkerID: "worker",
		PosterID: "poster",
		Category: "research",
		// F5 5B: DeriveSettlement requires the round to be terminal at
		// settler invocation. The recognition fabric's
		// TaskVerificationConsensusConsumer transitions the round to
		// terminal BEFORE the dispatcher's TVConsensusConsumer.Apply
		// fires in production; tests construct the round in the
		// post-transition state to mirror that ordering.
		State:        taskverification.RoundStateFinalizedAccept,
		FinalVerdict: taskverification.VerdictPass,
		// CanonicalSealContext + EpochAtFinalization populated by the
		// finalizing consumer in production; tests use empty/zero values
		// (the isActivated helper short-circuits to (false, nil) on empty
		// activation EventID, so V-1 check never reaches DAG).
		Votes: []taskverification.TaskVerificationVoteRecord{
			{ValidatorID: "validator-1", Verdict: taskverification.VerdictPass, ScoreBP: 7000, AnalyzerFamily: "heuristic", Stake: 100},
		},
	}
	return task.ID, round
}

func makeConsensusEvent(t *testing.T, taskID, roundID, verdict string) *event.Event {
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

func TestTVConsensusConsumer_Name(t *testing.T) {
	c, _, _, _, _ := newTestTVConsumer(t)
	if c.Name() != "tv_consensus_settlement" {
		t.Errorf("Name: got %q", c.Name())
	}
}

func TestTVConsensusConsumer_Type(t *testing.T) {
	c, _, _, _, _ := newTestTVConsumer(t)
	if c.Type() != dispatch.TypeA {
		t.Errorf("Type: got %v", c.Type())
	}
}

func TestTVConsensusConsumer_Interested(t *testing.T) {
	c, _, _, _, _ := newTestTVConsumer(t)
	ev := makeConsensusEvent(t, "task-1", "round-1", "pass")
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

func TestTVConsensusConsumer_Prerequisites(t *testing.T) {
	c, _, _, _, _ := newTestTVConsumer(t)
	ev := makeConsensusEvent(t, "task-1", "round-1", "pass")
	prereqs := c.Prerequisites(ev)
	if prereqs != nil {
		t.Errorf("Prerequisites should be nil; got %v", prereqs)
	}
}

func TestTVConsensusConsumer_PrerequisiteSchemaVersion(t *testing.T) {
	c, _, _, _, _ := newTestTVConsumer(t)
	if c.PrerequisiteSchemaVersion() != 1 {
		t.Errorf("PrerequisiteSchemaVersion: got %d", c.PrerequisiteSchemaVersion())
	}
}

func TestTVConsensusConsumer_Apply_AcceptPath(t *testing.T) {
	c, tm, tl, em, store := newTestTVConsumer(t)
	budget := uint64(10000)
	taskID, round := setupTask(t, tm, tl, em, budget)
	_ = store.SaveRound(context.Background(), round)

	ev := makeConsensusEvent(t, taskID, "round-1", "pass")
	if err := c.Apply(context.Background(), ev); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	workerBal, _ := tl.Balance(crypto.AgentID("worker"))
	if workerBal != budget*7300/10000 {
		t.Errorf("worker balance: got %d want %d", workerBal, budget*7300/10000)
	}

	task, _ := tm.Get(taskID)
	if task.Status != tasks.TaskStatusCompleted {
		t.Errorf("task status: got %s want completed", task.Status)
	}

	// Conservation check: sum of all payouts equals budget.
	treasuryBal, _ := tl.Balance(crypto.AgentID("genesis:treasury"))
	v1Bal, _ := tl.Balance(crypto.AgentID("validator-1"))
	total := workerBal + v1Bal + treasuryBal
	if total != budget {
		t.Errorf("total distributed %d != budget %d (worker=%d v1=%d treasury=%d)",
			total, budget, workerBal, v1Bal, treasuryBal)
	}
}

func TestTVConsensusConsumer_Apply_Idempotent(t *testing.T) {
	c, tm, tl, em, store := newTestTVConsumer(t)
	budget := uint64(10000)
	taskID, round := setupTask(t, tm, tl, em, budget)
	_ = store.SaveRound(context.Background(), round)

	ev := makeConsensusEvent(t, taskID, "round-1", "pass")
	_ = c.Apply(context.Background(), ev)

	workerBal1, _ := tl.Balance(crypto.AgentID("worker"))

	if err := c.Apply(context.Background(), ev); err != nil {
		t.Fatalf("second Apply: %v", err)
	}

	workerBal2, _ := tl.Balance(crypto.AgentID("worker"))
	if workerBal1 != workerBal2 {
		t.Errorf("balance changed on second Apply: %d → %d", workerBal1, workerBal2)
	}
}

// TestTVConsensusConsumer_Apply_SecondEventForSameTask_NoOp exercises the
// post-§10 task-level idempotency guard. A first TaskVerificationConsensus
// event fully settles a task; a second event with a distinct content hash
// (different RoundID field) targeting the same task must be a no-op — no
// additional transfers, no ledger mutation.
//
// Regression guard for the treasury-spread failure mode observed on the
// live testnet, where multiple finalizing validators each emitted their own
// consensus event with distinct event hashes and each drove an interleaved
// partial drain of the same escrow bucket.
func TestTVConsensusConsumer_Apply_SecondEventForSameTask_NoOp(t *testing.T) {
	c, tm, tl, em, store := newTestTVConsumer(t)
	budget := uint64(10000)
	taskID, round := setupTask(t, tm, tl, em, budget)
	_ = store.SaveRound(context.Background(), round)

	ev1 := makeConsensusEvent(t, taskID, "round-1", "pass")
	if err := c.Apply(context.Background(), ev1); err != nil {
		t.Fatalf("first Apply: %v", err)
	}
	workerBal1, _ := tl.Balance(crypto.AgentID("worker"))
	treasuryBal1, _ := tl.Balance(crypto.AgentID("genesis:treasury"))
	v1Bal1, _ := tl.Balance(crypto.AgentID("validator-1"))

	ev2 := makeConsensusEvent(t, taskID, "round-alternate", "pass")
	if err := c.Apply(context.Background(), ev2); err != nil {
		t.Fatalf("second Apply with distinct event: %v", err)
	}

	workerBal2, _ := tl.Balance(crypto.AgentID("worker"))
	treasuryBal2, _ := tl.Balance(crypto.AgentID("genesis:treasury"))
	v1Bal2, _ := tl.Balance(crypto.AgentID("validator-1"))
	if workerBal1 != workerBal2 || treasuryBal1 != treasuryBal2 || v1Bal1 != v1Bal2 {
		t.Errorf("balances mutated on second distinct event: worker %d→%d treasury %d→%d v1 %d→%d",
			workerBal1, workerBal2, treasuryBal1, treasuryBal2, v1Bal1, v1Bal2)
	}
}

// TestTVConsensusConsumer_Apply_EscrowAlreadyReleased_NoOp covers the state
// where a prior settlement fully completed and deleted the escrow entry.
// A late-arriving consensus event for the same task must return nil and
// not re-drive settlement (including not invoking the peer-node catch-up
// path, which would re-register escrow metadata and re-enter ReleaseSettlement).
func TestTVConsensusConsumer_Apply_EscrowAlreadyReleased_NoOp(t *testing.T) {
	c, tm, tl, em, store := newTestTVConsumer(t)
	budget := uint64(10000)
	taskID, round := setupTask(t, tm, tl, em, budget)
	_ = store.SaveRound(context.Background(), round)

	ev1 := makeConsensusEvent(t, taskID, "round-1", "pass")
	if err := c.Apply(context.Background(), ev1); err != nil {
		t.Fatalf("first Apply: %v", err)
	}
	if em.IsLocked(taskID) {
		t.Fatalf("escrow should be released after successful settlement")
	}

	workerBal1, _ := tl.Balance(crypto.AgentID("worker"))
	treasuryBal1, _ := tl.Balance(crypto.AgentID("genesis:treasury"))

	ev2 := makeConsensusEvent(t, taskID, "round-2", "pass")
	if err := c.Apply(context.Background(), ev2); err != nil {
		t.Fatalf("Apply with deleted escrow: %v", err)
	}

	workerBal2, _ := tl.Balance(crypto.AgentID("worker"))
	treasuryBal2, _ := tl.Balance(crypto.AgentID("genesis:treasury"))
	if workerBal1 != workerBal2 || treasuryBal1 != treasuryBal2 {
		t.Errorf("balances mutated on Apply with deleted escrow: worker %d→%d treasury %d→%d",
			workerBal1, workerBal2, treasuryBal1, treasuryBal2)
	}
}

// TestTVConsensusConsumer_Apply_ConcurrentSameTask_Serialized exercises the
// per-task mutex added after the v2 §10 rerun found that the commit-10
// task-level pre-check closed sequential re-entrance but not concurrent
// re-entrance. Two goroutines call Apply simultaneously with distinct
// TaskVerificationConsensus events targeting the same task. Without the
// per-task mutex, both would pass the task-terminal / HasSettlementStarted
// pre-checks (no paid flags set, task not terminal) and race into
// ReleaseSettlement with different recipient sets, producing interleaved
// partial drains and divergent escrow end-state. With the mutex, the
// second goroutine blocks until the first finishes settlement and terminal-
// status transition, then observes AlreadyApplied and exits.
//
// FINDING #9 fix (F4B step 0.2): the original test pre-saved only round-1
// and used round-alt for ev2. Whichever goroutine won the per-task mutex
// first determined whether ev2's Apply hit the LoadRound path (succeeds
// when ev1 won and set the task terminal — short-circuits in pre-check 1)
// or hit it before pre-check 1 was satisfied (fails with "round not
// found"). The test was racy on goroutine scheduling, not on the SUT.
// Fix: pre-save round-alt as a sibling round so BOTH goroutines have a
// loadable round if they reach pre-check 2. The test then asserts what
// it's actually trying to assert — that the per-task mutex serializes
// concurrent Apply calls and exactly one settlement lands.
func TestTVConsensusConsumer_Apply_ConcurrentSameTask_Serialized(t *testing.T) {
	c, tm, tl, em, store := newTestTVConsumer(t)
	budget := uint64(10000)
	taskID, round := setupTask(t, tm, tl, em, budget)
	_ = store.SaveRound(context.Background(), round)

	// Pre-save the alternate round so whichever goroutine wins the per-
	// task mutex first can load its round if it reaches pre-check 2.
	// Before this fix, the test was flaky: if the second goroutine won
	// the mutex it would call LoadRound("round-alt") before the first
	// goroutine had transitioned task status to terminal, and LoadRound
	// would error out before the pre-check could fire. Part B of the
	// canonical-distribution-integer-migration workstream surfaced the
	// flake by changing integer-path timing enough that ev2 wins the
	// race a non-trivial fraction of the time; F4B step 0.2 (FINDING #9
	// fix) then formalized the repro as a test-design bug rather than a
	// SUT race.
	roundAlt := *round
	roundAlt.RoundID = "round-alt"
	_ = store.SaveRound(context.Background(), &roundAlt)

	ev1 := makeConsensusEvent(t, taskID, "round-1", "pass")
	ev2 := makeConsensusEvent(t, taskID, "round-alt", "pass")

	var wg sync.WaitGroup
	var errs [2]error
	wg.Add(2)
	go func() { defer wg.Done(); errs[0] = c.Apply(context.Background(), ev1) }()
	go func() { defer wg.Done(); errs[1] = c.Apply(context.Background(), ev2) }()
	wg.Wait()

	for i, e := range errs {
		if e != nil {
			t.Errorf("concurrent Apply[%d]: %v", i, e)
		}
	}

	// Exactly one settlement should have landed: worker paid budget*7300/10000,
	// treasury paid the remainder after worker + validator pool. Any divergence
	// would manifest as excess or missing balance on worker, treasury, or a
	// non-empty escrow bucket.
	workerBal, _ := tl.Balance(crypto.AgentID("worker"))
	if workerBal != budget*7300/10000 {
		t.Errorf("worker balance: got %d want %d", workerBal, budget*7300/10000)
	}
	treasuryBal, _ := tl.Balance(crypto.AgentID("genesis:treasury"))
	v1Bal, _ := tl.Balance(crypto.AgentID("validator-1"))
	if total := workerBal + v1Bal + treasuryBal; total != budget {
		t.Errorf("total distributed %d != budget %d (worker=%d v1=%d treasury=%d)",
			total, budget, workerBal, v1Bal, treasuryBal)
	}
	bucketBal, _ := tl.Balance(crypto.AgentID("escrow:" + taskID))
	if bucketBal != 0 {
		t.Errorf("escrow bucket should be empty after settlement; got %d stuck", bucketBal)
	}
	if em.IsLocked(taskID) {
		t.Errorf("escrow entry should be deleted after successful settlement")
	}
	task, _ := tm.Get(taskID)
	if task.Status != tasks.TaskStatusCompleted {
		t.Errorf("task status: got %s want completed", task.Status)
	}
}

// TestTVConsensusConsumer_Apply_ConcurrentDifferentTasks_Parallel verifies
// that the per-task mutex does not serialize across different tasks. Two
// goroutines applying events for two distinct tasks must both complete
// settlement — the locks are independent.
func TestTVConsensusConsumer_Apply_ConcurrentDifferentTasks_Parallel(t *testing.T) {
	c, tm, tl, em, store := newTestTVConsumer(t)
	budgetA, budgetB := uint64(10000), uint64(20000)

	// Fund a second poster so both tasks can be escrowed independently.
	_ = tl.FundAgent("poster", 100_000)

	taskA, roundA := setupTask(t, tm, tl, em, budgetA)
	_ = store.SaveRound(context.Background(), roundA)

	// Second task uses a different task id (PostTask generates unique ids)
	// but we need to reuse helpers that call PostTask on the same manager.
	taskB, rawRoundB := setupTask(t, tm, tl, em, budgetB)
	// setupTask uses RoundID "round-1" for every task — rewrite to avoid
	// cross-task round collision in the store.
	roundB := *rawRoundB
	roundB.RoundID = "round-B"
	_ = store.SaveRound(context.Background(), &roundB)

	evA := makeConsensusEvent(t, taskA, "round-1", "pass")
	evB := makeConsensusEvent(t, taskB, "round-B", "pass")

	var wg sync.WaitGroup
	var errs [2]error
	wg.Add(2)
	go func() { defer wg.Done(); errs[0] = c.Apply(context.Background(), evA) }()
	go func() { defer wg.Done(); errs[1] = c.Apply(context.Background(), evB) }()
	wg.Wait()

	for i, e := range errs {
		if e != nil {
			t.Errorf("Apply[%d]: %v", i, e)
		}
	}

	// Both tasks should be completed; both escrow buckets empty.
	for _, tid := range []string{taskA, taskB} {
		if em.IsLocked(tid) {
			t.Errorf("escrow for task %s should be released", tid)
		}
		if bal, _ := tl.Balance(crypto.AgentID("escrow:" + tid)); bal != 0 {
			t.Errorf("escrow bucket %s not empty: %d stuck", tid, bal)
		}
		task, _ := tm.Get(tid)
		if task.Status != tasks.TaskStatusCompleted {
			t.Errorf("task %s status: got %s", tid, task.Status)
		}
	}
}

// TestTVConsensusConsumer_Apply_MidSettlement_NoOp covers the critical case
// where a prior event's settlement partially applied (some paid flags set,
// some not) because ReleaseSettlement failed mid-way. The HasSettlementStarted
// pre-check must short-circuit a subsequent event's Apply to prevent the
// settler from being re-entered with a different recipient set and causing
// a second interleaved partial drain of the same bucket.
func TestTVConsensusConsumer_Apply_MidSettlement_NoOp(t *testing.T) {
	c, tm, tl, em, store := newTestTVConsumer(t)
	budget := uint64(10000)
	taskID, round := setupTask(t, tm, tl, em, budget)
	_ = store.SaveRound(context.Background(), round)

	// Drive a partial settlement via the F5 5B canonical applicator:
	// two records sized so the second fails (insufficient bucket
	// balance). Worker record (5000 ≤ 10000) succeeds and sets
	// WorkerPaid=true; the treasury record (20000 > 5000 remaining)
	// fails. Result: entry persists with WorkerPaid=true and no other
	// flags — equivalent state to the legacy ReleaseSettlement
	// partial-failure case (Plan v3 §0 decision 9 reframe; the
	// behavioral invariant tested — HasSettlementStarted pre-check
	// short-circuits subsequent Apply — is preserved).
	partialRecords := []derivation.PayoutRecord{
		{
			CanonicalID:       "test-mid-settlement-worker",
			DerivationVersion: derivation.DerivationVersion,
			SettlementKey:     derivation.SettlementKey{TaskID: taskID, RoundID: "round-1", FundingReference: "funding-1"},
			Recipient:         derivation.Recipient{ID: "worker", Role: derivation.RoleWorker},
			Amount:            derivation.Amount{Value: 5000, Currency: derivation.CurrencyAET},
			Purpose:           derivation.Purpose{Tag: derivation.TagWorkerPayout},
		},
		{
			CanonicalID:       "test-mid-settlement-treasury-fails",
			DerivationVersion: derivation.DerivationVersion,
			SettlementKey:     derivation.SettlementKey{TaskID: taskID, RoundID: "round-1", FundingReference: "funding-1"},
			Recipient:         derivation.Recipient{ID: "genesis:treasury", Role: derivation.RoleTreasury},
			Amount:            derivation.Amount{Value: 20000, Currency: derivation.CurrencyAET}, // bigger than remaining bucket; fails
			Purpose:           derivation.Purpose{Tag: derivation.TagTreasuryRemainder},
		},
	}
	if err := em.ApplySettlementRecords(taskID, partialRecords); err == nil {
		t.Fatalf("ApplySettlementRecords should have failed on the treasury leg")
	}
	if !em.IsLocked(taskID) {
		t.Fatalf("entry should still be locked after partial failure")
	}
	if !em.HasSettlementStarted(taskID) {
		t.Fatalf("HasSettlementStarted should be true after partial payout")
	}

	workerBal1, _ := tl.Balance(crypto.AgentID("worker"))
	v1Bal1, _ := tl.Balance(crypto.AgentID("validator-1"))
	treasuryBal1, _ := tl.Balance(crypto.AgentID("genesis:treasury"))

	ev := makeConsensusEvent(t, taskID, "round-1", "pass")
	if err := c.Apply(context.Background(), ev); err != nil {
		t.Fatalf("Apply mid-settlement: %v", err)
	}

	workerBal2, _ := tl.Balance(crypto.AgentID("worker"))
	v1Bal2, _ := tl.Balance(crypto.AgentID("validator-1"))
	treasuryBal2, _ := tl.Balance(crypto.AgentID("genesis:treasury"))
	if workerBal1 != workerBal2 || v1Bal1 != v1Bal2 || treasuryBal1 != treasuryBal2 {
		t.Errorf("mid-settlement Apply perturbed balances: worker %d→%d v1 %d→%d treasury %d→%d",
			workerBal1, workerBal2, v1Bal1, v1Bal2, treasuryBal1, treasuryBal2)
	}
}

func TestTVConsensusConsumer_RecoveryProbe_Completed(t *testing.T) {
	c, tm, tl, em, store := newTestTVConsumer(t)
	budget := uint64(10000)
	taskID, round := setupTask(t, tm, tl, em, budget)
	_ = store.SaveRound(context.Background(), round)

	ev := makeConsensusEvent(t, taskID, "round-1", "pass")
	_ = c.Apply(context.Background(), ev)

	status, err := c.RecoveryProbe(context.Background(), ev)
	if err != nil {
		t.Fatalf("RecoveryProbe: %v", err)
	}
	if status != dispatch.RecoveryCompleted {
		t.Errorf("probe: got %v want RecoveryCompleted", status)
	}
}

func TestTVConsensusConsumer_RecoveryProbe_NotStarted(t *testing.T) {
	c, tm, _, _, _ := newTestTVConsumer(t)
	_, _ = tm.PostTask("poster", "test", "desc", "research", 1000)
	allTasks := tm.Search(tasks.TaskStatusOpen, "", 0)
	taskID := allTasks[0].ID

	ev := makeConsensusEvent(t, taskID, "round-1", "pass")
	status, err := c.RecoveryProbe(context.Background(), ev)
	if err != nil {
		t.Fatalf("RecoveryProbe: %v", err)
	}
	if status != dispatch.RecoveryNotStarted {
		t.Errorf("probe: got %v want RecoveryNotStarted", status)
	}
}

func TestTVConsensusConsumer_Conformance(t *testing.T) {
	conformance.RunTypeAConformance(t, func() (dispatch.Consumer, func()) {
		tl := ledger.NewTransferLedger()
		_ = tl.FundAgent("alice", 1_000_000)
		tm := tasks.NewTaskManager()
		em := escrow.New(tl)

		opts := badger.DefaultOptions("").WithInMemory(true).WithLogger(nil)
		db, _ := badger.Open(opts)
		store := taskverification.NewBadgerStore(db)

		settler := settlement.NewVerificationConsensusSettler(
			tm, tl, em, &tvStubDAG{}, nil, "genesis:treasury", nil, true)
		c := dispatch.NewTVConsensusConsumer(settler, store, tm, em)
		return c, func() { db.Close() }
	})
}
