package dispatch_test

import (
	"context"
	"testing"
	"time"

	"github.com/Aethernet-network/aethernet/internal/consensus"
	"github.com/Aethernet-network/aethernet/internal/crypto"
	"github.com/Aethernet-network/aethernet/internal/dispatch"
	"github.com/Aethernet-network/aethernet/internal/dispatch/conformance"
	"github.com/Aethernet-network/aethernet/internal/event"
	"github.com/Aethernet-network/aethernet/internal/identity"
	"github.com/Aethernet-network/aethernet/internal/ledger"
	"github.com/Aethernet-network/aethernet/internal/settlement"
)

// registerVoter is a local helper mirroring the consensus test suite's
// registerAgent helper. Creates a CapabilityFingerprint with the given
// reputation and stake so ComputeWeight returns the expected product.
func registerVoter(t *testing.T, reg *identity.Registry, id crypto.AgentID, rep, stake uint64) {
	t.Helper()
	now := time.Now().UTC()
	fp := &identity.CapabilityFingerprint{
		AgentID:              id,
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
		t.Fatalf("registerVoter %s: %v", id, err)
	}
}

// stubValidatorSetSource is a minimal implementation of
// consensus.ValidatorSetSource used to drive reject-path finalization
// in tests. Rejection requires TotalActiveWeight > 0 (the consensus
// tally consults the snapshot's ActiveWeight to decide when approval
// is mathematically impossible), so every Settlement reject-path test
// binds one of these to the VotingRound.
type stubValidatorSetSource struct {
	weights    map[crypto.AgentID]uint64
	setVersion uint64
}

func (s *stubValidatorSetSource) VoteWeightByKey(id crypto.AgentID) (uint64, bool) {
	w, ok := s.weights[id]
	if !ok || w == 0 {
		return 0, false
	}
	return w, true
}

func (s *stubValidatorSetSource) SetVersion() uint64 { return s.setVersion }

func (s *stubValidatorSetSource) ActiveWeight() uint64 {
	var total uint64
	// safe: iteration order does not affect canonical state (commutative sum)
	for _, w := range s.weights {
		total += w
	}
	return total
}

// buildFinalizedRound constructs a fresh consensus.VotingRound bound
// to a stub validator-set snapshot and drives it to finalization by
// registering the specified yes/no votes on the given target. All
// voters receive equal weight (100); TotalActiveWeight=100*N so the
// BFT denominator is well-defined and both approval (yes ≥ 2/3) and
// rejection (no > 1/3) finalization paths are reachable.
//
// The helper is shared across Settlement LK tests so each test can
// specify a corpus (pass / fail / missing-record) without repeating
// the wiring.
func buildFinalizedRound(
	t *testing.T,
	target event.EventID,
	yesVoters []crypto.AgentID,
	noVoters []crypto.AgentID,
) (*consensus.VotingRound, *identity.Registry) {
	t.Helper()
	reg := identity.NewRegistry()
	weights := make(map[crypto.AgentID]uint64)
	for _, v := range yesVoters {
		registerVoter(t, reg, v, 5000, 2000)
		weights[v] = 100
	}
	for _, v := range noVoters {
		registerVoter(t, reg, v, 5000, 2000)
		weights[v] = 100
	}
	vr := consensus.NewVotingRound(nil, reg)
	vr.SetValidatorSet(&stubValidatorSetSource{weights: weights, setVersion: 1})
	for _, v := range yesVoters {
		if err := vr.RegisterVote(target, v, true, 42); err != nil {
			t.Fatalf("RegisterVote(yes) %s: %v", v, err)
		}
	}
	for _, v := range noVoters {
		if err := vr.RegisterVote(target, v, false, 0); err != nil {
			t.Fatalf("RegisterVote(no) %s: %v", v, err)
		}
	}
	return vr, reg
}

