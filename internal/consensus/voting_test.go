package consensus_test

import (
	"errors"
	"fmt"
	"sync"
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

// registerAgent adds an agent to reg with preset ReputationScore (0–10000 bp)
// and StakedAmount (micro-AET). Both fields are set directly on the fingerprint
// so tests can exercise precise weight values without driving the registry's
// task-completion machinery.
func registerAgent(t *testing.T, reg *identity.Registry, agentID crypto.AgentID, rep, stake uint64) {
	t.Helper()
	now := time.Now().UTC()
	fp := &identity.CapabilityFingerprint{
		AgentID:              agentID,
		PublicKey:            make([]byte, 32),
		ReputationScore:      rep,
		StakedAmount:         stake,
		OptimisticTrustLimit: 1000,
		Capabilities:         []identity.Capability{},
		FirstSeen:            now,
		LastActive:           now,
		FingerprintVersion:   1,
	}
	if err := reg.Register(fp); err != nil {
		t.Fatalf("registerAgent %s: %v", agentID, err)
	}
}

// newRound returns a VotingRound backed by reg using DefaultConsensusConfig
// (SupermajorityThreshold=0.667, MaxRounds=10, MinParticipants=3).
func newRound(reg *identity.Registry) *consensus.VotingRound {
	return consensus.NewVotingRound(nil, reg)
}

// ---------------------------------------------------------------------------
// Weight computation
// ---------------------------------------------------------------------------

func TestComputeWeight_ZeroReputation(t *testing.T) {
	reg := identity.NewRegistry()
	registerAgent(t, reg, "voter-zero-rep", 0, 5000)
	vr := newRound(reg)

	w, err := vr.ComputeWeight("voter-zero-rep")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if w != 0 {
		t.Errorf("want weight 0 (zero reputation), got %d", w)
	}
}

func TestComputeWeight_ZeroStake(t *testing.T) {
	reg := identity.NewRegistry()
	registerAgent(t, reg, "voter-zero-stake", 5000, 0)
	vr := newRound(reg)

	w, err := vr.ComputeWeight("voter-zero-stake")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if w != 0 {
		t.Errorf("want weight 0 (zero stake), got %d", w)
	}
}

func TestComputeWeight_BothSet(t *testing.T) {
	reg := identity.NewRegistry()
	// rep=5000, stake=2000 → weight = 5000 * 2000 / 10000 = 1000
	registerAgent(t, reg, "voter-both", 5000, 2000)
	vr := newRound(reg)

	w, err := vr.ComputeWeight("voter-both")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := uint64(5000 * 2000 / 10000)
	if w != want {
		t.Errorf("want weight %d, got %d", want, w)
	}
}

func TestComputeWeight_UnknownAgent(t *testing.T) {
	reg := identity.NewRegistry()
	vr := newRound(reg)

	_, err := vr.ComputeWeight(crypto.AgentID("nobody"))
	if err == nil {
		t.Fatal("want error for unknown agent, got nil")
	}
}

// ---------------------------------------------------------------------------
// Vote registration
// ---------------------------------------------------------------------------

func TestRegisterVote_CreatesRecord(t *testing.T) {
	reg := identity.NewRegistry()
	registerAgent(t, reg, "voter-a", 5000, 1000)
	vr := newRound(reg)
	eid := event.EventID("event-001")

	if err := vr.RegisterVote(eid, "voter-a", true); err != nil {
		t.Fatalf("RegisterVote: %v", err)
	}

	rec, err := vr.GetRecord(eid)
	if err != nil {
		t.Fatalf("GetRecord: %v", err)
	}
	if rec.EventID != eid {
		t.Errorf("EventID: want %s, got %s", eid, rec.EventID)
	}
	if len(rec.Votes) != 1 {
		t.Errorf("want 1 vote recorded, got %d", len(rec.Votes))
	}
}

func TestRegisterVote_DuplicateVote(t *testing.T) {
	reg := identity.NewRegistry()
	registerAgent(t, reg, "voter-dup", 5000, 1000)
	vr := newRound(reg)
	eid := event.EventID("event-002")

	if err := vr.RegisterVote(eid, "voter-dup", true); err != nil {
		t.Fatalf("first RegisterVote: %v", err)
	}
	err := vr.RegisterVote(eid, "voter-dup", false)
	if !errors.Is(err, consensus.ErrDuplicateVote) {
		t.Errorf("want ErrDuplicateVote, got %v", err)
	}
}

func TestRegisterVote_VoteRecorded(t *testing.T) {
	reg := identity.NewRegistry()
	registerAgent(t, reg, "voter-rec", 5000, 1000)
	vr := newRound(reg)
	eid := event.EventID("event-003")

	if err := vr.RegisterVote(eid, "voter-rec", true); err != nil {
		t.Fatalf("RegisterVote: %v", err)
	}

	rec, err := vr.GetRecord(eid)
	if err != nil {
		t.Fatalf("GetRecord: %v", err)
	}
	vote, ok := rec.Votes[crypto.AgentID("voter-rec")]
	if !ok {
		t.Fatal("vote not found in VoteRecord")
	}
	if !vote {
		t.Error("want vote=true, got false")
	}
}

// ---------------------------------------------------------------------------
// Tally and finalization
// ---------------------------------------------------------------------------

func TestTally_SingleVoter_NotFinalized(t *testing.T) {
	// MinParticipants=3 (DefaultConsensusConfig); one voter never satisfies it.
	reg := identity.NewRegistry()
	registerAgent(t, reg, "solo", 5000, 2000)
	vr := newRound(reg)
	eid := event.EventID("event-solo")

	if err := vr.RegisterVote(eid, "solo", true); err != nil {
		t.Fatalf("RegisterVote: %v", err)
	}

	finalized, err := vr.IsFinalized(eid)
	if err != nil {
		t.Fatalf("IsFinalized: %v", err)
	}
	if finalized {
		t.Error("want not finalized (MinParticipants=3 not met), got finalized")
	}
}

func TestTally_ThreeYes_Finalized(t *testing.T) {
	// Three equal-weight voters, all yes → yesWeight/totalWeight = 1.0 ≥ 0.667.
	reg := identity.NewRegistry()
	registerAgent(t, reg, "v1", 5000, 2000)
	registerAgent(t, reg, "v2", 5000, 2000)
	registerAgent(t, reg, "v3", 5000, 2000)
	vr := newRound(reg)
	eid := event.EventID("event-three-yes")

	for _, voter := range []crypto.AgentID{"v1", "v2", "v3"} {
		if err := vr.RegisterVote(eid, voter, true); err != nil {
			t.Fatalf("RegisterVote %s: %v", voter, err)
		}
	}

	finalized, err := vr.IsFinalized(eid)
	if err != nil {
		t.Fatalf("IsFinalized: %v", err)
	}
	if !finalized {
		t.Error("want finalized after 3/3 yes votes, got not finalized")
	}
}

func TestTally_TwoYesOneNo_NoFinalize(t *testing.T) {
	// Equal weights: yesWeight/totalWeight = 2/3 = 0.6666… < 0.667 threshold.
	reg := identity.NewRegistry()
	registerAgent(t, reg, "va", 5000, 2000)
	registerAgent(t, reg, "vb", 5000, 2000)
	registerAgent(t, reg, "vc", 5000, 2000)
	vr := newRound(reg)
	eid := event.EventID("event-split")

	_ = vr.RegisterVote(eid, "va", true)
	_ = vr.RegisterVote(eid, "vb", true)
	_ = vr.RegisterVote(eid, "vc", false) // one no vote

	finalized, err := vr.IsFinalized(eid)
	if err != nil {
		t.Fatalf("IsFinalized: %v", err)
	}
	if finalized {
		t.Error("want not finalized (2/3 = 0.6666 < 0.667 threshold), got finalized")
	}
}

func TestFinalOrder_Sequential(t *testing.T) {
	// Three events finalized in succession must receive strictly increasing,
	// consecutive FinalOrder values (1, 2, 3).
	reg := identity.NewRegistry()
	registerAgent(t, reg, "u1", 5000, 2000)
	registerAgent(t, reg, "u2", 5000, 2000)
	registerAgent(t, reg, "u3", 5000, 2000)
	vr := newRound(reg)

	voters := []crypto.AgentID{"u1", "u2", "u3"}
	eids := []event.EventID{"evA", "evB", "evC"}

	// Finalize events one at a time in order.
	for _, eid := range eids {
		for _, voter := range voters {
			if err := vr.RegisterVote(eid, voter, true); err != nil {
				t.Fatalf("RegisterVote %s/%s: %v", eid, voter, err)
			}
		}
	}

	orders := make([]uint64, len(eids))
	for i, eid := range eids {
		order, err := vr.FinalOrder(eid)
		if err != nil {
			t.Fatalf("FinalOrder %s: %v", eid, err)
		}
		orders[i] = order
	}

	// Expect 1, 2, 3 — consecutive from the monotonic orderSeq.
	for i := 1; i < len(orders); i++ {
		if orders[i] != orders[i-1]+1 {
			t.Errorf("FinalOrder not consecutive: events[%d]=%d, events[%d]=%d",
				i-1, orders[i-1], i, orders[i])
		}
	}
	if orders[0] != 1 {
		t.Errorf("first FinalOrder: want 1, got %d", orders[0])
	}
}

func TestIsFinalized_BeforeSupermajority(t *testing.T) {
	reg := identity.NewRegistry()
	registerAgent(t, reg, "p1", 5000, 2000)
	vr := newRound(reg)
	eid := event.EventID("event-before")

	_ = vr.RegisterVote(eid, "p1", true) // only 1 of 3 required participants

	finalized, err := vr.IsFinalized(eid)
	if err != nil {
		t.Fatalf("IsFinalized: %v", err)
	}
	if finalized {
		t.Error("want false before supermajority, got true")
	}
}

func TestIsFinalized_AfterSupermajority(t *testing.T) {
	reg := identity.NewRegistry()
	registerAgent(t, reg, "q1", 5000, 2000)
	registerAgent(t, reg, "q2", 5000, 2000)
	registerAgent(t, reg, "q3", 5000, 2000)
	vr := newRound(reg)
	eid := event.EventID("event-after")

	for _, voter := range []crypto.AgentID{"q1", "q2", "q3"} {
		_ = vr.RegisterVote(eid, voter, true)
	}

	finalized, err := vr.IsFinalized(eid)
	if err != nil {
		t.Fatalf("IsFinalized: %v", err)
	}
	if !finalized {
		t.Error("want true after 3/3 supermajority, got false")
	}
}

func TestFinalOrder_ErrorForUnfinalized(t *testing.T) {
	reg := identity.NewRegistry()
	registerAgent(t, reg, "r1", 5000, 2000)
	vr := newRound(reg)
	eid := event.EventID("event-unfinalized")

	_ = vr.RegisterVote(eid, "r1", true) // 1 vote, below MinParticipants=3

	_, err := vr.FinalOrder(eid)
	if !errors.Is(err, consensus.ErrNotFinalized) {
		t.Errorf("want ErrNotFinalized, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// Concurrent tests
// ---------------------------------------------------------------------------

func TestConcurrent_SameEvent(t *testing.T) {
	// 10 goroutines each register a vote on the same event simultaneously.
	// All votes are false (no) to prevent early finalization from allowing only
	// a subset of goroutines to record their vote before ErrAlreadyFinalized fires.
	const numVoters = 10
	reg := identity.NewRegistry()
	for i := 0; i < numVoters; i++ {
		id := crypto.AgentID(fmt.Sprintf("concurrent-voter-%02d", i))
		registerAgent(t, reg, id, 5000, 2000)
	}
	vr := newRound(reg)
	eid := event.EventID("concurrent-event")

	var wg sync.WaitGroup
	for i := 0; i < numVoters; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			id := crypto.AgentID(fmt.Sprintf("concurrent-voter-%02d", i))
			_ = vr.RegisterVote(eid, id, false) // all no: yesWeight/totalWeight = 0
		}(i)
	}
	wg.Wait()

	rec, err := vr.GetRecord(eid)
	if err != nil {
		t.Fatalf("GetRecord after concurrent votes: %v", err)
	}
	if len(rec.Votes) != numVoters {
		t.Errorf("want %d votes recorded, got %d", numVoters, len(rec.Votes))
	}
}

func TestConcurrent_MultipleEvents(t *testing.T) {
	// numGoroutines goroutines each finalise their own distinct event by
	// registering 3 yes votes. All agents are pre-registered so goroutines
	// only contend on the VotingRound mutex, not the Registry.
	const numGoroutines = 10
	reg := identity.NewRegistry()
	for i := 0; i < numGoroutines; i++ {
		for j := 0; j < 3; j++ {
			id := crypto.AgentID(fmt.Sprintf("me-voter-%02d-%02d", i, j))
			registerAgent(t, reg, id, 5000, 2000)
		}
	}
	vr := consensus.NewVotingRound(nil, reg)

	var wg sync.WaitGroup
	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			eid := event.EventID(fmt.Sprintf("multi-event-%02d", i))
			for j := 0; j < 3; j++ {
				voter := crypto.AgentID(fmt.Sprintf("me-voter-%02d-%02d", i, j))
				_ = vr.RegisterVote(eid, voter, true)
			}
		}(i)
	}
	wg.Wait()

	got := vr.FinalizedCount()
	if got != numGoroutines {
		t.Errorf("want %d finalized events, got %d", numGoroutines, got)
	}
}

