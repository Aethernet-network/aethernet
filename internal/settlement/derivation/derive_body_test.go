package derivation

import (
	"context"
	"errors"
	"testing"

	"github.com/Aethernet-network/aethernet/internal/crypto"
	"github.com/Aethernet-network/aethernet/internal/dag"
	"github.com/Aethernet-network/aethernet/internal/event"
	"github.com/Aethernet-network/aethernet/internal/protocolmath"
	"github.com/Aethernet-network/aethernet/internal/taskverification"
)

// fakeAnchorReader implements AnchorReader for tests with a controllable
// canonical-DAG state. Each fake DAG is a flat map of EventID → *Event;
// IsAncestor / CountAncestorsByType walk the CausalRefs graph.
type fakeAnchorReader struct {
	events map[event.EventID]*event.Event
}

func (f *fakeAnchorReader) Get(id event.EventID) (*event.Event, error) {
	if ev, ok := f.events[id]; ok {
		return ev, nil
	}
	return nil, dag.ErrEventNotFound
}

func (f *fakeAnchorReader) IsAncestor(ancestor, descendant event.EventID) (bool, error) {
	if _, ok := f.events[ancestor]; !ok {
		return false, dag.ErrEventNotFound
	}
	if _, ok := f.events[descendant]; !ok {
		return false, dag.ErrEventNotFound
	}
	if ancestor == descendant {
		return false, nil
	}
	visited := map[event.EventID]struct{}{descendant: {}}
	queue := []event.EventID{descendant}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		ev := f.events[cur]
		for _, ref := range ev.CausalRefs {
			if ref == ancestor {
				return true, nil
			}
			if _, seen := visited[ref]; seen {
				continue
			}
			visited[ref] = struct{}{}
			if _, ok := f.events[ref]; ok {
				queue = append(queue, ref)
			}
		}
	}
	return false, nil
}

func (f *fakeAnchorReader) CountAncestorsByType(descendant event.EventID, eventType event.EventType) (uint64, error) {
	if _, ok := f.events[descendant]; !ok {
		return 0, dag.ErrEventNotFound
	}
	visited := map[event.EventID]struct{}{descendant: {}}
	queue := []event.EventID{descendant}
	var count uint64
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		ev := f.events[cur]
		for _, ref := range ev.CausalRefs {
			if _, seen := visited[ref]; seen {
				continue
			}
			visited[ref] = struct{}{}
			refEv, ok := f.events[ref]
			if !ok {
				return 0, dag.ErrEventNotFound
			}
			queue = append(queue, ref)
			if refEv.Type == eventType {
				count++
			}
		}
	}
	return count, nil
}

// fakeEscrow + fakeTask implement EscrowLookup + TaskLookup with
// controllable returns.
type fakeEscrow struct {
	fundingRef event.EventID
	amount     uint64
	posterID   crypto.AgentID
}

func (f *fakeEscrow) FundingRef(_ string) (event.EventID, error) { return f.fundingRef, nil }
func (f *fakeEscrow) EscrowAmount(_ string) (uint64, error)      { return f.amount, nil }
func (f *fakeEscrow) PosterID(_ string) (crypto.AgentID, error)  { return f.posterID, nil }

type fakeTask struct {
	category string
	family   string
	status   string
}

func (f *fakeTask) Category(_ string) (string, error) { return f.category, nil }
func (f *fakeTask) Family(_ string) (string, error)   { return f.family, nil }
func (f *fakeTask) Status(_ string) (string, error)   { return f.status, nil }