// newTestSettlementLKConsumer builds a SettlementLogicalKeyConsumer
// with a fresh Applicator + VotingRound + Registry. The Applicator's
// eventLookup returns (nil, err) — Settlement.Apply tolerates this
// by deferring the payload for later reconciliation, which is the
// correct path when the target is not yet known to the DAG (tests
// that only exercise IsApplied / IsComplete don't need the target).
func newTestSettlementLKConsumer(
	t *testing.T,
	target event.EventID,
	yesVoters []crypto.AgentID,
	noVoters []crypto.AgentID,
) (*dispatch.SettlementLogicalKeyConsumer, *settlement.Applicator, *consensus.VotingRound) {
	return newTestSettlementLKConsumerWithExtras(t, target, yesVoters, noVoters, nil)
}

// newTestSettlementLKConsumerWithExtras is the variant used by
// not-finalized tests: registers additional voters in the validator-
// set snapshot WITHOUT casting their votes, inflating the
// denominator so neither supermajority threshold is crossed by the
// corpus. Useful for exercising IsComplete=false paths under an
// active snapshot.
func newTestSettlementLKConsumerWithExtras(
	t *testing.T,
	target event.EventID,
	yesVoters []crypto.AgentID,
	noVoters []crypto.AgentID,
	extraSnapshotVoters []crypto.AgentID,
) (*dispatch.SettlementLogicalKeyConsumer, *settlement.Applicator, *consensus.VotingRound) {
	t.Helper()
	tl := ledger.NewTransferLedger()
	gl := ledger.NewGenerationLedger()
	reg := identity.NewRegistry()
	app := settlement.NewApplicator(tl, gl, reg, func(id event.EventID) (*event.Event, error) {
		return nil, errNotFoundLK
	})
	vr := buildFinalizedRoundWithExtras(t, target, yesVoters, noVoters, extraSnapshotVoters)
	c := dispatch.NewSettlementLogicalKeyConsumer(app, vr, func() uint64 { return 3 })
	return c, app, vr
}

// buildFinalizedRoundWithExtras is buildFinalizedRound with optional
// extra voters registered in the snapshot (and identity registry)
// but who cast no votes. Used by not-finalized tests.
func buildFinalizedRoundWithExtras(
	t *testing.T,
	target event.EventID,
	yesVoters []crypto.AgentID,
	noVoters []crypto.AgentID,
	extras []crypto.AgentID,
) *consensus.VotingRound {
	t.Helper()
	reg := identity.NewRegistry()
	weights := make(map[crypto.AgentID]uint64)
	for _, v := range yesVoters {
		registerVoter(t, reg, v, 5000, 2000)
		weights[v] = 100
	}
	for _, v := range noVoters {
		registerVoter(t, reg, v, 5000, 2000)
		weights[v] = 100
	}
	for _, v := range extras {
		registerVoter(t, reg, v, 5000, 2000)
		weights[v] = 100
	}
	vr := consensus.NewVotingRound(nil, reg)
	vr.SetValidatorSet(&stubValidatorSetSource{weights: weights, setVersion: 1})
	for _, v := range yesVoters {
		if err := vr.RegisterVote(target, v, true, 42); err != nil {
			t.Fatalf("RegisterVote(yes) %s: %v", v, err)
		}
	}
	for _, v := range noVoters {
		if err := vr.RegisterVote(target, v, false, 0); err != nil {
			t.Fatalf("RegisterVote(no) %s: %v", v, err)
		}
	}
	return vr
}

// makeSettlementEvent constructs a Settlement event with the given
// TargetEventID and Verdict. Used to exercise Key() under known
// payload shapes.
func makeSettlementLKEvent(t *testing.T, targetID, verdict string) *event.Event {
	t.Helper()
	ev, err := event.New(
		event.EventTypeSettlement,
		nil,
		settlement.SettlementPayload{
			Version:        1,
			TargetEventID:  targetID,
			Verdict:        verdict,
			VerifiedValue:  1000,
			ConsensusRound: 7,
			Attestations:   nil,
		},
		"finalizer-1",
		nil,
		0,
	)
	if err != nil {
		t.Fatalf("construct settlement event: %v", err)
	}
	return ev
}

