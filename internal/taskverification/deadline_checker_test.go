package taskverification

import (
	"context"
	"testing"
	"time"

	badger "github.com/dgraph-io/badger/v4"

	"github.com/Aethernet-network/aethernet/internal/crypto"
	"github.com/Aethernet-network/aethernet/internal/event"
)

type discardPublisher struct {
	count int
}

func (p *discardPublisher) Publish(_ *event.Event) error {
	p.count++
	return nil
}

func testBadgerStore(t *testing.T) *BadgerStore {
	t.Helper()
	db, err := badger.Open(badger.DefaultOptions("").WithInMemory(true).WithLogger(nil))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return NewBadgerStore(db)
}

func TestDeadlineChecker_FinalizesExpired(t *testing.T) {
	store := testBadgerStore(t)
	kp, _ := crypto.GenerateKeyPair()
	pub := &discardPublisher{}

	r, _ := OpenRound(OpenRoundParams{
		TaskID: "task-dc", SubmissionEventID: "evt-dc",
		WorkerID: "w", PosterID: "p", Category: "research",
		DiversityFloor: 2, AcceptanceThresholdBP: 6000,
		DeadlineSeconds: 60, Now: 1000,
	})
	r.PassWeight = 200
	r.FailWeight = 100
	_ = store.SaveRound(context.Background(), r)

	checker := NewDeadlineChecker(
		store,
		NewFinalizer(DefaultFinalizerConfig()),
		pub, kp, kp.AgentID(),
		func() uint64 { return 1000 },
		time.Hour, // don't tick in test
		60,
		func() int64 { return 1200 }, // past deadline (1060)
	)

	checker.check() // invoke directly

	updated, _ := store.LoadRound(context.Background(), r.RoundID)
	if updated.State != RoundStateDisputed {
		t.Errorf("State = %s; want disputed", updated.State)
	}
	if pub.count == 0 {
		t.Error("expected consensus event to be published")
	}
}

func TestDeadlineChecker_ExtendsConvergent(t *testing.T) {
	store := testBadgerStore(t)
	kp, _ := crypto.GenerateKeyPair()
	pub := &discardPublisher{}

	r, _ := OpenRound(OpenRoundParams{
		TaskID: "task-ext", SubmissionEventID: "evt-ext",
		WorkerID: "w", PosterID: "p", Category: "research",
		DiversityFloor: 2, AcceptanceThresholdBP: 6000,
		DeadlineSeconds: 60, Now: 1000,
	})
	// 600/1000 = 60% pass → convergence plausible (≥50%)
	r.PassWeight = 600
	r.ParticipatingFamilies = map[string]uint64{"llm_semantic": 600}
	_ = store.SaveRound(context.Background(), r)

	checker := NewDeadlineChecker(
		store,
		NewFinalizer(DefaultFinalizerConfig()),
		pub, kp, kp.AgentID(),
		func() uint64 { return 1000 },
		time.Hour, 60,
		func() int64 { return 1100 }, // past original deadline (1060)
	)

	checker.check()

	updated, _ := store.LoadRound(context.Background(), r.RoundID)
	if updated.State != RoundStateOpen {
		t.Errorf("State = %s; want open (extended)", updated.State)
	}
	if updated.ExtendedUntilUnix == 0 {
		t.Error("ExtendedUntilUnix should be set")
	}
	if pub.count != 0 {
		t.Error("should NOT publish consensus event for extension")
	}
}

func TestDeadlineChecker_DoesNotExtendTwice(t *testing.T) {
	store := testBadgerStore(t)
	kp, _ := crypto.GenerateKeyPair()
	pub := &discardPublisher{}

	r, _ := OpenRound(OpenRoundParams{
		TaskID: "task-ext2", SubmissionEventID: "evt-ext2",
		WorkerID: "w", PosterID: "p", Category: "research",
		DiversityFloor: 2, AcceptanceThresholdBP: 6000,
		DeadlineSeconds: 60, Now: 1000,
	})
	r.PassWeight = 600
	r.ParticipatingFamilies = map[string]uint64{"llm_semantic": 600}
	r.ExtendedUntilUnix = 1120 // already extended once
	_ = store.SaveRound(context.Background(), r)

	checker := NewDeadlineChecker(
		store,
		NewFinalizer(DefaultFinalizerConfig()),
		pub, kp, kp.AgentID(),
		func() uint64 { return 1000 },
		time.Hour, 60,
		func() int64 { return 1200 }, // past extended deadline (1120)
	)

	checker.check()

	updated, _ := store.LoadRound(context.Background(), r.RoundID)
	if updated.State != RoundStateDisputed {
		t.Errorf("State = %s; want disputed (no second extension)", updated.State)
	}
}

func TestDeadlineChecker_IgnoresNotExpired(t *testing.T) {
	store := testBadgerStore(t)
	kp, _ := crypto.GenerateKeyPair()
	pub := &discardPublisher{}

	r, _ := OpenRound(OpenRoundParams{
		TaskID: "task-ok", SubmissionEventID: "evt-ok",
		WorkerID: "w", PosterID: "p", Category: "research",
		DiversityFloor: 2, AcceptanceThresholdBP: 6000,
		DeadlineSeconds: 60, Now: 1000,
	})
	_ = store.SaveRound(context.Background(), r)

	checker := NewDeadlineChecker(
		store,
		NewFinalizer(DefaultFinalizerConfig()),
		pub, kp, kp.AgentID(),
		func() uint64 { return 1000 },
		time.Hour, 60,
		func() int64 { return 1030 }, // well before deadline (1060)
	)

	checker.check()

	updated, _ := store.LoadRound(context.Background(), r.RoundID)
	if updated.State != RoundStateOpen {
		t.Errorf("State = %s; want open (not expired)", updated.State)
	}
	if pub.count != 0 {
		t.Error("should not publish for non-expired round")
	}
}

func TestDeadlineChecker_StartStop(t *testing.T) {
	store := testBadgerStore(t)
	kp, _ := crypto.GenerateKeyPair()

	checker := NewDeadlineChecker(
		store,
		NewFinalizer(DefaultFinalizerConfig()),
		&discardPublisher{}, kp, kp.AgentID(),
		func() uint64 { return 1000 },
		50*time.Millisecond, 60,
		func() int64 { return time.Now().Unix() },
	)

	checker.Start()
	time.Sleep(100 * time.Millisecond)
	checker.Stop() // should not hang or panic
}