// ---------------------------------------------------------------------------
// Fix 1: Overflow-safe weight computation
// ---------------------------------------------------------------------------

func TestComputeWeight_NoOverflow(t *testing.T) {
	// Use values that would overflow uint64 under naive multiplication:
	// 10000 * 1_844_674_407_370_955_300 = 1.84e22, exceeding uint64 max (1.84e19).
	// The math/big implementation must produce the correct result.
	reg := identity.NewRegistry()
	const rep = uint64(10000)                      // max reputation
	const stake = uint64(1_844_674_407_370_955_300) // near uint64 max / 10000
	registerAgent(t, reg, "whale", rep, stake)
	vr := newRound(reg)

	w, err := vr.ComputeWeight("whale")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Expected: 10000 * 1_844_674_407_370_955_300 / 10000 = 1_844_674_407_370_955_300
	if w != stake {
		t.Errorf("want weight %d, got %d (overflow detected!)", stake, w)
	}
}

func TestComputeWeight_LargeValues_NoWrapAround(t *testing.T) {
	// Values that definitely overflow a uint64 multiplication:
	// 10000 * MaxUint64 = overflow. Result should saturate, not wrap.
	reg := identity.NewRegistry()
	registerAgent(t, reg, "mega", 10000, ^uint64(0))
	vr := newRound(reg)

	w, err := vr.ComputeWeight("mega")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// The big.Int result exceeds uint64, so it should saturate at MaxUint64.
	if w == 0 {
		t.Fatal("weight wrapped to zero — overflow bug present")
	}
	// Since 10000 * MaxUint64 / 10000 = MaxUint64, the result should be MaxUint64.
	if w != ^uint64(0) {
		t.Errorf("want MaxUint64 (%d), got %d", ^uint64(0), w)
	}
}