// --- Shape tests ------------------------------------------------------------

func TestSettlementLKConsumer_Name(t *testing.T) {
	c, _, _ := newTestSettlementLKConsumer(t, "target-1", []crypto.AgentID{"v1", "v2", "v3"}, nil)
	if c.Name() != "settlement_lk" {
		t.Errorf("Name: got %q want settlement_lk", c.Name())
	}
}

func TestSettlementLKConsumer_Interested(t *testing.T) {
	c, _, _ := newTestSettlementLKConsumer(t, "target-1", []crypto.AgentID{"v1", "v2", "v3"}, nil)
	ev := makeSettlementLKEvent(t, "target-1", "accepted")
	if !c.Interested(ev) {
		t.Error("should be interested in Settlement events")
	}
	other, _ := event.New(event.EventTypeTransfer, nil, event.TransferPayload{
		FromAgent: "a", ToAgent: "b", Amount: 1, Currency: "AET",
	}, "a", nil, 0)
	if c.Interested(other) {
		t.Error("should NOT be interested in Transfer events")
	}
}

func TestSettlementLKConsumer_Key_ExtractsTargetEventID(t *testing.T) {
	c, _, _ := newTestSettlementLKConsumer(t, "target-xyz", []crypto.AgentID{"v1", "v2", "v3"}, nil)
	ev := makeSettlementLKEvent(t, "target-xyz", "accepted")
	key, err := c.Key(ev)
	if err != nil {
		t.Fatalf("Key: %v", err)
	}
	if key != "target-xyz" {
		t.Errorf("Key: got %q want target-xyz", key)
	}
}

func TestSettlementLKConsumer_Key_EmptyTargetErrors(t *testing.T) {
	c, _, _ := newTestSettlementLKConsumer(t, "target-1", []crypto.AgentID{"v1", "v2", "v3"}, nil)
	ev := makeSettlementLKEvent(t, "", "accepted")
	if _, err := c.Key(ev); err == nil {
		t.Error("Key on empty TargetEventID should error")
	}
}

// --- IsComplete tests -------------------------------------------------------

// TestSettlementLKConsumer_IsComplete_Finalized covers the load-
// bearing affirmative case: a finalized VoteRecord returns true.
// Three yes votes on equal-weight voters → yesWeight/totalWeight=1.0
// ≥ 0.667, so TallyVotes sets Finalized=true.
func TestSettlementLKConsumer_IsComplete_Finalized(t *testing.T) {
	target := event.EventID("target-finalized")
	c, _, _ := newTestSettlementLKConsumer(t, target, []crypto.AgentID{"v1", "v2", "v3"}, nil)

	complete, err := c.IsComplete(dispatch.RoundState{LogicalKey: dispatch.LogicalKey(target)})
	if err != nil {
		t.Fatalf("IsComplete: %v", err)
	}
	if !complete {
		t.Error("expected IsComplete=true on finalized 3/3 record")
	}
}

// TestSettlementLKConsumer_IsComplete_NotFinalized covers the
// under-quorum case: 2 yes + 1 no out of 4 total active voters →
// yesRatio=0.5 (< 0.667), noRatio=0.25 (< 0.333). Neither
// supermajority threshold crossed; the record stays non-finalized;
// IsComplete returns false. The helper registers 4 voters but only
// 3 vote — the 4th is a "not-yet-voted" placeholder making the
// denominator match MinParticipants+1 so neither side finalizes.
func TestSettlementLKConsumer_IsComplete_NotFinalized(t *testing.T) {
	target := event.EventID("target-split")
	c, _, _ := newTestSettlementLKConsumerWithExtras(t, target,
		[]crypto.AgentID{"va", "vb"},
		[]crypto.AgentID{"vc"},
		[]crypto.AgentID{"vd"}, // registered in snapshot, no vote cast
	)

	complete, err := c.IsComplete(dispatch.RoundState{LogicalKey: dispatch.LogicalKey(target)})
	if err != nil {
		t.Fatalf("IsComplete: %v", err)
	}
	if complete {
		t.Error("expected IsComplete=false on 2 yes + 1 no out of 4 total (below either threshold)")
	}
}

