package validatorlifecycle

// Tests that verify the sync handler integration path: lifecycle events
// extracted from DAG events update the Reducer and produce correct snapshots.
// These test the ExtractLifecycleEvent → Apply → Snapshot pipeline that the
// sync handler uses at runtime.

import (
	"errors"
	"testing"

	"github.com/Aethernet-network/aethernet/internal/crypto"
	"github.com/Aethernet-network/aethernet/internal/event"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// createLifecycleDAGEvent creates a standard event.Event with a lifecycle
// payload, signed by a test keypair.
func createLifecycleDAGEvent(t *testing.T, evType event.EventType, payload interface{}, causalTS uint64) *event.Event {
	t.Helper()
	kp, _ := crypto.GenerateKeyPair()
	agentID := string(kp.AgentID())
	ev, err := event.New(evType, nil, payload, agentID, nil, 0)
	if err != nil {
		t.Fatalf("create event: %v", err)
	}
	// Override causal timestamp for deterministic testing.
	ev.CausalTimestamp = causalTS
	return ev
}

// ---------------------------------------------------------------------------
// ValidatorJoin via sync updates snapshot
// ---------------------------------------------------------------------------

func TestSync_JoinUpdatesSnapshot(t *testing.T) {
	// Start with genesis manifest.
	m := DefaultTestnetManifest()
	r, _ := SeedReducerFromManifest(m)
	preJoinSnap := r.Snapshot()

	// Simulate a ValidatorJoin DAG event arriving via sync.
	joinPayload := ValidatorJoinPayload{
		Version:              1,
		ValidatorID:          "new-joiner",
		OperatorAgentID:      "key-joiner",
		ConsensusPublicKey:   "key-joiner",
		KeyEpoch:             1,
		BondedStake:          MinBondedStake,
		EffectiveFromVersion: 99, // payload version is informational; Reducer uses its own
	}
	ev := createLifecycleDAGEvent(t, event.EventTypeValidatorJoin, joinPayload, 100)

	// Extract and apply.
	lcEvents, err := ExtractLifecycleEvent(ev)
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	for _, lc := range lcEvents {
		if err := r.Apply(lc); err != nil {
			t.Fatalf("apply: %v", err)
		}
	}

	postJoinSnap := r.Snapshot()

	// Pre-join snapshot should NOT include the new joiner.
	_, eligible := preJoinSnap.VoteWeightByKey("key-joiner")
	if eligible {
		t.Fatal("new joiner should NOT be in pre-join snapshot")
	}

	// Post-join snapshot should include the seat (as PendingJoin, not participating).
	seat, ok := postJoinSnap.Seats["new-joiner"]
	if !ok {
		t.Fatal("new joiner should be in post-join snapshot")
	}
	if seat.Status != SeatPendingJoin {
		t.Fatalf("expected PendingJoin, got %s", seat.Status)
	}

	// Version should have incremented.
	if postJoinSnap.Version <= preJoinSnap.Version {
		t.Fatal("version should have incremented after join")
	}
}

// ---------------------------------------------------------------------------
// ValidatorExit via sync removes from future snapshots
// ---------------------------------------------------------------------------

func TestSync_ExitRemovesFromSnapshot(t *testing.T) {
	m := &GenesisManifest{
		Entries: []GenesisManifestEntry{
			{ValidatorID: "v-exit", OperatorAgentID: "k-exit", ConsensusPublicKey: "k-exit",
				KeyEpoch: 1, BondedStake: 100_000, InitialStatus: SeatActive},
		},
	}
	r, _ := SeedReducerFromManifest(m)

	// Take snapshot while active.
	activeSnap := r.Snapshot()
	_, eligible := activeSnap.VoteWeightByKey("k-exit")
	if !eligible {
		t.Fatal("should be eligible before exit")
	}

	// Begin cooldown via exit event.
	exitBeginPayload := ValidatorExitPayload{
		Version:              1,
		ValidatorID:          "v-exit",
		Phase:                ExitPhaseBeginCooldown,
		CooldownDuration:     5,
		EffectiveFromVersion: 99,
	}
	ev1 := createLifecycleDAGEvent(t, event.EventTypeValidatorExit, exitBeginPayload, 10)
	lcEvents, _ := ExtractLifecycleEvent(ev1)
	for _, lc := range lcEvents {
		_ = r.Apply(lc)
	}

	// Complete exit.
	exitCompletePayload := ValidatorExitPayload{
		Version:              1,
		ValidatorID:          "v-exit",
		Phase:                ExitPhaseCompleteExit,
		EffectiveFromVersion: 99,
	}
	ev2 := createLifecycleDAGEvent(t, event.EventTypeValidatorExit, exitCompletePayload, 20)
	lcEvents, _ = ExtractLifecycleEvent(ev2)
	for _, lc := range lcEvents {
		_ = r.Apply(lc)
	}

	exitedSnap := r.Snapshot()
	_, eligible = exitedSnap.VoteWeightByKey("k-exit")
	if eligible {
		t.Fatal("exited validator should NOT be eligible")
	}

	// Old snapshot still shows as eligible.
	_, eligible = activeSnap.VoteWeightByKey("k-exit")
	if !eligible {
		t.Fatal("exited validator should still be eligible in pre-exit snapshot")
	}
}

// ---------------------------------------------------------------------------
// ValidatorSlashApplied via sync reduces stake
// ---------------------------------------------------------------------------

func TestSync_SlashReducesStake(t *testing.T) {
	m := &GenesisManifest{
		Entries: []GenesisManifestEntry{
			{ValidatorID: "v-slash", OperatorAgentID: "k-slash", ConsensusPublicKey: "k-slash",
				KeyEpoch: 1, BondedStake: 100_000, InitialStatus: SeatActive},
		},
	}
	r, _ := SeedReducerFromManifest(m)

	slashPayload := ValidatorSlashAppliedPayload{
		Version:              1,
		ValidatorID:          "v-slash",
		Offense:              "fraud",
		EvidenceRef:          "ev-hash",
		SlashPercentBP:       3000,
		SlashAmount:          30_000,
		RemainingStake:       70_000,
		PermanentExclusion:   false,
		Reason:               "test slash",
		EffectiveFromVersion: 99,
	}
	ev := createLifecycleDAGEvent(t, event.EventTypeValidatorSlashApplied, slashPayload, 10)
	lcEvents, _ := ExtractLifecycleEvent(ev)
	for _, lc := range lcEvents {
		_ = r.Apply(lc)
	}

	seat, _ := r.Seat("v-slash")
	if seat.StakeAmount != 70_000 {
		t.Fatalf("expected stake 70000 after slash, got %d", seat.StakeAmount)
	}
	if seat.Status != SeatSuspended {
		t.Fatalf("expected Suspended after slash, got %s", seat.Status)
	}

	snap := r.Snapshot()
	_, eligible := snap.VoteWeightByKey("k-slash")
	if eligible {
		t.Fatal("slashed validator should NOT be eligible")
	}
}

// ---------------------------------------------------------------------------
// ValidatorKeyRotate via sync updates operator key
// ---------------------------------------------------------------------------

func TestSync_KeyRotateUpdatesKey(t *testing.T) {
	m := &GenesisManifest{
		Entries: []GenesisManifestEntry{
			{ValidatorID: "v-rot", OperatorAgentID: "k-old", ConsensusPublicKey: "k-old",
				KeyEpoch: 1, BondedStake: 100_000, InitialStatus: SeatActive},
		},
	}
	r, _ := SeedReducerFromManifest(m)
	preRotateSnap := r.Snapshot()

	rotatePayload := ValidatorKeyRotatePayload{
		Version:              1,
		ValidatorID:          "v-rot",
		OldConsensusKey:      "k-old",
		NewConsensusKey:      "k-new",
		OldKeyEpoch:          1,
		NewKeyEpoch:          2,
		EffectiveFromVersion: 99,
	}
	ev := createLifecycleDAGEvent(t, event.EventTypeValidatorKeyRotate, rotatePayload, 50)
	lcEvents, _ := ExtractLifecycleEvent(ev)
	for _, lc := range lcEvents {
		_ = r.Apply(lc)
	}

	postRotateSnap := r.Snapshot()

	// Old key valid in pre-rotate snapshot.
	_, eligible := preRotateSnap.VoteWeightByKey("k-old")
	if !eligible {
		t.Fatal("old key should be eligible in pre-rotate snapshot")
	}

	// New key valid in post-rotate snapshot.
	_, eligible = postRotateSnap.VoteWeightByKey("k-new")
	if !eligible {
		t.Fatal("new key should be eligible in post-rotate snapshot")
	}
	_, eligible = postRotateSnap.VoteWeightByKey("k-old")
	if eligible {
		t.Fatal("old key should NOT be eligible in post-rotate snapshot")
	}
}

// ---------------------------------------------------------------------------
// Non-lifecycle events are ignored
// ---------------------------------------------------------------------------

func TestSync_NonLifecycleEventIgnored(t *testing.T) {
	// A Transfer event should return ErrUnsupportedEvent from ExtractLifecycleEvent.
	kp, _ := crypto.GenerateKeyPair()
	agentID := string(kp.AgentID())
	transferPayload := event.TransferPayload{
		FromAgent: "alice", ToAgent: "bob", Amount: 1000, Currency: "AET",
	}
	ev, _ := event.New(event.EventTypeTransfer, nil, transferPayload, agentID, nil, 0)

	_, err := ExtractLifecycleEvent(ev)
	if !errors.Is(err, ErrUnsupportedEvent) {
		t.Fatalf("expected ErrUnsupportedEvent for Transfer, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// Invalid lifecycle event is skipped without crash
// ---------------------------------------------------------------------------

func TestSync_InvalidLifecycleEventSkipped(t *testing.T) {
	// A ValidatorJoin with missing required fields should fail extraction
	// but not panic.
	kp, _ := crypto.GenerateKeyPair()
	agentID := string(kp.AgentID())
	badPayload := ValidatorJoinPayload{
		// Missing ValidatorID and ConsensusPublicKey.
		Version:              1,
		OperatorAgentID:      "op",
		KeyEpoch:             1,
		BondedStake:          100_000,
		EffectiveFromVersion: 1,
	}
	ev, _ := event.New(event.EventTypeValidatorJoin, nil, badPayload, agentID, nil, 0)

	_, err := ExtractLifecycleEvent(ev)
	if err == nil {
		t.Fatal("expected validation error for incomplete payload")
	}
	// The error should be about the missing validator_id, not a panic.
	if !errors.Is(err, ErrPayloadMissingValidatorID) {
		t.Fatalf("expected ErrPayloadMissingValidatorID, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// DAG replay produces same snapshot as live processing
// ---------------------------------------------------------------------------

func TestSync_ReplayMatchesLiveProcessing(t *testing.T) {
	// Process lifecycle events "live" and record the final snapshot digest.
	m := DefaultTestnetManifest()
	rLive, _ := SeedReducerFromManifest(m)

	// Simulate a sequence of lifecycle events.
	events := []struct {
		evType  event.EventType
		payload interface{}
		ts      uint64
	}{
		{event.EventTypeValidatorJoin, ValidatorJoinPayload{
			Version: 1, ValidatorID: "v-new", OperatorAgentID: "k-new", ConsensusPublicKey: "k-new",
			KeyEpoch: 1, BondedStake: MinBondedStake, EffectiveFromVersion: 1,
		}, 100},
		{event.EventTypeValidatorSuspend, ValidatorSuspendPayload{
			Version: 1, ValidatorID: "v-new", Reason: "test", EffectiveFromVersion: 2,
		}, 200},
	}

	var dagEvents []*event.Event
	for _, e := range events {
		ev := createLifecycleDAGEvent(t, e.evType, e.payload, e.ts)
		dagEvents = append(dagEvents, ev)
		lcEvents, err := ExtractLifecycleEvent(ev)
		if err != nil {
			t.Fatalf("extract: %v", err)
		}
		for _, lc := range lcEvents {
			_ = rLive.Apply(lc)
		}
	}
	liveDigest := rLive.Snapshot().ComputeDigest()

	// Now "replay" from scratch: seed from manifest, then replay the same events.
	rReplay, _ := SeedReducerFromManifest(m)
	for _, ev := range dagEvents {
		lcEvents, err := ExtractLifecycleEvent(ev)
		if err != nil {
			continue
		}
		for _, lc := range lcEvents {
			_ = rReplay.Apply(lc)
		}
	}
	replayDigest := rReplay.Snapshot().ComputeDigest()

	if liveDigest != replayDigest {
		t.Fatalf("replay digest differs from live: %s vs %s", replayDigest, liveDigest)
	}
}

// ---------------------------------------------------------------------------
// Full sync pipeline: DAG event → Extract → Apply → Snapshot → VoteWeight
// ---------------------------------------------------------------------------

func TestSync_FullPipeline_JoinActivateVote(t *testing.T) {
	m := &GenesisManifest{
		Entries: []GenesisManifestEntry{
			{ValidatorID: "gen-1", OperatorAgentID: "k-gen", ConsensusPublicKey: "k-gen",
				KeyEpoch: 1, BondedStake: 100_000, InitialStatus: SeatActive},
		},
	}
	r, _ := SeedReducerFromManifest(m)

	// Genesis validator should be eligible.
	snap := r.Snapshot()
	w, eligible := snap.VoteWeightByKey("k-gen")
	if !eligible || w != 100_000 {
		t.Fatalf("genesis validator should be eligible with weight 100000, got eligible=%v weight=%d", eligible, w)
	}

	// New validator joins via sync.
	joinEv := createLifecycleDAGEvent(t, event.EventTypeValidatorJoin, ValidatorJoinPayload{
		Version: 1, ValidatorID: "v-new", OperatorAgentID: "k-new", ConsensusPublicKey: "k-new",
		KeyEpoch: 1, BondedStake: MinBondedStake, EffectiveFromVersion: 1,
	}, 100)
	lcEvents, _ := ExtractLifecycleEvent(joinEv)
	for _, lc := range lcEvents {
		_ = r.Apply(lc)
	}

	// PendingJoin — not eligible yet.
	snap = r.Snapshot()
	_, eligible = snap.VoteWeightByKey("k-new")
	if eligible {
		t.Fatal("PendingJoin seat should not be vote-eligible")
	}

	// Genesis validator still eligible after someone else joins.
	_, eligible = snap.VoteWeightByKey("k-gen")
	if !eligible {
		t.Fatal("genesis validator should still be eligible after new join")
	}
}