// ---------------------------------------------------------------------------
// Snapshot-bound consensus tests
// ---------------------------------------------------------------------------

// buildSnapshot creates a lifecycle Reducer with the given seats, all Active,
// and returns the snapshot. Uses SeedReducerFromManifest which handles the
// PendingJoin → Probationary → Active genesis bypass correctly.
func buildSnapshot(t *testing.T, seats []struct {
	id    validatorlifecycle.ValidatorID
	key   crypto.AgentID
	stake uint64
}) *validatorlifecycle.ValidatorSnapshot {
	t.Helper()
	entries := make([]validatorlifecycle.GenesisManifestEntry, len(seats))
	for i, s := range seats {
		entries[i] = validatorlifecycle.GenesisManifestEntry{
			ValidatorID:        s.id,
			OperatorAgentID:    s.key,
			ConsensusPublicKey: s.key,
			KeyEpoch:           1,
			BondedStake:        s.stake,
			InitialStatus:      validatorlifecycle.SeatActive,
		}
	}
	manifest := &validatorlifecycle.GenesisManifest{Entries: entries}
	r, err := validatorlifecycle.SeedReducerFromManifest(manifest)
	if err != nil {
		t.Fatalf("SeedReducerFromManifest: %v", err)
	}
	return r.Snapshot()
}