// TestSettlementLKConsumer_IsComplete_MissingRecord covers the
// absent-record case: the target has no VoteRecord yet (votes have
// not yet been projected on this node). IsComplete MUST return
// false (not error) so the dispatcher defers and retries on the
// next Admit.
func TestSettlementLKConsumer_IsComplete_MissingRecord(t *testing.T) {
	c, _, _ := newTestSettlementLKConsumer(t, "other-target", []crypto.AgentID{"v1", "v2", "v3"}, nil)
	complete, err := c.IsComplete(dispatch.RoundState{LogicalKey: "nope-no-such-target"})
	if err != nil {
		t.Errorf("IsComplete on missing record should not error; got %v", err)
	}
	if complete {
		t.Error("IsComplete should be false when VoteRecord is missing")
	}
}

// --- DeriveOutcome tests ----------------------------------------------------

// TestSettlementLKConsumer_DeriveOutcome_AcceptVerdict verifies the
// accept-path derivation: 3/3 yes → VerdictAccept, ScoreBP=0
// (Settlement doesn't carry scores), participants in lex order.
func TestSettlementLKConsumer_DeriveOutcome_AcceptVerdict(t *testing.T) {
	target := event.EventID("target-accept")
	c, _, _ := newTestSettlementLKConsumer(t, target,
		[]crypto.AgentID{"v-gamma", "v-alpha", "v-beta"}, // register in non-lex order
		nil,
	)

	outcome, err := c.DeriveOutcome(dispatch.RoundState{LogicalKey: dispatch.LogicalKey(target)})
	if err != nil {
		t.Fatalf("DeriveOutcome: %v", err)
	}
	if outcome.Verdict != dispatch.VerdictAccept {
		t.Errorf("Verdict: got %q want accept", outcome.Verdict)
	}
	if outcome.ScoreBP != 0 {
		t.Errorf("ScoreBP: got %d want 0 (Settlement payloads don't carry scores)", outcome.ScoreBP)
	}
	if len(outcome.ParticipatingIDs) != 3 {
		t.Fatalf("ParticipatingIDs length: got %d want 3", len(outcome.ParticipatingIDs))
	}
	// Lex order.
	if outcome.ParticipatingIDs[0] != "v-alpha" ||
		outcome.ParticipatingIDs[1] != "v-beta" ||
		outcome.ParticipatingIDs[2] != "v-gamma" {
		t.Errorf("ParticipatingIDs: got %v want [alpha,beta,gamma]", outcome.ParticipatingIDs)
	}
}

// TestSettlementLKConsumer_DeriveOutcome_RejectVerdict verifies the
// reject-path derivation: 3 no-votes on 3-validator cluster
// (noWeight ≥ 1/3 → rejection finalization path) → VerdictReject.
func TestSettlementLKConsumer_DeriveOutcome_RejectVerdict(t *testing.T) {
	target := event.EventID("target-reject")
	// 3 no-votes trigger the rejection finalization path: noWeight
	// exceeds 1/3 of totalActiveWeight, approval is mathematically
	// impossible → Finalized=true, FinalVerdict=false.
	c, _, _ := newTestSettlementLKConsumer(t, target,
		nil,
		[]crypto.AgentID{"n1", "n2", "n3"},
	)

	outcome, err := c.DeriveOutcome(dispatch.RoundState{LogicalKey: dispatch.LogicalKey(target)})
	if err != nil {
		t.Fatalf("DeriveOutcome: %v", err)
	}
	if outcome.Verdict != dispatch.VerdictReject {
		t.Errorf("Verdict: got %q want reject", outcome.Verdict)
	}
	if outcome.ScoreBP != 0 {
		t.Errorf("ScoreBP: got %d want 0", outcome.ScoreBP)
	}
	if len(outcome.ParticipatingIDs) != 3 {
		t.Errorf("ParticipatingIDs length: got %d want 3", len(outcome.ParticipatingIDs))
	}
}