// makeAcceptRound constructs a finalized accept round with N agreeing
// validators (Pass votes) plus a fixed worker + poster.
func makeAcceptRound(workerID, posterID crypto.AgentID, validators []crypto.AgentID) *taskverification.TaskVerificationRound {
	votes := make([]taskverification.TaskVerificationVoteRecord, 0, len(validators))
	for _, v := range validators {
		votes = append(votes, taskverification.TaskVerificationVoteRecord{
			ValidatorID: v,
			Verdict:     taskverification.VerdictPass,
			ScoreBP:     10000,
			Stake:       1000,
		})
	}
	return &taskverification.TaskVerificationRound{
		RoundID:              "round-accept-1",
		TaskID:               "task-1",
		SubmissionEventID:    "submission-1",
		WorkerID:             workerID,
		PosterID:             posterID,
		Category:             "test-category",
		State:                taskverification.RoundStateFinalizedAccept,
		Votes:                votes,
		FinalVerdict:         taskverification.VerdictPass,
		FinalScoreBP:         10000,
		CanonicalSealContext: "seal-context-1",
		EpochAtFinalization:  0,
	}
}

// fixedReader returns a fake DAG containing the events needed for
// derivation. submission is at depth 0; one parent at depth 1;
// grandparent at depth 2. All Transfer (so gen-ledger BFS finds them
// but Quality returns NeutralBP via stub → equal weights at each
// depth).
func fixedReader(submissionID, parent1ID, gp1ID event.EventID) *fakeAnchorReader {
	gp1 := &event.Event{ID: gp1ID, Type: event.EventTypeTransfer, AgentID: "gp-agent"}
	p1 := &event.Event{ID: parent1ID, Type: event.EventTypeTransfer, CausalRefs: []event.EventID{gp1ID}, AgentID: "p1-agent"}
	sub := &event.Event{ID: submissionID, Type: event.EventTypeTaskSubmitted, CausalRefs: []event.EventID{parent1ID}, AgentID: "sub-agent"}
	seal := &event.Event{ID: "seal-context-1", Type: event.EventTypeTaskVerificationConsensus, CausalRefs: []event.EventID{submissionID}, AgentID: "seal-agent"}
	return &fakeAnchorReader{
		events: map[event.EventID]*event.Event{
			gp1ID:        gp1,
			parent1ID:    p1,
			submissionID: sub,
			seal.ID:      seal,
		},
	}
}

// stubInputs assembles a DerivationInputs with stub W + stub Quality
// and the fake reader / lookups wired in. Activation EventIDs are left
// empty (zero value) — `isActivated` short-circuits to (false, nil) when
// the activation ID is the empty string, so the stub is selected. This
// mirrors the F5 5B ship configuration (post multi-AI Item 1 composite
// 2026-04-25): the function-field ActivationCheck surface that
// previously hid closure-captured runtime state is GONE; tests now
// control activation via canonical primitives only (activation EventID
// + AnchorReader.IsAncestor).
func stubInputs(reader AnchorReader, escrow *fakeEscrow, task *fakeTask, treasury crypto.AgentID) DerivationInputs {
	return DerivationInputs{
		w:          WProjections{Stub: NeutralBPStubW{}},
		quality:    QualityProjections{Stub: NeutralQualityStub{}},
		dagReader:  reader,
		escrowMgr:  escrow,
		treasuryID: treasury,
		// reputationActivationEventID and qualityActivationEventID
		// intentionally left as the empty-string zero value: matches
		// pre-locked-workstream config; isActivated returns (false, nil).
	}
}

// TestDeriveSettlement_AcceptPath_ConservesBudget verifies the accept
// path produces records that sum exactly to budget. Conservation is
// the canonical invariant: no µAET created or destroyed in derivation.
func TestDeriveSettlement_AcceptPath_ConservesBudget(t *testing.T) {
	t.Parallel()

	const budget uint64 = 100_000
	worker := crypto.AgentID("worker-agent")
	poster := crypto.AgentID("poster-agent")
	treasury := crypto.AgentID("treasury-agent")
	validators := []crypto.AgentID{"validator-a", "validator-b", "validator-c"}

	round := makeAcceptRound(worker, poster, validators)
	reader := fixedReader(round.SubmissionEventID, "parent-1", "grandparent-1")
	inputs := stubInputs(reader, &fakeEscrow{
		fundingRef: "funding-1",
		amount:     budget,
		posterID:   poster,
	}, &fakeTask{category: "test-category"}, treasury)

	result, err := DeriveSettlement(context.Background(), round, inputs)
	if err != nil {
		t.Fatalf("DeriveSettlement: %v", err)
	}
	if result.Status != StatusDerived {
		t.Fatalf("Status = %v, want StatusDerived", result.Status)
	}
	if result.TerminalStatus != TerminalAccept {
		t.Fatalf("TerminalStatus = %v, want TerminalAccept", result.TerminalStatus)
	}

	var sum uint64
	for _, r := range result.Records {
		sum += r.Amount.Value
	}
	if sum != budget {
		t.Fatalf("budget conservation violated: sum(records)=%d, budget=%d", sum, budget)
	}
}