func TestSnapshot_VoteRejectedIfNotInSnapshot(t *testing.T) {
	// Create snapshot with two seats.
	snap := buildSnapshot(t, []struct {
		id    validatorlifecycle.ValidatorID
		key   crypto.AgentID
		stake uint64
	}{
		{"seat-1", "key-alice", 100_000},
		{"seat-2", "key-bob", 200_000},
	})

	// VotingRound with snapshot bound.
	reg := identity.NewRegistry()
	// Register a third agent in the registry but NOT in the snapshot.
	registerAgent(t, reg, "key-eve", 5000, 100_000)
	cfg := &consensus.ConsensusConfig{
		SupermajorityThreshold: 0.667,
		MaxRounds:              10,
		RoundTimeout:           5 * time.Second,
		MinParticipants:        1,
	}
	vr := consensus.NewVotingRound(cfg, reg)
	vr.SetValidatorSet(snap)

	eid := event.EventID("test-event-snap")

	// Eve is in the registry but NOT in the snapshot — should be rejected.
	err := vr.RegisterVote(eid, "key-eve", true)
	if !errors.Is(err, consensus.ErrVoterNotInSnapshot) {
		t.Fatalf("expected ErrVoterNotInSnapshot for eve, got: %v", err)
	}

	// Alice IS in the snapshot — should succeed.
	if err := vr.RegisterVote(eid, "key-alice", true); err != nil {
		t.Fatalf("alice should be accepted: %v", err)
	}
}