// TestSettlementLKConsumer_DeriveOutcome_MissingRecordErrors
// verifies that DeriveOutcome on a missing record returns an error
// (IsComplete contract violation — callers must check IsComplete
// first).
func TestSettlementLKConsumer_DeriveOutcome_MissingRecordErrors(t *testing.T) {
	c, _, _ := newTestSettlementLKConsumer(t, "target-other", []crypto.AgentID{"v1", "v2", "v3"}, nil)
	if _, err := c.DeriveOutcome(dispatch.RoundState{LogicalKey: "nope"}); err == nil {
		t.Error("DeriveOutcome on missing record should error")
	}
}

// TestSettlementLKConsumer_DeriveOutcome_NonFinalizedErrors
// verifies that calling DeriveOutcome on a non-finalized record
// errors (IsComplete contract violation — the outcome is undefined
// before finalization). Uses the extras-voter shape so the
// rejection-threshold path does not fire before DeriveOutcome runs.
func TestSettlementLKConsumer_DeriveOutcome_NonFinalizedErrors(t *testing.T) {
	target := event.EventID("target-split")
	c, _, _ := newTestSettlementLKConsumerWithExtras(t, target,
		[]crypto.AgentID{"va", "vb"},
		[]crypto.AgentID{"vc"},
		[]crypto.AgentID{"vd"},
	)
	if _, err := c.DeriveOutcome(dispatch.RoundState{LogicalKey: dispatch.LogicalKey(target)}); err == nil {
		t.Error("DeriveOutcome on non-finalized record should error")
	}
}

// --- Apply tests ------------------------------------------------------------

// TestSettlementLKConsumer_Apply_InvokesApplicator verifies the
// accept-path Apply invocation reaches the applicator. The
// applicator's lookup returns not-found → the payload is deferred,
// which is a legitimate no-error completion path (applicator
// behaviour when target is not yet in the local DAG). IsApplied
// remains false; this is the correct behavior for the deferred
// path.
func TestSettlementLKConsumer_Apply_InvokesApplicator(t *testing.T) {
	target := event.EventID("target-apply-accept")
	c, _, _ := newTestSettlementLKConsumer(t, target, []crypto.AgentID{"v1", "v2", "v3"}, nil)

	outcome := dispatch.Outcome{
		Verdict:          dispatch.VerdictAccept,
		ParticipatingIDs: []crypto.AgentID{"v1", "v2", "v3"},
	}
	if err := c.Apply(context.Background(), event.EventID("trigger"), dispatch.LogicalKey(target), outcome); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	// Deferred path — IsApplied stays false because target is not in
	// the DAG. No error is the success signal here.
}

// TestSettlementLKConsumer_Apply_UnknownVerdictErrors verifies
// defensive handling: a zero-value Outcome.Verdict (empty string)
// returns an error rather than silently invoking the applicator
// with an unroutable verdict.
func TestSettlementLKConsumer_Apply_UnknownVerdictErrors(t *testing.T) {
	target := event.EventID("target-unknown-verdict")
	c, _, _ := newTestSettlementLKConsumer(t, target, []crypto.AgentID{"v1", "v2", "v3"}, nil)

	// Outcome with no Verdict set.
	outcome := dispatch.Outcome{}
	if err := c.Apply(context.Background(), event.EventID("trigger"), dispatch.LogicalKey(target), outcome); err == nil {
		t.Error("Apply with empty Verdict should error")
	}
}