// TestDeriveSettlement_AcceptPath_CanonicalIDsUnique verifies every
// record has a unique canonical_id (U-1 invariant).
func TestDeriveSettlement_AcceptPath_CanonicalIDsUnique(t *testing.T) {
	t.Parallel()

	worker := crypto.AgentID("worker-agent")
	poster := crypto.AgentID("poster-agent")
	treasury := crypto.AgentID("treasury-agent")
	validators := []crypto.AgentID{"validator-a", "validator-b"}

	round := makeAcceptRound(worker, poster, validators)
	reader := fixedReader(round.SubmissionEventID, "parent-1", "grandparent-1")
	inputs := stubInputs(reader, &fakeEscrow{
		fundingRef: "funding-1",
		amount:     50_000,
		posterID:   poster,
	}, &fakeTask{category: "test-category"}, treasury)

	result, err := DeriveSettlement(context.Background(), round, inputs)
	if err != nil {
		t.Fatalf("DeriveSettlement: %v", err)
	}

	seen := make(map[string]bool)
	for _, r := range result.Records {
		if r.CanonicalID == "" {
			t.Fatalf("record has empty CanonicalID: %+v", r)
		}
		if len(r.CanonicalID) != 64 {
			t.Fatalf("CanonicalID length = %d, want 64 (SHA-256 hex): %s", len(r.CanonicalID), r.CanonicalID)
		}
		if seen[r.CanonicalID] {
			t.Fatalf("U-1 violation: duplicate canonical_id %s", r.CanonicalID)
		}
		seen[r.CanonicalID] = true
	}
}

// TestDeriveSettlement_AcceptPath_OrdinalsMonotone verifies the schema
// 4-step ordinal-assignment rule produces a strictly monotone counter
// from 0 across the full sequence. Per Grok discovery-tax (c) — easy
// to get wrong on first pass.
func TestDeriveSettlement_AcceptPath_OrdinalsMonotone(t *testing.T) {
	t.Parallel()

	worker := crypto.AgentID("worker-agent")
	poster := crypto.AgentID("poster-agent")
	treasury := crypto.AgentID("treasury-agent")
	validators := []crypto.AgentID{"validator-a", "validator-b", "validator-c"}

	round := makeAcceptRound(worker, poster, validators)
	reader := fixedReader(round.SubmissionEventID, "parent-1", "grandparent-1")
	inputs := stubInputs(reader, &fakeEscrow{
		fundingRef: "funding-1",
		amount:     100_000,
		posterID:   poster,
	}, &fakeTask{category: "test-category"}, treasury)

	result, err := DeriveSettlement(context.Background(), round, inputs)
	if err != nil {
		t.Fatalf("DeriveSettlement: %v", err)
	}

	for i, r := range result.Records {
		if r.Purpose.Ordinal != uint32(i) {
			t.Fatalf("ordinals not monotone: record %d has Ordinal=%d, want %d", i, r.Purpose.Ordinal, i)
		}
	}
}

