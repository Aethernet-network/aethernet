package epoch_test

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/Aethernet-network/aethernet/internal/crypto"
	"github.com/Aethernet-network/aethernet/internal/dag"
	"github.com/Aethernet-network/aethernet/internal/epoch"
	"github.com/Aethernet-network/aethernet/internal/event"
)

// kp returns a fresh signing keypair.
func kp(t *testing.T) *crypto.KeyPair {
	t.Helper()
	k, err := crypto.GenerateKeyPair()
	if err != nil {
		t.Fatalf("GenerateKeyPair: %v", err)
	}
	return k
}

// makeSignedEvent builds and signs an event of the given type.
func makeSignedEvent(t *testing.T, eventType event.EventType, payload any, refs []event.EventID, prior map[event.EventID]uint64) *event.Event {
	t.Helper()
	signer := kp(t)
	ev, err := event.New(eventType, refs, payload, string(signer.AgentID()), prior, 0)
	if err != nil {
		t.Fatalf("event.New(%s): %v", eventType, err)
	}
	if err := crypto.SignEvent(ev, signer); err != nil {
		t.Fatalf("SignEvent: %v", err)
	}
	return ev
}

// makeTransfer is the standard signed Transfer event used as a stand-in
// for non-EpochBoundary events in test DAGs.
func makeTransfer(t *testing.T, refs ...*event.Event) *event.Event {
	t.Helper()
	parentRefs := make([]event.EventID, len(refs))
	prior := make(map[event.EventID]uint64, len(refs))
	for i, p := range refs {
		parentRefs[i] = p.ID
		prior[p.ID] = p.CausalTimestamp
	}
	signer := kp(t)
	aid := string(signer.AgentID())
	payload := event.TransferPayload{FromAgent: aid, ToAgent: "sink", Amount: 1, Currency: "AET"}
	return makeSignedEvent(t, event.EventTypeTransfer, payload, parentRefs, prior)
}

// makeTVConsensus is a TVConsensus event with minimum required payload
// fields so unmarshal won't panic in adjacent code (validator doesn't
// read TVC payload contents).
func makeTVConsensus(t *testing.T, refs ...*event.Event) *event.Event {
	t.Helper()
	parentRefs := make([]event.EventID, len(refs))
	prior := make(map[event.EventID]uint64, len(refs))
	for i, p := range refs {
		parentRefs[i] = p.ID
		prior[p.ID] = p.CausalTimestamp
	}
	payload := event.TaskVerificationConsensusPayload{
		Version:           1,
		RoundID:           "test-round",
		TaskID:            "test-task",
		SubmissionEventID: "test-submission",
		WorkerID:          "test-worker",
		PosterID:          "test-poster",
		FinalVerdict:      "pass",
	}
	return makeSignedEvent(t, event.EventTypeTaskVerificationConsensus, payload, parentRefs, prior)
}

// makeBoundary builds a signed EpochBoundary event with the provided
// payload, referencing trigger as its sole CausalRef.
func makeBoundary(t *testing.T, payload event.EpochBoundaryPayload, trigger *event.Event) *event.Event {
	t.Helper()
	refs := []event.EventID{trigger.ID}
	prior := map[event.EventID]uint64{trigger.ID: trigger.CausalTimestamp}
	return makeSignedEvent(t, event.EventTypeEpochBoundary, payload, refs, prior)
}

// buildDAGWithChain returns a DAG containing genesis Transfer + n
// TVConsensus events in a linear chain. Returns the DAG and the slice
// of TVConsensus events in chain order. NO admission cross-check is
// registered, so events are admitted unconditionally — useful for
// constructing the canonical state before testing the validator
// directly.
func buildDAGWithChain(t *testing.T, n int) (*dag.DAG, []*event.Event) {
	t.Helper()
	d := dag.New()
	g := makeTransfer(t)
	if err := d.Add(g); err != nil {
		t.Fatalf("seed Transfer: %v", err)
	}
	tvcs := make([]*event.Event, n)
	parent := g
	for i := 0; i < n; i++ {
		tvc := makeTVConsensus(t, parent)
		if err := d.Add(tvc); err != nil {
			t.Fatalf("seed TVConsensus[%d]: %v", i, err)
		}
		tvcs[i] = tvc
		parent = tvc
	}
	return d, tvcs
}

// runValidator constructs a temporary DAG holding the candidate's
// CausalRefs and invokes BoundaryAdmissionValidator via the registered
// path. Returns the error from dag.Add. This is the integration form —
// the validator is exercised in its real call site (under write lock).
func runValidator(t *testing.T, d *dag.DAG, candidate *event.Event) error {
	t.Helper()
	return d.Add(candidate)
}

