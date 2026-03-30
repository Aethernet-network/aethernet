// Integration tests for the validator lifecycle subsystem. These exercise the
// full lifecycle pipeline: genesis manifest → Reducer → Snapshot → Consensus
// VotingRound → committee selection → vote admission → settlement.
//
// No HTTP servers, no real TCP connections, no external infrastructure.
// All tests are deterministic and reproducible.
package integration_test

import (
	"errors"
	"testing"
	"time"

	"github.com/Aethernet-network/aethernet/internal/consensus"
	"github.com/Aethernet-network/aethernet/internal/crypto"
	"github.com/Aethernet-network/aethernet/internal/event"
	"github.com/Aethernet-network/aethernet/internal/identity"
	"github.com/Aethernet-network/aethernet/internal/validatorlifecycle"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// threeNodeGenesis creates a genesis manifest with 3 Active validators.
func threeNodeGenesis() *validatorlifecycle.GenesisManifest {
	return &validatorlifecycle.GenesisManifest{
		Entries: []validatorlifecycle.GenesisManifestEntry{
			{
				ValidatorID: "node-1", OperatorAgentID: "key-n1", ConsensusPublicKey: "key-n1",
				KeyEpoch: 1, BondedStake: 100_000, InitialStatus: validatorlifecycle.SeatActive,
			},
			{
				ValidatorID: "node-2", OperatorAgentID: "key-n2", ConsensusPublicKey: "key-n2",
				KeyEpoch: 1, BondedStake: 100_000, InitialStatus: validatorlifecycle.SeatActive,
			},
			{
				ValidatorID: "node-3", OperatorAgentID: "key-n3", ConsensusPublicKey: "key-n3",
				KeyEpoch: 1, BondedStake: 100_000, InitialStatus: validatorlifecycle.SeatActive,
			},
		},
	}
}

// seedAndSnapshot seeds a reducer from a manifest and returns both.
func seedAndSnapshot(t *testing.T, m *validatorlifecycle.GenesisManifest) (*validatorlifecycle.Reducer, *validatorlifecycle.ValidatorSnapshot) {
	t.Helper()
	r, err := validatorlifecycle.SeedReducerFromManifest(m)
	if err != nil {
		t.Fatalf("SeedReducerFromManifest: %v", err)
	}
	return r, r.Snapshot()
}

// snapshotVotingRound creates a VotingRound bound to a snapshot with a
// registry that has entries for all participating seats. MinParticipants
// is set to the participating seat count.
func snapshotVotingRound(t *testing.T, snap *validatorlifecycle.ValidatorSnapshot) *consensus.VotingRound {
	t.Helper()
	reg := identity.NewRegistry()
	for _, seat := range snap.ParticipatingSeats() {
		fp := &identity.CapabilityFingerprint{
			AgentID:              seat.OperatorKey,
			PublicKey:            make([]byte, 32),
			ReputationScore:      5000,
			StakedAmount:         seat.StakeAmount,
			OptimisticTrustLimit: 1000,
			Capabilities:         []identity.Capability{},
			FirstSeen:            time.Now(),
			LastActive:           time.Now(),
			FingerprintVersion:   1,
		}
		_ = reg.Register(fp)
	}
	cfg := &consensus.ConsensusConfig{
		SupermajorityThreshold: 0.667,
		MaxRounds:              10,
		RoundTimeout:           5 * time.Second,
		MinParticipants:        snap.ActiveSeatCount,
	}
	vr := consensus.NewVotingRound(cfg, reg)
	vr.SetValidatorSet(snap)
	return vr
}

// ---------------------------------------------------------------------------
// 1. Genesis startup: 3 nodes produce identical snapshots
// ---------------------------------------------------------------------------

func TestIntegration_GenesisStartup_IdenticalSnapshots(t *testing.T) {
	m := threeNodeGenesis()

	// Simulate 3 independent nodes seeding from the same manifest.
	var digests [3]string
	for i := 0; i < 3; i++ {
		r, err := validatorlifecycle.SeedReducerFromManifest(m)
		if err != nil {
			t.Fatalf("node %d: %v", i, err)
		}
		snap := r.Snapshot()
		digests[i] = snap.ComputeDigest()
	}

	if digests[0] != digests[1] || digests[1] != digests[2] {
		t.Fatalf("genesis snapshots diverge: %s, %s, %s", digests[0], digests[1], digests[2])
	}
}

// ---------------------------------------------------------------------------
// 2. Settlement with snapshot-bound vote eligibility
// ---------------------------------------------------------------------------

func TestIntegration_SnapshotBoundSettlement(t *testing.T) {
	_, snap := seedAndSnapshot(t, threeNodeGenesis())
	vr := snapshotVotingRound(t, snap)

	eid := event.EventID("tx-to-settle")

	// All 3 genesis validators vote yes.
	for _, key := range []crypto.AgentID{"key-n1", "key-n2", "key-n3"} {
		if err := vr.RegisterVote(eid, key, true, 0); err != nil {
			t.Fatalf("vote from %s: %v", key, err)
		}
	}

	finalized, _ := vr.IsFinalized(eid)
	if !finalized {
		t.Fatal("expected finalization after 3/3 yes votes")
	}

	rec, _ := vr.GetRecord(eid)
	if rec.TotalWeight != 300_000 {
		t.Fatalf("expected TotalWeight 300000, got %d", rec.TotalWeight)
	}
	if rec.ValidatorSetVersion != snap.SetVersion() {
		t.Fatalf("round should capture snapshot version %d, got %d",
			snap.SetVersion(), rec.ValidatorSetVersion)
	}
}

// ---------------------------------------------------------------------------
// 3. Join path: new validator not eligible for old rounds
// ---------------------------------------------------------------------------

func TestIntegration_JoinNotEligibleForOldRounds(t *testing.T) {
	r, _ := seedAndSnapshot(t, threeNodeGenesis())

	// Take a snapshot BEFORE the new validator joins.
	preJoinSnap := r.Snapshot()
	preJoinVR := snapshotVotingRound(t, preJoinSnap)

	// New validator joins.
	err := r.Apply(validatorlifecycle.LifecycleEvent{
		Kind:        validatorlifecycle.EventJoin,
		EventID:     "join-new",
		CausalTS:    100,
		SeatID:      "node-4",
		OperatorKey: "key-n4",
		StakeAmount: validatorlifecycle.MinBondedStake,
		IsGenesis:   true, // exempt from stake check for test simplicity
	})
	if err != nil {
		t.Fatalf("join: %v", err)
	}

	// Open a round on the pre-join snapshot. The new validator should NOT
	// be able to vote — they joined after this snapshot.
	eid := event.EventID("old-round")
	for _, key := range []crypto.AgentID{"key-n1", "key-n2"} {
		_ = preJoinVR.RegisterVote(eid, key, true, 0)
	}

	// New validator tries to vote on the old round.
	err = preJoinVR.RegisterVote(eid, "key-n4", true, 0)
	if err == nil {
		t.Fatal("new validator should NOT be eligible for pre-join round")
	}
	if !errors.Is(err, consensus.ErrVoterNotInSnapshot) {
		t.Fatalf("expected ErrVoterNotInSnapshot, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// 4. Exit path: old votes remain valid, new rounds exclude exited seat
// ---------------------------------------------------------------------------

func TestIntegration_ExitOldVotesValid(t *testing.T) {
	r, _ := seedAndSnapshot(t, threeNodeGenesis())

	// Take a snapshot while all 3 are active.
	activeSnap := r.Snapshot()

	// Node-1 begins cooldown and exits.
	_ = r.Apply(validatorlifecycle.LifecycleEvent{
		Kind: validatorlifecycle.EventBeginCooldown, EventID: "cd-1", CausalTS: 10,
		SeatID: "node-1", CooldownDuration: 5,
	})
	_ = r.Apply(validatorlifecycle.LifecycleEvent{
		Kind: validatorlifecycle.EventExit, EventID: "exit-1", CausalTS: 20,
		SeatID: "node-1",
	})

	// Old snapshot: node-1's votes are still valid.
	w, eligible := activeSnap.VoteWeightByKey("key-n1")
	if !eligible || w != 100_000 {
		t.Fatalf("exited node-1 should still be eligible in pre-exit snapshot: eligible=%v weight=%d", eligible, w)
	}

	// New snapshot: node-1 is NOT eligible.
	postExitSnap := r.Snapshot()
	_, eligible = postExitSnap.VoteWeightByKey("key-n1")
	if eligible {
		t.Fatal("exited node-1 should NOT be eligible in post-exit snapshot")
	}

	// New round on post-exit snapshot: only node-2 and node-3 can vote.
	postExitVR := snapshotVotingRound(t, postExitSnap)
	eid := event.EventID("post-exit-round")
	err := postExitVR.RegisterVote(eid, "key-n1", true, 0)
	if err == nil {
		t.Fatal("exited node-1 should be rejected in post-exit round")
	}
}

// ---------------------------------------------------------------------------
// 5. Suspend/resume returns through Probationary
// ---------------------------------------------------------------------------

func TestIntegration_SuspendResumeProbationary(t *testing.T) {
	r, _ := seedAndSnapshot(t, threeNodeGenesis())

	// Suspend node-2.
	_ = r.Apply(validatorlifecycle.LifecycleEvent{
		Kind: validatorlifecycle.EventSuspend, EventID: "susp-2", CausalTS: 10,
		SeatID: "node-2", Reason: "stake deficit",
	})

	seat, _ := r.Seat("node-2")
	if seat.Status != validatorlifecycle.SeatSuspended {
		t.Fatalf("expected Suspended, got %s", seat.Status)
	}

	// Suspended seat should NOT be eligible.
	suspSnap := r.Snapshot()
	_, eligible := suspSnap.VoteWeightByKey("key-n2")
	if eligible {
		t.Fatal("suspended seat should NOT be eligible")
	}

	// Resume → returns to Probationary, not Active.
	_ = r.Apply(validatorlifecycle.LifecycleEvent{
		Kind: validatorlifecycle.EventReinstate, EventID: "rein-2", CausalTS: 20,
		SeatID: "node-2",
	})

	seat, _ = r.Seat("node-2")
	if seat.Status != validatorlifecycle.SeatProbationary {
		t.Fatalf("resume should return to Probationary, got %s", seat.Status)
	}
	if seat.Weight != 50_000 { // half of 100,000
		t.Fatalf("probationary weight should be half, got %d", seat.Weight)
	}

	// Probationary seat IS eligible (participating status).
	probSnap := r.Snapshot()
	w, eligible := probSnap.VoteWeightByKey("key-n2")
	if !eligible {
		t.Fatal("probationary seat should be eligible")
	}
	if w != 50_000 {
		t.Fatalf("expected weight 50000, got %d", w)
	}
}

// ---------------------------------------------------------------------------
// 6. Key rotation across round boundaries
// ---------------------------------------------------------------------------

func TestIntegration_KeyRotationAcrossRoundBoundary(t *testing.T) {
	r, _ := seedAndSnapshot(t, threeNodeGenesis())

	// Take snapshot before rotation (represents an open round).
	preRotateSnap := r.Snapshot()
	preRotateVR := snapshotVotingRound(t, preRotateSnap)

	// Node-1 rotates key.
	_ = r.Apply(validatorlifecycle.LifecycleEvent{
		Kind: validatorlifecycle.EventRotateKey, EventID: "rot-1", CausalTS: 50,
		SeatID: "node-1", OperatorKey: "key-n1-new", OldOperatorKey: "key-n1",
	})

	postRotateSnap := r.Snapshot()
	postRotateVR := snapshotVotingRound(t, postRotateSnap)

	// Pre-rotation round: old key valid, new key invalid.
	oldRound := event.EventID("old-round-kr")
	if err := preRotateVR.RegisterVote(oldRound, "key-n1", true, 0); err != nil {
		t.Fatalf("old key should be valid for pre-rotate round: %v", err)
	}
	if err := preRotateVR.RegisterVote(oldRound, "key-n1-new", true, 0); err == nil {
		t.Fatal("new key should NOT be valid for pre-rotate round")
	}

	// Post-rotation round: new key valid, old key invalid.
	newRound := event.EventID("new-round-kr")
	if err := postRotateVR.RegisterVote(newRound, "key-n1-new", true, 0); err != nil {
		t.Fatalf("new key should be valid for post-rotate round: %v", err)
	}
	if err := postRotateVR.RegisterVote(newRound, "key-n1", true, 0); err == nil {
		t.Fatal("old key should NOT be valid for post-rotate round")
	}
}

// ---------------------------------------------------------------------------
// 7. Slash / cooldown / re-entry
// ---------------------------------------------------------------------------

func TestIntegration_SlashCooldownReentry(t *testing.T) {
	r, _ := seedAndSnapshot(t, threeNodeGenesis())

	// Slash node-3 with cooldown.
	_ = r.Apply(validatorlifecycle.LifecycleEvent{
		Kind: validatorlifecycle.EventSlash, EventID: "slash-3", CausalTS: 10,
		SeatID: "node-3", Reason: "dishonest replay",
		SlashAmount: 30_000, CooldownDuration: 50,
	})

	seat, _ := r.Seat("node-3")
	if seat.Status != validatorlifecycle.SeatSuspended {
		t.Fatalf("expected Suspended after slash, got %s", seat.Status)
	}
	if seat.StakeAmount != 70_000 {
		t.Fatalf("expected 70000 stake after slash, got %d", seat.StakeAmount)
	}

	// Slashed seat is ineligible.
	slashSnap := r.Snapshot()
	_, eligible := slashSnap.VoteWeightByKey("key-n3")
	if eligible {
		t.Fatal("slashed seat should NOT be eligible")
	}

	// Early reinstate fails.
	err := r.Apply(validatorlifecycle.LifecycleEvent{
		Kind: validatorlifecycle.EventReinstate, EventID: "rein-early", CausalTS: 30,
		SeatID: "node-3",
	})
	if !errors.Is(err, validatorlifecycle.ErrCooldownNotElapsed) {
		t.Fatalf("expected ErrCooldownNotElapsed, got %v", err)
	}

	// Reinstate after cooldown.
	err = r.Apply(validatorlifecycle.LifecycleEvent{
		Kind: validatorlifecycle.EventReinstate, EventID: "rein-ok", CausalTS: 70,
		SeatID: "node-3",
	})
	if err != nil {
		t.Fatalf("reinstate after cooldown should succeed: %v", err)
	}

	seat, _ = r.Seat("node-3")
	if seat.Status != validatorlifecycle.SeatProbationary {
		t.Fatalf("expected Probationary after reinstate, got %s", seat.Status)
	}

	// Re-entered seat is eligible (probationary, participating).
	reentrySnap := r.Snapshot()
	w, eligible := reentrySnap.VoteWeightByKey("key-n3")
	if !eligible {
		t.Fatal("reinstated seat should be eligible")
	}
	if w != 35_000 { // 70000 / 2 (probationary half weight)
		t.Fatalf("expected probationary weight 35000, got %d", w)
	}
}

// ---------------------------------------------------------------------------
// 8. Replay determinism: same history → same snapshot digest
// ---------------------------------------------------------------------------

func TestIntegration_ReplayDeterminism(t *testing.T) {
	// Define a lifecycle history.
	history := []validatorlifecycle.LifecycleEvent{
		{Kind: validatorlifecycle.EventJoin, EventID: "j1", CausalTS: 1, SeatID: "s1",
			OperatorKey: "k1", StakeAmount: 100_000, IsGenesis: true},
		{Kind: validatorlifecycle.EventJoin, EventID: "j2", CausalTS: 2, SeatID: "s2",
			OperatorKey: "k2", StakeAmount: 200_000, IsGenesis: true},
		{Kind: validatorlifecycle.EventJoin, EventID: "j3", CausalTS: 3, SeatID: "s3",
			OperatorKey: "k3", StakeAmount: 150_000, IsGenesis: true},
	}

	// Replay twice and compare digests.
	var digests [2]string
	for attempt := 0; attempt < 2; attempt++ {
		r := validatorlifecycle.NewReducer()
		for _, ev := range history {
			if err := r.Apply(ev); err != nil {
				t.Fatalf("attempt %d: apply %s: %v", attempt, ev.EventID, err)
			}
		}
		snap := r.Snapshot()
		digests[attempt] = snap.ComputeDigest()
	}

	if digests[0] != digests[1] {
		t.Fatalf("replay produced different digests: %s vs %s", digests[0], digests[1])
	}
}

func TestIntegration_ExtendedReplayDeterminism(t *testing.T) {
	// A more complex history with multiple lifecycle transitions.
	buildHistory := func() []validatorlifecycle.LifecycleEvent {
		return []validatorlifecycle.LifecycleEvent{
			{Kind: validatorlifecycle.EventJoin, EventID: "j1", CausalTS: 1, SeatID: "s1",
				OperatorKey: "k1", StakeAmount: 100_000, IsGenesis: true},
			{Kind: validatorlifecycle.EventJoin, EventID: "j2", CausalTS: 2, SeatID: "s2",
				OperatorKey: "k2", StakeAmount: 200_000, IsGenesis: true},
			{Kind: validatorlifecycle.EventSuspend, EventID: "susp-s1", CausalTS: 10,
				SeatID: "s1", Reason: "stake deficit"},
			{Kind: validatorlifecycle.EventReinstate, EventID: "rein-s1", CausalTS: 20,
				SeatID: "s1"},
			{Kind: validatorlifecycle.EventRotateKey, EventID: "rot-s2", CausalTS: 30,
				SeatID: "s2", OperatorKey: "k2-new", OldOperatorKey: "k2"},
			{Kind: validatorlifecycle.EventSlash, EventID: "slash-s1", CausalTS: 40,
				SeatID: "s1", Reason: "fraud", SlashAmount: 20_000, CooldownDuration: 50},
			{Kind: validatorlifecycle.EventReinstate, EventID: "rein-s1-2", CausalTS: 100,
				SeatID: "s1"},
			{Kind: validatorlifecycle.EventStakeUpdate, EventID: "stake-s2", CausalTS: 110,
				SeatID: "s2", StakeAmount: 300_000},
		}
	}

	var digests [3]string
	for i := 0; i < 3; i++ {
		r := validatorlifecycle.NewReducer()
		for _, ev := range buildHistory() {
			_ = r.Apply(ev)
		}
		snap := r.Snapshot()
		digests[i] = snap.ComputeDigest()
	}

	if digests[0] != digests[1] || digests[1] != digests[2] {
		t.Fatalf("extended replay diverged: %s, %s, %s", digests[0], digests[1], digests[2])
	}
}

// ---------------------------------------------------------------------------
// 9. Identity exists but seat not in snapshot → vote rejected
// ---------------------------------------------------------------------------

func TestIntegration_IdentityExistsButNoSeat(t *testing.T) {
	_, snap := seedAndSnapshot(t, threeNodeGenesis())

	// Create a voting round with the snapshot.
	reg := identity.NewRegistry()
	// Register all genesis validators in the identity registry.
	for _, seat := range snap.ParticipatingSeats() {
		fp := &identity.CapabilityFingerprint{
			AgentID:            seat.OperatorKey,
			PublicKey:          make([]byte, 32),
			ReputationScore:    5000,
			StakedAmount:       seat.StakeAmount,
			Capabilities:       []identity.Capability{},
			FirstSeen:          time.Now(),
			LastActive:         time.Now(),
			FingerprintVersion: 1,
		}
		_ = reg.Register(fp)
	}

	// Also register an agent that is NOT in the snapshot.
	outsiderFP := &identity.CapabilityFingerprint{
		AgentID:              "key-outsider",
		PublicKey:            make([]byte, 32),
		ReputationScore:      8000,
		StakedAmount:         500_000,
		OptimisticTrustLimit: 1000,
		Capabilities:         []identity.Capability{},
		FirstSeen:            time.Now(),
		LastActive:           time.Now(),
		FingerprintVersion:   1,
	}
	_ = reg.Register(outsiderFP)

	cfg := &consensus.ConsensusConfig{
		SupermajorityThreshold: 0.667,
		MaxRounds:              10,
		RoundTimeout:           5 * time.Second,
		MinParticipants:        1,
	}
	vr := consensus.NewVotingRound(cfg, reg)
	vr.SetValidatorSet(snap)

	eid := event.EventID("outsider-round")

	// Outsider has high rep/stake in the registry but NO seat in snapshot.
	err := vr.RegisterVote(eid, "key-outsider", true, 0)
	if err == nil {
		t.Fatal("outsider should be REJECTED despite having identity in registry")
	}
	if !errors.Is(err, consensus.ErrVoterNotInSnapshot) {
		t.Fatalf("expected ErrVoterNotInSnapshot, got: %v", err)
	}

	// Genesis validator IS accepted.
	if err := vr.RegisterVote(eid, "key-n1", true, 0); err != nil {
		t.Fatalf("genesis validator should be accepted: %v", err)
	}
}

// ---------------------------------------------------------------------------
// 10. Committee selection is deterministic across nodes
// ---------------------------------------------------------------------------

func TestIntegration_CommitteeSelectionDeterministic(t *testing.T) {
	m := threeNodeGenesis()

	// 3 independent nodes select a committee for the same event.
	var digests [3]string
	for i := 0; i < 3; i++ {
		r, err := validatorlifecycle.SeedReducerFromManifest(m)
		if err != nil {
			t.Fatalf("node %d: %v", i, err)
		}
		snap := r.Snapshot()
		cs := snap.SelectCommittee("event-abc")
		digests[i] = cs.Digest
	}

	if digests[0] != digests[1] || digests[1] != digests[2] {
		t.Fatalf("committee digests diverge: %s, %s, %s", digests[0], digests[1], digests[2])
	}
}

// ---------------------------------------------------------------------------
// 11. Full lifecycle journey: join → activate → slash → cooldown → reinstate
// ---------------------------------------------------------------------------

func TestIntegration_FullLifecycleJourney(t *testing.T) {
	r, _ := seedAndSnapshot(t, threeNodeGenesis())

	// A new validator joins.
	_ = r.Apply(validatorlifecycle.LifecycleEvent{
		Kind: validatorlifecycle.EventJoin, EventID: "j-new", CausalTS: 100,
		SeatID: "node-4", OperatorKey: "key-n4",
		StakeAmount: validatorlifecycle.MinBondedStake, IsGenesis: true,
	})

	// PendingJoin → cannot vote yet (not participating).
	s4, _ := r.Seat("node-4")
	if s4.Status != validatorlifecycle.SeatPendingJoin {
		t.Fatalf("expected PendingJoin, got %s", s4.Status)
	}
	pendingSnap := r.Snapshot()
	_, eligible := pendingSnap.VoteWeightByKey("key-n4")
	if eligible {
		t.Fatal("PendingJoin seat should not be vote-eligible")
	}

	// PendingJoin → Active is disallowed (PendingJoin only allows → Probationary).
	// Use a second SeedReducerFromManifest to create node-4 as Active for the
	// slash test. In production, a separate probation-entry event would advance
	// the seat. For this integration test, we re-seed with node-4 included.
	mWithN4 := &validatorlifecycle.GenesisManifest{
		Entries: append(threeNodeGenesis().Entries, validatorlifecycle.GenesisManifestEntry{
			ValidatorID: "node-4", OperatorAgentID: "key-n4", ConsensusPublicKey: "key-n4",
			KeyEpoch: 1, BondedStake: validatorlifecycle.MinBondedStake, InitialStatus: validatorlifecycle.SeatActive,
		}),
	}
	r, _ = seedAndSnapshot(t, mWithN4)

	// Slash node-4.
	_ = r.Apply(validatorlifecycle.LifecycleEvent{
		Kind: validatorlifecycle.EventSlash, EventID: "slash-n4", CausalTS: 200,
		SeatID: "node-4", Reason: "test slash",
		SlashAmount: 1_000_000_000, CooldownDuration: 100,
	})

	s4, _ = r.Seat("node-4")
	if s4.Status != validatorlifecycle.SeatSuspended {
		t.Fatalf("expected Suspended after slash, got %s", s4.Status)
	}

	// Reinstate after cooldown.
	_ = r.Apply(validatorlifecycle.LifecycleEvent{
		Kind: validatorlifecycle.EventReinstate, EventID: "rein-n4", CausalTS: 310,
		SeatID: "node-4",
	})

	s4, _ = r.Seat("node-4")
	if s4.Status != validatorlifecycle.SeatProbationary {
		t.Fatalf("expected Probationary after reinstate, got %s", s4.Status)
	}
}