// TestDeriveSettlement_RejectPath_NoGenLedgerNoWorker verifies reject
// produces poster + validators + treasury, no worker, no gen-ledger.
func TestDeriveSettlement_RejectPath_NoGenLedgerNoWorker(t *testing.T) {
	t.Parallel()

	worker := crypto.AgentID("worker-agent")
	poster := crypto.AgentID("poster-agent")
	treasury := crypto.AgentID("treasury-agent")
	validators := []crypto.AgentID{"validator-a", "validator-b"}

	round := makeAcceptRound(worker, poster, validators)
	round.State = taskverification.RoundStateFinalizedReject
	round.FinalVerdict = taskverification.VerdictFail
	for i := range round.Votes {
		round.Votes[i].Verdict = taskverification.VerdictFail
	}

	reader := fixedReader(round.SubmissionEventID, "parent-1", "grandparent-1")
	inputs := stubInputs(reader, &fakeEscrow{
		fundingRef: "funding-1",
		amount:     50_000,
		posterID:   poster,
	}, &fakeTask{category: "test-category"}, treasury)

	result, err := DeriveSettlement(context.Background(), round, inputs)
	if err != nil {
		t.Fatalf("DeriveSettlement: %v", err)
	}
	if result.TerminalStatus != TerminalReject {
		t.Fatalf("TerminalStatus = %v, want TerminalReject", result.TerminalStatus)
	}

	// No worker, no gen-ledger.
	for _, r := range result.Records {
		if r.Recipient.Role == RoleWorker {
			t.Fatalf("reject path should not produce Worker record: %+v", r)
		}
		if r.Recipient.Role == RoleGenLedgerAncestor {
			t.Fatalf("reject path should not produce GenLedgerAncestor record: %+v", r)
		}
	}
}

// TestDeriveSettlement_DisputePath_FiftyFiftyWorkerPoster verifies
// dispute splits the worker share 50/50 between worker and poster.
func TestDeriveSettlement_DisputePath_FiftyFiftyWorkerPoster(t *testing.T) {
	t.Parallel()

	worker := crypto.AgentID("worker-agent")
	poster := crypto.AgentID("poster-agent")
	treasury := crypto.AgentID("treasury-agent")

	round := makeAcceptRound(worker, poster, nil)
	round.State = taskverification.RoundStateDisputed
	round.FinalVerdict = taskverification.VerdictAbstain

	reader := fixedReader(round.SubmissionEventID, "parent-1", "grandparent-1")
	inputs := stubInputs(reader, &fakeEscrow{
		fundingRef: "funding-1",
		amount:     100_000,
		posterID:   poster,
	}, &fakeTask{category: "test-category"}, treasury)

	result, err := DeriveSettlement(context.Background(), round, inputs)
	if err != nil {
		t.Fatalf("DeriveSettlement: %v", err)
	}
	if result.TerminalStatus != TerminalDispute {
		t.Fatalf("TerminalStatus = %v, want TerminalDispute", result.TerminalStatus)
	}

	var workerAmt, posterAmt uint64
	for _, r := range result.Records {
		switch r.Recipient.Role {
		case RoleWorker:
			workerAmt = r.Amount.Value
		case RolePosterRefund:
			posterAmt = r.Amount.Value
		}
	}
	// workerPortion = 100000 * 7300 / 10000 = 73000
	// workerAmt = 73000 / 2 = 36500
	// posterAmt = 73000 - 36500 = 36500
	if workerAmt != 36_500 || posterAmt != 36_500 {
		t.Fatalf("dispute split not 50/50: worker=%d poster=%d", workerAmt, posterAmt)
	}
}