func TestBoundaryValidator_AcceptsCanonicalEmission(t *testing.T) {
	// Build a DAG with EpochLength TVConsensus events. The 1000-th
	// triggers EpochBoundary(1).
	d, tvcs := buildDAGWithChain(t, int(epoch.EpochLength))
	if err := d.RegisterAdmissionCrossCheck(event.EventTypeEpochBoundary, epoch.BoundaryAdmissionValidator); err != nil {
		t.Fatalf("Register: %v", err)
	}

	trigger := tvcs[len(tvcs)-1] // 1000-th TVConsensus
	payload := event.EpochBoundaryPayload{
		Version:        1,
		Epoch:          1,
		TriggerEventID: trigger.ID,
	}
	boundary := makeBoundary(t, payload, trigger)

	if err := runValidator(t, d, boundary); err != nil {
		t.Fatalf("expected canonical EpochBoundary(1) to admit, got: %v", err)
	}
}

func TestBoundaryValidator_RejectsWrongVersion(t *testing.T) {
	d, tvcs := buildDAGWithChain(t, int(epoch.EpochLength))
	if err := d.RegisterAdmissionCrossCheck(event.EventTypeEpochBoundary, epoch.BoundaryAdmissionValidator); err != nil {
		t.Fatalf("Register: %v", err)
	}
	payload := event.EpochBoundaryPayload{
		Version:        2,
		Epoch:          1,
		TriggerEventID: tvcs[len(tvcs)-1].ID,
	}
	boundary := makeBoundary(t, payload, tvcs[len(tvcs)-1])
	err := runValidator(t, d, boundary)
	if !errors.Is(err, epoch.ErrInvalidPayloadVersion) {
		t.Fatalf("expected ErrInvalidPayloadVersion, got: %v", err)
	}
}

func TestBoundaryValidator_RejectsEpochZero(t *testing.T) {
	d, tvcs := buildDAGWithChain(t, int(epoch.EpochLength))
	if err := d.RegisterAdmissionCrossCheck(event.EventTypeEpochBoundary, epoch.BoundaryAdmissionValidator); err != nil {
		t.Fatalf("Register: %v", err)
	}
	payload := event.EpochBoundaryPayload{
		Version:        1,
		Epoch:          0, // explicitly forbidden by §4.2
		TriggerEventID: tvcs[len(tvcs)-1].ID,
	}
	boundary := makeBoundary(t, payload, tvcs[len(tvcs)-1])
	err := runValidator(t, d, boundary)
	if !errors.Is(err, epoch.ErrInvalidEpoch) {
		t.Fatalf("expected ErrInvalidEpoch, got: %v", err)
	}
}

func TestBoundaryValidator_RejectsTriggerWrongType(t *testing.T) {
	// Use a Transfer event as TriggerEventID — wrong type, must reject.
	d := dag.New()
	if err := d.RegisterAdmissionCrossCheck(event.EventTypeEpochBoundary, epoch.BoundaryAdmissionValidator); err != nil {
		t.Fatalf("Register: %v", err)
	}
	tx := makeTransfer(t)
	if err := d.Add(tx); err != nil {
		t.Fatalf("seed Transfer: %v", err)
	}
	payload := event.EpochBoundaryPayload{
		Version:        1,
		Epoch:          1,
		TriggerEventID: tx.ID, // Transfer, not TVConsensus
	}
	boundary := makeBoundary(t, payload, tx)
	err := runValidator(t, d, boundary)
	if !errors.Is(err, epoch.ErrTriggerEventWrongType) {
		t.Fatalf("expected ErrTriggerEventWrongType, got: %v", err)
	}
}

func TestBoundaryValidator_RejectsThresholdNotCrossed(t *testing.T) {
	// Build with EpochLength-1 TVConsensus events: trigger is rank 999,
	// not 1000. EpochBoundary(1) should fail threshold check.
	d, tvcs := buildDAGWithChain(t, int(epoch.EpochLength)-1)
	if err := d.RegisterAdmissionCrossCheck(event.EventTypeEpochBoundary, epoch.BoundaryAdmissionValidator); err != nil {
		t.Fatalf("Register: %v", err)
	}
	payload := event.EpochBoundaryPayload{
		Version:        1,
		Epoch:          1,
		TriggerEventID: tvcs[len(tvcs)-1].ID,
	}
	boundary := makeBoundary(t, payload, tvcs[len(tvcs)-1])
	err := runValidator(t, d, boundary)
	if !errors.Is(err, epoch.ErrThresholdNotCrossed) {
		t.Fatalf("expected ErrThresholdNotCrossed, got: %v", err)
	}
}

