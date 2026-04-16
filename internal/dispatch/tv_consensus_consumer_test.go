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
	"github.com/Aethernet-network/aethernet/internal/tasks"
	"github.com/Aethernet-network/aethernet/internal/taskverification"
)

type tvStubDAG struct{}

func (d *tvStubDAG) All() []*event.Event { return nil }

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
		tm, tl, em, &tvStubDAG{}, nil, "genesis:treasury", nil)
	consumer := dispatch.NewTVConsensusConsumer(settler, store, tm)
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
			tm, tl, em, &tvStubDAG{}, nil, "genesis:treasury", nil)
		c := dispatch.NewTVConsensusConsumer(settler, store, tm)
		return c, func() { db.Close() }
	})
}