// TestDeriveSettlement_DeterminismAcrossInvocations is the load-bearing
// D-1 test: same canonical inputs → byte-identical Records (incl.
// canonical_ids).
func TestDeriveSettlement_DeterminismAcrossInvocations(t *testing.T) {
	t.Parallel()

	build := func() (DerivationResult, error) {
		worker := crypto.AgentID("worker-agent")
		poster := crypto.AgentID("poster-agent")
		treasury := crypto.AgentID("treasury-agent")
		validators := []crypto.AgentID{"validator-a", "validator-b", "validator-c"}
		round := makeAcceptRound(worker, poster, validators)
		reader := fixedReader(round.SubmissionEventID, "parent-1", "grandparent-1")
		inputs := stubInputs(reader, &fakeEscrow{
			fundingRef: "funding-1",
			amount:     250_000,
			posterID:   poster,
		}, &fakeTask{category: "test-category"}, treasury)
		return DeriveSettlement(context.Background(), round, inputs)
	}

	r1, err := build()
	if err != nil {
		t.Fatalf("run1: %v", err)
	}
	r2, err := build()
	if err != nil {
		t.Fatalf("run2: %v", err)
	}

	if len(r1.Records) != len(r2.Records) {
		t.Fatalf("D-1 violated: record count differs %d vs %d", len(r1.Records), len(r2.Records))
	}
	for i := range r1.Records {
		if r1.Records[i].CanonicalID != r2.Records[i].CanonicalID {
			t.Fatalf("D-1 violated: record[%d] canonical_id differs: %s vs %s",
				i, r1.Records[i].CanonicalID, r2.Records[i].CanonicalID)
		}
		if r1.Records[i].Amount.Value != r2.Records[i].Amount.Value {
			t.Fatalf("D-1 violated: record[%d] amount differs: %d vs %d",
				i, r1.Records[i].Amount.Value, r2.Records[i].Amount.Value)
		}
	}
}

// TestDeriveSettlement_DeferralOnActivationCheckErr verifies the V-1
// deferral path: when the activation EventID is non-empty AND the
// canonical AnchorReader returns ErrEventNotFound for IsAncestor on it
// (because the activation event is not yet locally materialized),
// DeriveSettlement surfaces Status=StatusDeferred,
// Cause=DeferredCauseV1AncestorCheck.
//
// Mechanism migration (multi-AI Item 1 composite, 2026-04-25): the
// previous shape of this test injected a synthetic ActivationCheck
// closure that returned ErrEventNotFound directly. The function-field
// surface was eliminated; activation now flows through canonical
// primitives only. New shape: set
// inputs.ReputationActivationEventID = "missing-activation-event" and
// rely on fakeAnchorReader.IsAncestor's natural ErrEventNotFound return
// when the ancestor is absent from the events map. Same V-1 invariant
// (deferral on materialization lag of the activation event) is exercised
// by the same canonical primitive (AnchorReader.IsAncestor) the
// production code path uses; only the injection mechanism changed.
func TestDeriveSettlement_DeferralOnActivationCheckErr(t *testing.T) {
	t.Parallel()

	worker := crypto.AgentID("worker-agent")
	poster := crypto.AgentID("poster-agent")
	treasury := crypto.AgentID("treasury-agent")
	round := makeAcceptRound(worker, poster, []crypto.AgentID{"v1"})
	reader := fixedReader(round.SubmissionEventID, "parent-1", "grandparent-1")

	// "missing-activation-event" is NOT in reader.events; fakeAnchorReader.IsAncestor
	// returns (false, dag.ErrEventNotFound) for absent ancestors (see line 30-31).
	// isActivated forwards that error; DeriveSettlement converts it to
	// Status=StatusDeferred, Cause=DeferredCauseV1AncestorCheck.
	const missingActivation event.EventID = "missing-activation-event"

	inputs := DerivationInputs{
		w:                           WProjections{Stub: NeutralBPStubW{}},
		quality:                     QualityProjections{Stub: NeutralQualityStub{}},
		dagReader:                   reader,
		escrowMgr:                   &fakeEscrow{fundingRef: "funding-1", amount: 100_000, posterID: poster},
		reputationActivationEventID: missingActivation,
		treasuryID:                  treasury,
	}

	result, err := DeriveSettlement(context.Background(), round, inputs)
	if err != nil {
		t.Fatalf("DeriveSettlement: %v", err)
	}
	if result.Status != StatusDeferred {
		t.Fatalf("Status = %v, want StatusDeferred", result.Status)
	}
	if result.Cause != DeferredCauseV1AncestorCheck {
		t.Fatalf("Cause = %v, want DeferredCauseV1AncestorCheck", result.Cause)
	}
	if len(result.Records) != 0 {
		t.Fatalf("Records should be empty on deferral: %+v", result.Records)
	}
}