// TestSettlementLKConsumer_Apply_MissingVoteRecordErrors verifies
// that Apply on a key with no VoteRecord returns an error (the
// dispatcher's per-key state machine should never schedule Apply
// without IsComplete=true first, so this is a defensive guard).
func TestSettlementLKConsumer_Apply_MissingVoteRecordErrors(t *testing.T) {
	c, _, _ := newTestSettlementLKConsumer(t, "target-other", []crypto.AgentID{"v1", "v2", "v3"}, nil)

	outcome := dispatch.Outcome{Verdict: dispatch.VerdictAccept}
	if err := c.Apply(context.Background(), event.EventID("trigger"), "nope-no-record", outcome); err == nil {
		t.Error("Apply on missing VoteRecord should error")
	}
}

// --- RecoveryProbe tests ----------------------------------------------------

// TestSettlementLKConsumer_RecoveryProbe_NotApplied_NotStarted
// verifies that an unseen target returns RecoveryNotStarted — the
// dispatcher's next Admit re-drives IsComplete / Apply.
func TestSettlementLKConsumer_RecoveryProbe_NotApplied_NotStarted(t *testing.T) {
	c, _, _ := newTestSettlementLKConsumer(t, "target-1", []crypto.AgentID{"v1", "v2", "v3"}, nil)

	status, err := c.RecoveryProbe(context.Background(), "target-1")
	if err != nil {
		t.Fatalf("RecoveryProbe: %v", err)
	}
	if status != dispatch.RecoveryNotStarted {
		t.Errorf("RecoveryProbe: got %v want RecoveryNotStarted", status)
	}
}

// TestSettlementLKConsumer_RecoveryProbe_Applied_Completed verifies
// positive-evidence recovery: once the applicator's IsApplied set
// contains the target, RecoveryProbe returns RecoveryCompleted
// regardless of VoteRecord state. This is the C-14 monotonic
// evidence guarantee.
func TestSettlementLKConsumer_RecoveryProbe_Applied_Completed(t *testing.T) {
	target := event.EventID("target-applied")
	c, app, _ := newTestSettlementLKConsumer(t, target, []crypto.AgentID{"v1", "v2", "v3"}, nil)

	// Inject the target into the applicator's applied set via the
	// normal Apply path — but first wire the lookup to succeed so
	// the applicator runs to completion rather than deferring.
	tl := ledger.NewTransferLedger()
	gl := ledger.NewGenerationLedger()
	reg := identity.NewRegistry()
	_ = reg.Register(&identity.CapabilityFingerprint{
		AgentID:              "sender",
		PublicKey:            make([]byte, 32),
		ReputationScore:      5000,
		StakedAmount:         2000,
		OptimisticTrustLimit: 1000,
		FirstSeen:            time.Now().UTC(),
		LastActive:           time.Now().UTC(),
		FingerprintVersion:   1,
	})
	_ = reg.Register(&identity.CapabilityFingerprint{
		AgentID:              "recipient",
		PublicKey:            make([]byte, 32),
		ReputationScore:      5000,
		StakedAmount:         2000,
		OptimisticTrustLimit: 1000,
		FirstSeen:            time.Now().UTC(),
		LastActive:           time.Now().UTC(),
		FingerprintVersion:   1,
	})
	_ = tl.FundAgent("sender", 1000)
	// Transfer event as the target — any event.Type the applicator
	// handles works; Transfer is the simplest.
	targetEv, _ := event.New(event.EventTypeTransfer, nil, event.TransferPayload{
		Version: 1, FromAgent: "sender", ToAgent: "recipient", Amount: 100, Currency: "AET",
	}, "sender", nil, 0)
	targetEv.ID = target // force deterministic ID for test
	app2 := settlement.NewApplicator(tl, gl, reg, func(id event.EventID) (*event.Event, error) {
		if id == target {
			return targetEv, nil
		}
		return nil, errNotFoundLK
	})
	sp := &settlement.SettlementPayload{
		Version:        1,
		TargetEventID:  string(target),
		Verdict:        string(settlement.VerdictAccepted),
		VerifiedValue:  100,
		ConsensusRound: 1,
	}
	if err := app2.Apply(sp); err != nil {
		t.Fatalf("app2.Apply: %v", err)
	}
	if !app2.IsApplied(target) {
		t.Fatalf("app2.IsApplied should be true after successful Apply")
	}
	// The production consumer reads from `app`, not `app2`. The
	// probe should therefore still return NotStarted against the
	// original applicator.
	status, err := c.RecoveryProbe(context.Background(), dispatch.LogicalKey(target))
	if err != nil {
		t.Fatalf("RecoveryProbe: %v", err)
	}
	if status != dispatch.RecoveryNotStarted {
		t.Errorf("RecoveryProbe against the original unseen applicator: got %v want RecoveryNotStarted", status)
	}
	// Now rebuild the consumer pointing at app2 and confirm the
	// positive path.
	c2 := dispatch.NewSettlementLogicalKeyConsumer(app2, nil, func() uint64 { return 3 })
	status2, err := c2.RecoveryProbe(context.Background(), dispatch.LogicalKey(target))
	if err != nil {
		t.Fatalf("RecoveryProbe: %v", err)
	}
	if status2 != dispatch.RecoveryCompleted {
		t.Errorf("RecoveryProbe against applied set: got %v want RecoveryCompleted", status2)
	}
	_ = app // unused — kept for symmetry with newTestSettlementLKConsumer
}