func TestSnapshot_WeightFromSnapshot(t *testing.T) {
	// Snapshot gives alice weight 100_000, bob weight 200_000.
	snap := buildSnapshot(t, []struct {
		id    validatorlifecycle.ValidatorID
		key   crypto.AgentID
		stake uint64
	}{
		{"seat-a", "key-alice", 100_000},
		{"seat-b", "key-bob", 200_000},
	})

	// Registry gives alice different values — snapshot should take precedence.
	reg := identity.NewRegistry()
	registerAgent(t, reg, "key-alice", 9999, 999_999) // would give huge weight from registry
	registerAgent(t, reg, "key-bob", 9999, 999_999)

	vr := consensus.NewVotingRound(nil, reg)
	vr.SetValidatorSet(snap)

	// Weight should come from snapshot, not registry.
	w, err := vr.ComputeWeight("key-alice")
	if err != nil {
		t.Fatalf("ComputeWeight alice: %v", err)
	}
	if w != 100_000 {
		t.Fatalf("expected weight 100_000 from snapshot, got %d", w)
	}
	w, err = vr.ComputeWeight("key-bob")
	if err != nil {
		t.Fatalf("ComputeWeight bob: %v", err)
	}
	if w != 200_000 {
		t.Fatalf("expected weight 200_000 from snapshot, got %d", w)
	}
}

func TestSnapshot_SameVotesSameDeterministicResult(t *testing.T) {
	seats := []struct {
		id    validatorlifecycle.ValidatorID
		key   crypto.AgentID
		stake uint64
	}{
		{"s1", "k1", 100_000},
		{"s2", "k2", 100_000},
		{"s3", "k3", 100_000},
	}
	snap := buildSnapshot(t, seats)

	// Run the same voting sequence twice with the same snapshot.
	for attempt := 0; attempt < 2; attempt++ {
		reg := identity.NewRegistry()
		for _, s := range seats {
			registerAgent(t, reg, s.key, 5000, uint64(s.stake))
		}
		cfg := &consensus.ConsensusConfig{
			SupermajorityThreshold: 0.667,
			MaxRounds:              10,
			RoundTimeout:           5 * time.Second,
			MinParticipants:        3,
		}
		vr := consensus.NewVotingRound(cfg, reg)
		vr.SetValidatorSet(snap)
		eid := event.EventID("deterministic-event")

		for _, s := range seats {
			_ = vr.RegisterVote(eid, s.key, true)
		}

		rec, err := vr.GetRecord(eid)
		if err != nil {
			t.Fatalf("attempt %d: GetRecord: %v", attempt, err)
		}
		if !rec.Finalized {
			t.Fatalf("attempt %d: expected finalized", attempt)
		}
		if rec.TotalWeight != 300_000 {
			t.Fatalf("attempt %d: expected TotalWeight 300_000, got %d", attempt, rec.TotalWeight)
		}
		if rec.YesWeight != 300_000 {
			t.Fatalf("attempt %d: expected YesWeight 300_000, got %d", attempt, rec.YesWeight)
		}
	}
}