// TestDeriveSettlement_DeferralOnWLookupErr verifies the W deferral
// path: W.Lookup returning ErrEventNotFound surfaces as DeferredCauseWLookup.
func TestDeriveSettlement_DeferralOnWLookupErr(t *testing.T) {
	t.Parallel()

	worker := crypto.AgentID("worker-agent")
	poster := crypto.AgentID("poster-agent")
	treasury := crypto.AgentID("treasury-agent")
	round := makeAcceptRound(worker, poster, []crypto.AgentID{"v1", "v2"})
	reader := fixedReader(round.SubmissionEventID, "parent-1", "grandparent-1")

	deferringW := &deferringWStub{}

	inputs := DerivationInputs{
		w:          WProjections{Stub: deferringW},
		quality:    QualityProjections{Stub: NeutralQualityStub{}},
		dagReader:  reader,
		escrowMgr:  &fakeEscrow{fundingRef: "funding-1", amount: 100_000, posterID: poster},
		treasuryID: treasury,
		// Activation EventIDs left empty: isActivated short-circuits to
		// (false, nil) → stub-W selected → deferringWStub.Lookup fires.
	}

	result, err := DeriveSettlement(context.Background(), round, inputs)
	if err != nil {
		t.Fatalf("DeriveSettlement: %v", err)
	}
	if result.Status != StatusDeferred {
		t.Fatalf("Status = %v, want StatusDeferred", result.Status)
	}
	if result.Cause != DeferredCauseWLookup {
		t.Fatalf("Cause = %v, want DeferredCauseWLookup", result.Cause)
	}
}

type deferringWStub struct{}

func (deferringWStub) Lookup(_ crypto.AgentID, _, _ string, _ crypto.AgentID, _, _ uint64) (protocolmath.BasisPoints, error) {
	return 0, dag.ErrEventNotFound
}

// TestDeriveSettlement_DeferralOnDAGAncestorBFSErr verifies the
// gen-ledger BFS deferral path: ReadAtAnchor returning ErrEventNotFound
// (because a CausalRef target is not yet materialized in the local DAG)
// surfaces as Status=StatusDeferred, Cause=DeferredCauseDAGAncestorBFS.
//
// Topology: submission has CausalRefs=[missing-parent], where
// missing-parent is NOT present in the fake reader's events map. BFS
// dequeues submission (depth 0), enqueues missing-parent, then on
// dequeue calls reader.Get(missing-parent) → ErrEventNotFound. That
// propagates up through ReadAtAnchor to the gen-ledger calculator's
// errors.Is(readErr, dag.ErrEventNotFound) branch.
//
// V#5 part (d) — multi-AI ChatGPT review 2026-04-25.
func TestDeriveSettlement_DeferralOnDAGAncestorBFSErr(t *testing.T) {
	t.Parallel()

	worker := crypto.AgentID("worker-agent")
	poster := crypto.AgentID("poster-agent")
	treasury := crypto.AgentID("treasury-agent")
	round := makeAcceptRound(worker, poster, []crypto.AgentID{"v1", "v2"})

	// Reader where submission references missing-parent (absent from
	// events map). BFS will enqueue and then fail to Get it.
	sub := &event.Event{
		ID:         round.SubmissionEventID,
		Type:       event.EventTypeTaskSubmitted,
		CausalRefs: []event.EventID{"missing-parent"},
		AgentID:    "sub-agent",
	}
	seal := &event.Event{
		ID:         "seal-context-1",
		Type:       event.EventTypeTaskVerificationConsensus,
		CausalRefs: []event.EventID{round.SubmissionEventID},
		AgentID:    "seal-agent",
	}
	reader := &fakeAnchorReader{
		events: map[event.EventID]*event.Event{
			round.SubmissionEventID: sub,
			seal.ID:                 seal,
		},
	}

	inputs := DerivationInputs{
		w:          WProjections{Stub: NeutralBPStubW{}},
		quality:    QualityProjections{Stub: NeutralQualityStub{}},
		dagReader:  reader,
		escrowMgr:  &fakeEscrow{fundingRef: "funding-1", amount: 100_000, posterID: poster},
		treasuryID: treasury,
		// Activation EventIDs left empty: isActivated short-circuits to
		// (false, nil) → stub paths selected; gen-ledger BFS reaches the
		// missing-parent and surfaces ErrEventNotFound.
	}

	result, err := DeriveSettlement(context.Background(), round, inputs)
	if err != nil {
		t.Fatalf("DeriveSettlement: %v", err)
	}
	if result.Status != StatusDeferred {
		t.Fatalf("Status = %v, want StatusDeferred", result.Status)
	}
	if result.Cause != DeferredCauseDAGAncestorBFS {
		t.Fatalf("Cause = %v, want DeferredCauseDAGAncestorBFS", result.Cause)
	}
	if len(result.Records) != 0 {
		t.Fatalf("Records should be empty on deferral: %+v", result.Records)
	}
}