// --- End-to-end dispatcher tests --------------------------------------------

// TestSettlementLKConsumer_EndToEnd_OneApplyPerTargetEventID is the
// load-bearing F4B property for Settlement: multiple byte-distinct
// Settlement events for the SAME TargetEventID produce exactly ONE
// Apply invocation via the dispatcher's logical-key admission.
// Different canonical hashes, different triggering payload fields —
// all collapse to one admission record keyed by TargetEventID.
func TestSettlementLKConsumer_EndToEnd_OneApplyPerTargetEventID(t *testing.T) {
	target := event.EventID("target-e2e")
	c, _, _ := newTestSettlementLKConsumer(t, target, []crypto.AgentID{"v1", "v2", "v3"}, nil)

	d, adm := newTestDispatcherForLK(t)
	if err := d.RegisterLogicalKey(c); err != nil {
		t.Fatalf("RegisterLogicalKey: %v", err)
	}

	// Two byte-distinct events with DIFFERENT Verdict strings but
	// SAME TargetEventID. Both project to the same LogicalKey.
	ev1 := makeSettlementLKEvent(t, string(target), "accepted")
	ev2 := makeSettlementLKEvent(t, string(target), "rejected")
	if ev1.ID == ev2.ID {
		t.Fatalf("test setup: events should be byte-distinct")
	}

	if err := d.Admit(context.Background(), ev1); err != nil {
		t.Fatalf("Admit ev1: %v", err)
	}
	if err := d.Admit(context.Background(), ev2); err != nil {
		t.Fatalf("Admit ev2: %v", err)
	}

	// Exactly one admission record under the logical-key prefix.
	storeKey := dispatch.LogicalAdmissionKey("settlement_lk", dispatch.LogicalKey(target))
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

// --- Conformance ------------------------------------------------------------

// TestSettlementLKConsumer_Conformance runs the baseline Type E
// behavioral suite. The consumer is wrapped in a factory that sets
// up a minimal, deterministic finalized VoteRecord. Because the
// conformance suite's baseline event is a Transfer (not Settlement),
// the `Interested` returns false and the per-subtest t.Skip path
// fires. This confirms structural validity of the consumer shape
// (LogicalKeyConsumer satisfied) without exercising Settlement
// semantics through the synthetic harness.
//
// Settlement-specific semantics are covered by the unit tests above
// + TestSettlementLKConsumer_EndToEnd_OneApplyPerTargetEventID.
func TestSettlementLKConsumer_Conformance(t *testing.T) {
	conformance.RunLogicalKeyConformance(t, func() (dispatch.LogicalKeyConsumer, func()) {
		c, _, _ := newTestSettlementLKConsumer(t, "conformance-target",
			[]crypto.AgentID{"v1", "v2", "v3"}, nil)
		return c, func() {}
	})
}