func TestSnapshot_RoundCapturesVersion(t *testing.T) {
	snap := buildSnapshot(t, []struct {
		id    validatorlifecycle.ValidatorID
		key   crypto.AgentID
		stake uint64
	}{
		{"seat-v", "key-ver", 100_000},
	})

	reg := identity.NewRegistry()
	registerAgent(t, reg, "key-ver", 5000, 100_000)
	cfg := &consensus.ConsensusConfig{
		SupermajorityThreshold: 0.667,
		MaxRounds:              10,
		RoundTimeout:           5 * time.Second,
		MinParticipants:        1,
	}
	vr := consensus.NewVotingRound(cfg, reg)
	vr.SetValidatorSet(snap)

	eid := event.EventID("version-check")
	if err := vr.RegisterVote(eid, "key-ver", true); err != nil {
		t.Fatalf("RegisterVote: %v", err)
	}

	rec, err := vr.GetRecord(eid)
	if err != nil {
		t.Fatalf("GetRecord: %v", err)
	}
	if rec.ValidatorSetVersion == 0 {
		t.Fatal("ValidatorSetVersion should be non-zero when snapshot is bound")
	}
	// The snapshot version should match.
	if rec.ValidatorSetVersion != snap.SetVersion() {
		t.Fatalf("expected version %d, got %d", snap.SetVersion(), rec.ValidatorSetVersion)
	}
}

func TestSnapshot_FallbackToRegistry_WhenNoSnapshot(t *testing.T) {
	// When no snapshot is bound, the original registry-based path is used.
	reg := identity.NewRegistry()
	registerAgent(t, reg, "legacy-voter", 5000, 2000)
	vr := consensus.NewVotingRound(nil, reg)
	// No SetValidatorSet call.

	w, err := vr.ComputeWeight("legacy-voter")
	if err != nil {
		t.Fatalf("ComputeWeight: %v", err)
	}
	// 5000 * 2000 / 10000 = 1000
	if w != 1000 {
		t.Fatalf("expected registry weight 1000, got %d", w)
	}
}

func TestSnapshot_NoVersionWhenNoSnapshot(t *testing.T) {
	reg := identity.NewRegistry()
	registerAgent(t, reg, "no-snap-voter", 5000, 2000)
	cfg := &consensus.ConsensusConfig{
		SupermajorityThreshold: 0.667,
		MaxRounds:              10,
		RoundTimeout:           5 * time.Second,
		MinParticipants:        1,
	}
	vr := consensus.NewVotingRound(cfg, reg)

	eid := event.EventID("no-snap-event")
	_ = vr.RegisterVote(eid, "no-snap-voter", true)

	rec, err := vr.GetRecord(eid)
	if err != nil {
		t.Fatalf("GetRecord: %v", err)
	}
	if rec.ValidatorSetVersion != 0 {
		t.Fatalf("expected version 0 when no snapshot, got %d", rec.ValidatorSetVersion)
	}
}

// ---------------------------------------------------------------------------
// Committee-bound consensus tests
// ---------------------------------------------------------------------------

// staticCommittee implements CommitteeSource with a fixed member set.
type staticCommittee struct {
	members map[crypto.AgentID]bool
}

func (sc *staticCommittee) SelectForRound(_ event.EventID) map[crypto.AgentID]bool {
	return sc.members
}

func TestCommittee_OutOfCommitteeVoteRejected(t *testing.T) {
	snap := buildSnapshot(t, []struct {
		id    validatorlifecycle.ValidatorID
		key   crypto.AgentID
		stake uint64
	}{
		{"s1", "key-alice", 100_000},
		{"s2", "key-bob", 200_000},
		{"s3", "key-carol", 150_000},
	})

	reg := identity.NewRegistry()
	registerAgent(t, reg, "key-alice", 5000, 100_000)
	registerAgent(t, reg, "key-bob", 5000, 200_000)
	registerAgent(t, reg, "key-carol", 5000, 150_000)

	cfg := &consensus.ConsensusConfig{
		SupermajorityThreshold: 0.667,
		MaxRounds:              10,
		RoundTimeout:           5 * time.Second,
		MinParticipants:        2,
	}
	vr := consensus.NewVotingRound(cfg, reg)
	vr.SetValidatorSet(snap)

	// Committee includes only alice and bob — not carol.
	vr.SetCommitteeSource(&staticCommittee{
		members: map[crypto.AgentID]bool{
			"key-alice": true,
			"key-bob":   true,
		},
	})

	eid := event.EventID("committee-event")

	// Alice and bob should succeed.
	if err := vr.RegisterVote(eid, "key-alice", true); err != nil {
		t.Fatalf("alice (in committee) should succeed: %v", err)
	}
	if err := vr.RegisterVote(eid, "key-bob", true); err != nil {
		t.Fatalf("bob (in committee) should succeed: %v", err)
	}

	// Carol is in the snapshot but NOT in the committee.
	err := vr.RegisterVote(eid, "key-carol", true)
	if !errors.Is(err, consensus.ErrVoterNotInCommittee) {
		t.Fatalf("carol (out of committee) should get ErrVoterNotInCommittee, got: %v", err)
	}
}