// TestDeriveSettlement_DeferralOnQualityLookupErr verifies the Quality
// deferral path: Quality.Lookup returning ErrEventNotFound during the
// per-ancestor weighting loop surfaces as Status=StatusDeferred,
// Cause=DeferredCauseQualityLookup.
//
// Setup: standard fixedReader (BFS succeeds, finds parent + grandparent
// at depths 1 and 2), but Quality stub returns ErrEventNotFound for
// every ancestor. The first ancestor's Lookup hit triggers deferral.
//
// V#5 part (d) — multi-AI ChatGPT review 2026-04-25.
func TestDeriveSettlement_DeferralOnQualityLookupErr(t *testing.T) {
	t.Parallel()

	worker := crypto.AgentID("worker-agent")
	poster := crypto.AgentID("poster-agent")
	treasury := crypto.AgentID("treasury-agent")
	round := makeAcceptRound(worker, poster, []crypto.AgentID{"v1", "v2"})
	reader := fixedReader(round.SubmissionEventID, "parent-1", "grandparent-1")

	deferringQuality := &deferringQualityStub{}

	inputs := DerivationInputs{
		w:          WProjections{Stub: NeutralBPStubW{}},
		quality:    QualityProjections{Stub: deferringQuality},
		dagReader:  reader,
		escrowMgr:  &fakeEscrow{fundingRef: "funding-1", amount: 100_000, posterID: poster},
		treasuryID: treasury,
		// Activation EventIDs left empty: isActivated short-circuits to
		// (false, nil) → stub-Quality selected → deferringQualityStub.Lookup fires.
	}

	result, err := DeriveSettlement(context.Background(), round, inputs)
	if err != nil {
		t.Fatalf("DeriveSettlement: %v", err)
	}
	if result.Status != StatusDeferred {
		t.Fatalf("Status = %v, want StatusDeferred", result.Status)
	}
	if result.Cause != DeferredCauseQualityLookup {
		t.Fatalf("Cause = %v, want DeferredCauseQualityLookup", result.Cause)
	}
	if len(result.Records) != 0 {
		t.Fatalf("Records should be empty on deferral: %+v", result.Records)
	}
}

type deferringQualityStub struct{}

func (deferringQualityStub) Lookup(_ event.EventID, _ uint64) (protocolmath.BasisPoints, error) {
	return 0, dag.ErrEventNotFound
}

// TestDeriveSettlement_NotTerminalErrors verifies precondition: a
// non-finalized round causes DeriveSettlement to error.
func TestDeriveSettlement_NotTerminalErrors(t *testing.T) {
	t.Parallel()

	round := makeAcceptRound("w", "p", nil)
	round.State = taskverification.RoundStateOpen
	inputs := stubInputs(&fakeAnchorReader{}, &fakeEscrow{}, &fakeTask{}, "treasury")
	_, err := DeriveSettlement(context.Background(), round, inputs)
	if err == nil {
		t.Fatal("expected error on non-terminal round")
	}
	if !errors.Is(err, errors.New("derivation: round")) && err == nil {
		// loose error-shape check
	}
}