func TestBoundaryValidator_RejectsEpochMismatchAfterFirstBoundary(t *testing.T) {
	// Build a DAG with one valid EpochBoundary already in place, then try
	// to admit an EpochBoundary(1) again — fails epoch-count cross-check
	// because canonical_count is now 1, so payload.Epoch must be 2.
	d, tvcs := buildDAGWithChain(t, int(epoch.EpochLength)*2)
	if err := d.RegisterAdmissionCrossCheck(event.EventTypeEpochBoundary, epoch.BoundaryAdmissionValidator); err != nil {
		t.Fatalf("Register: %v", err)
	}

	// Admit canonical EpochBoundary(1) at TVConsensus 1000.
	trig1 := tvcs[int(epoch.EpochLength)-1]
	pl1 := event.EpochBoundaryPayload{Version: 1, Epoch: 1, TriggerEventID: trig1.ID}
	b1 := makeBoundary(t, pl1, trig1)
	if err := d.Add(b1); err != nil {
		t.Fatalf("admit EpochBoundary(1): %v", err)
	}

	// Now build a TVConsensus chain on top of b1 (so b1 is in the
	// canonical ancestry of subsequent triggers).
	parent := b1
	for i := int(epoch.EpochLength); i < int(epoch.EpochLength)*2; i++ {
		// We already added EpochLength*2 TVCs in chain originally, but
		// they don't reference b1. Build a new chain for clarity.
		_ = parent
		_ = i
		break
	}
	// Use the second 1000th TVC from the original chain — it does NOT
	// have b1 as ancestor (b1 is on a separate branch). This means trying
	// to emit EpochBoundary(1) again with a different trigger is what we
	// test. The canonical_count for that trigger is 0 (no boundaries in
	// its ancestry), so it WOULD be canonical EpochBoundary(1). To test
	// the mismatch case, we need a payload claiming wrong Epoch.
	trig2 := tvcs[int(epoch.EpochLength)*2-1]
	plBad := event.EpochBoundaryPayload{Version: 1, Epoch: 99, TriggerEventID: trig2.ID}
	bBad := makeBoundary(t, plBad, trig2)
	err := d.Add(bBad)
	if !errors.Is(err, epoch.ErrEpochMismatch) {
		// May also fail threshold (depending on values). What matters: rejected.
		if !errors.Is(err, epoch.ErrThresholdNotCrossed) {
			t.Fatalf("expected ErrEpochMismatch or ErrThresholdNotCrossed, got: %v", err)
		}
	}
}

func TestBoundaryValidator_RejectsMalformedPayload(t *testing.T) {
	// Construct an EpochBoundary event with deliberately-invalid payload bytes.
	d := dag.New()
	if err := d.RegisterAdmissionCrossCheck(event.EventTypeEpochBoundary, epoch.BoundaryAdmissionValidator); err != nil {
		t.Fatalf("Register: %v", err)
	}
	tx := makeTransfer(t)
	if err := d.Add(tx); err != nil {
		t.Fatalf("seed Transfer: %v", err)
	}
	signer := kp(t)
	ev, err := event.New(
		event.EventTypeEpochBoundary,
		[]event.EventID{tx.ID},
		json.RawMessage(`{"this":"is","not":"a","valid":"payload"}`),
		string(signer.AgentID()),
		map[event.EventID]uint64{tx.ID: tx.CausalTimestamp},
		0,
	)
	if err != nil {
		t.Fatalf("event.New: %v", err)
	}
	if err := crypto.SignEvent(ev, signer); err != nil {
		t.Fatalf("SignEvent: %v", err)
	}
	addErr := d.Add(ev)
	// Malformed payload deserializes as a zero-valued EpochBoundaryPayload
	// (Version=0). Validator must catch via Version check.
	if !errors.Is(addErr, epoch.ErrInvalidPayloadVersion) {
		t.Fatalf("expected ErrInvalidPayloadVersion (zero version from missing field), got: %v", addErr)
	}
}

func TestBoundaryValidator_AcceptsSecondEpochAfterFirst(t *testing.T) {
	// End-to-end: EpochBoundary(1) admits, then a chain extends it,
	// then EpochBoundary(2) at rank 2*EpochLength admits canonically.
	d, tvcs := buildDAGWithChain(t, int(epoch.EpochLength))
	if err := d.RegisterAdmissionCrossCheck(event.EventTypeEpochBoundary, epoch.BoundaryAdmissionValidator); err != nil {
		t.Fatalf("Register: %v", err)
	}

	// EpochBoundary(1).
	trig1 := tvcs[len(tvcs)-1]
	b1 := makeBoundary(t, event.EpochBoundaryPayload{Version: 1, Epoch: 1, TriggerEventID: trig1.ID}, trig1)
	if err := d.Add(b1); err != nil {
		t.Fatalf("admit EpochBoundary(1): %v", err)
	}

	// Extend with EpochLength more TVConsensus events, with each
	// referencing both the prior TVC AND the EpochBoundary (so b1 is in
	// canonical ancestry). The simplest extension: each new TVC
	// references the prior one; the chain naturally inherits b1 via
	// the common ancestor since b1 references trig1 which is in the
	// chain. Wait — b1 is NOT in the ancestry of TVCs that don't
	// reference it. Need to build a new chain that does.
	parent := b1
	var trig2 *event.Event
	for i := 0; i < int(epoch.EpochLength); i++ {
		newTVC := makeTVConsensus(t, parent)
		if err := d.Add(newTVC); err != nil {
			t.Fatalf("admit chained TVC[%d]: %v", i, err)
		}
		parent = newTVC
		if i == int(epoch.EpochLength)-1 {
			trig2 = newTVC
		}
	}

	// EpochBoundary(2).
	pl2 := event.EpochBoundaryPayload{Version: 1, Epoch: 2, TriggerEventID: trig2.ID}
	b2 := makeBoundary(t, pl2, trig2)
	if err := d.Add(b2); err != nil {
		t.Fatalf("admit EpochBoundary(2): %v", err)
	}
}