func TestCommittee_NoCommitteeSource_AllVotersAccepted(t *testing.T) {
	snap := buildSnapshot(t, []struct {
		id    validatorlifecycle.ValidatorID
		key   crypto.AgentID
		stake uint64
	}{
		{"s1", "key-x", 100_000},
		{"s2", "key-y", 100_000},
		{"s3", "key-z", 100_000},
	})

	reg := identity.NewRegistry()
	registerAgent(t, reg, "key-x", 5000, 100_000)
	registerAgent(t, reg, "key-y", 5000, 100_000)
	registerAgent(t, reg, "key-z", 5000, 100_000)

	cfg := &consensus.ConsensusConfig{
		SupermajorityThreshold: 0.667,
		MaxRounds:              10,
		RoundTimeout:           5 * time.Second,
		MinParticipants:        3,
	}
	vr := consensus.NewVotingRound(cfg, reg)
	vr.SetValidatorSet(snap)
	// No SetCommitteeSource — all voters accepted.

	eid := event.EventID("no-committee-event")
	for _, key := range []crypto.AgentID{"key-x", "key-y", "key-z"} {
		if err := vr.RegisterVote(eid, key, true); err != nil {
			t.Fatalf("voter %s should succeed without committee source: %v", key, err)
		}
	}

	finalized, _ := vr.IsFinalized(eid)
	if !finalized {
		t.Fatal("expected finalization with 3/3 votes, no committee restriction")
	}
}

func TestCommittee_ThreeNodeTestnet_AllInCommittee(t *testing.T) {
	// Simulate the 3-node testnet: 3 seats, all should be in the committee
	// since 3 ≤ DefaultCommitteePolicy().MaxSize (21).
	snap := buildSnapshot(t, []struct {
		id    validatorlifecycle.ValidatorID
		key   crypto.AgentID
		stake uint64
	}{
		{"node-1", "key-n1", 100_000},
		{"node-2", "key-n2", 100_000},
		{"node-3", "key-n3", 100_000},
	})

	// Use the actual SelectCommittee function (which now uses bounded selection).
	cs := snap.SelectCommittee("some-event")
	if len(cs.Members) != 3 {
		t.Fatalf("3-node testnet: expected all 3 in committee, got %d", len(cs.Members))
	}
	if cs.TotalWeight != 300_000 {
		t.Fatalf("expected total weight 300000, got %d", cs.TotalWeight)
	}
}

func TestCommittee_NilCommitteeReturn_DisablesFiltering(t *testing.T) {
	// A CommitteeSource that returns nil disables filtering for that round.
	snap := buildSnapshot(t, []struct {
		id    validatorlifecycle.ValidatorID
		key   crypto.AgentID
		stake uint64
	}{
		{"s1", "key-a", 100_000},
	})

	reg := identity.NewRegistry()
	registerAgent(t, reg, "key-a", 5000, 100_000)

	cfg := &consensus.ConsensusConfig{
		SupermajorityThreshold: 0.667,
		MaxRounds:              10,
		RoundTimeout:           5 * time.Second,
		MinParticipants:        1,
	}
	vr := consensus.NewVotingRound(cfg, reg)
	vr.SetValidatorSet(snap)

	// Committee source returns nil — filtering disabled.
	vr.SetCommitteeSource(&staticCommittee{members: nil})

	eid := event.EventID("nil-committee-event")
	if err := vr.RegisterVote(eid, "key-a", true); err != nil {
		t.Fatalf("nil committee should disable filtering: %v", err)
	}
}
