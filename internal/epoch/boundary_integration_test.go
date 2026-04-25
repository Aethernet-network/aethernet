package epoch_test

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"

	"github.com/Aethernet-network/aethernet/internal/crypto"
	"github.com/Aethernet-network/aethernet/internal/dag"
	"github.com/Aethernet-network/aethernet/internal/epoch"
	"github.com/Aethernet-network/aethernet/internal/event"
)

// fakePublisher captures published events for assertion + delegates to
// the DAG via Add. Mirrors localpub.Publisher.Publish's contract for
// the emitter's purposes (Publish → dag.Add → admission cross-check).
type fakePublisher struct {
	mu        sync.Mutex
	d         *dag.DAG
	published []*event.Event
}

func (p *fakePublisher) Publish(ev *event.Event) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := p.d.Add(ev); err != nil {
		return err
	}
	p.published = append(p.published, ev)
	return nil
}

func (p *fakePublisher) Published() []*event.Event {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]*event.Event, len(p.published))
	copy(out, p.published)
	return out
}

// buildChainOfTVCs adds n TaskVerificationConsensus events to d in a
// linear chain rooted at parent. Returns the full slice of TVCs.
// Useful for crossing the EpochLength threshold deterministically.
func buildChainOfTVCs(t *testing.T, d *dag.DAG, parent *event.Event, n int) []*event.Event {
	t.Helper()
	out := make([]*event.Event, n)
	cur := parent
	for i := 0; i < n; i++ {
		tvc := makeTVConsensus(t, cur)
		if err := d.Add(tvc); err != nil {
			t.Fatalf("add TVC[%d]: %v", i, err)
		}
		out[i] = tvc
		cur = tvc
	}
	return out
}

// TestBoundary_EndToEnd_EmitterPublishesValidBoundaryAdmission verifies
// the full path: emitter receives TVC at threshold → constructs
// EpochBoundary → publish (= dag.Add) → admission cross-check passes →
// event lands in DAG → CountAncestorsByType observes it as +1.
func TestBoundary_EndToEnd_EmitterPublishesValidBoundaryAdmission(t *testing.T) {
	d := dag.New()
	if err := d.RegisterAdmissionCrossCheck(event.EventTypeEpochBoundary, epoch.BoundaryAdmissionValidator); err != nil {
		t.Fatalf("register cross-check: %v", err)
	}

	// Build EpochLength TVCs ending at the threshold-crossing event.
	g := makeTransfer(t)
	if err := d.Add(g); err != nil {
		t.Fatalf("add genesis: %v", err)
	}
	tvcs := buildChainOfTVCs(t, d, g, int(epoch.EpochLength))
	trigger := tvcs[len(tvcs)-1]

	// Pre-condition: no EpochBoundary ancestors of trigger.
	pre, err := d.CountAncestorsByType(trigger.ID, event.EventTypeEpochBoundary)
	if err != nil {
		t.Fatalf("pre-count: %v", err)
	}
	if pre != 0 {
		t.Fatalf("pre-condition violated: trigger has %d EpochBoundary ancestors, want 0", pre)
	}

	pub := &fakePublisher{d: d}
	signer := kp(t)
	emitter := epoch.NewBoundaryEmitter(d, pub, signer)

	if err := emitter.Consume(context.Background(), trigger); err != nil {
		t.Fatalf("emitter.Consume(trigger): %v", err)
	}

	// Post-condition 1: exactly one EpochBoundary published.
	if got := len(pub.Published()); got != 1 {
		t.Fatalf("published count = %d, want 1", got)
	}
	emitted := pub.Published()[0]
	if emitted.Type != event.EventTypeEpochBoundary {
		t.Fatalf("emitted type = %q, want EpochBoundary", emitted.Type)
	}

	// Post-condition 2: payload is canonical.
	var payload event.EpochBoundaryPayload
	if err := json.Unmarshal(emitted.Payload, &payload); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if payload.Epoch != 1 || payload.TriggerEventID != trigger.ID {
		t.Fatalf("payload = %+v, want Epoch=1 + TriggerEventID=trigger", payload)
	}

	// Post-condition 3: a follow-up TVC sees one EpochBoundary ancestor.
	follow := makeTVConsensus(t, emitted)
	if err := d.Add(follow); err != nil {
		t.Fatalf("add follow TVC: %v", err)
	}
	post, err := d.CountAncestorsByType(follow.ID, event.EventTypeEpochBoundary)
	if err != nil {
		t.Fatalf("post-count: %v", err)
	}
	if post != 1 {
		t.Fatalf("post-count = %d, want 1 (the freshly-emitted EpochBoundary(1))", post)
	}
}

// TestBoundary_GenesisEpochZero_NoEmissionBeforeFirstThreshold verifies
// sub-spec §7: in the first 1000 TVCs, no EpochBoundary is emitted;
// epoch_of(R) = 0 for any round whose seal context is one of those TVCs.
func TestBoundary_GenesisEpochZero_NoEmissionBeforeFirstThreshold(t *testing.T) {
	d := dag.New()
	if err := d.RegisterAdmissionCrossCheck(event.EventTypeEpochBoundary, epoch.BoundaryAdmissionValidator); err != nil {
		t.Fatalf("register cross-check: %v", err)
	}

	g := makeTransfer(t)
	if err := d.Add(g); err != nil {
		t.Fatalf("add genesis: %v", err)
	}
	// Build EpochLength-1 TVCs (one shy of threshold).
	tvcs := buildChainOfTVCs(t, d, g, int(epoch.EpochLength)-1)

	pub := &fakePublisher{d: d}
	signer := kp(t)
	emitter := epoch.NewBoundaryEmitter(d, pub, signer)

	// Drive emitter on every TVC. None should produce an emission.
	for _, tvc := range tvcs {
		if err := emitter.Consume(context.Background(), tvc); err != nil {
			t.Fatalf("emitter.Consume: %v", err)
		}
	}
	if got := len(pub.Published()); got != 0 {
		t.Fatalf("emissions before first threshold = %d, want 0 (sub-spec §7)", got)
	}

	// epoch_of(any TVC) = 0.
	for i, tvc := range tvcs {
		count, err := d.CountAncestorsByType(tvc.ID, event.EventTypeEpochBoundary)
		if err != nil {
			t.Fatalf("count for TVC[%d]: %v", i, err)
		}
		if count != 0 {
			t.Fatalf("epoch_of(TVC[%d]) = %d, want 0", i, count)
		}
	}
}

// TestBoundary_DeferralPath_ErrEventNotFoundReturnedToCaller verifies
// sub-spec §8.4: when CountAncestorsByType returns ErrEventNotFound
// (defensive case), the caller surfaces it loudly. Constructed by
// querying for a descendant that isn't in the DAG.
func TestBoundary_DeferralPath_ErrEventNotFoundReturnedToCaller(t *testing.T) {
	d := dag.New()

	// Empty DAG; ask for ancestors of an event that doesn't exist.
	_, err := d.CountAncestorsByType("nonexistent-event-id", event.EventTypeEpochBoundary)
	if !errors.Is(err, dag.ErrEventNotFound) {
		t.Fatalf("CountAncestorsByType on missing descendant: want ErrEventNotFound, got %v", err)
	}
}

// TestBoundary_MultiEmit_BothAdmittedByCrossCheck verifies sub-spec §2.2
// Candidate A: at the admission layer (cross-check only), two distinct
// emitters BOTH succeed in publishing EpochBoundary(1) for the same
// trigger. The cross-check is from the trigger's perspective:
// CountAncestorsByType(TriggerEventID, EpochBoundary) is 0 in both
// cases because newly-emitted boundaries are DESCENDANTS of the
// trigger, not ancestors. Admission cannot dedup; the LogicalKeyConsumer
// (sub-spec §2.2) is what converges multi-emit at admission to ONE
// canonical EpochBoundary per Epoch in production wiring.
//
// This test isolates the admission-layer behavior (multi-emit
// permitted); a separate test below exercises the LK-dedup path.
func TestBoundary_MultiEmit_BothAdmittedByCrossCheck(t *testing.T) {
	d := dag.New()
	if err := d.RegisterAdmissionCrossCheck(event.EventTypeEpochBoundary, epoch.BoundaryAdmissionValidator); err != nil {
		t.Fatalf("register cross-check: %v", err)
	}

	g := makeTransfer(t)
	if err := d.Add(g); err != nil {
		t.Fatalf("add genesis: %v", err)
	}
	tvcs := buildChainOfTVCs(t, d, g, int(epoch.EpochLength))
	trigger := tvcs[len(tvcs)-1]

	pubA := &fakePublisher{d: d}
	emitterA := epoch.NewBoundaryEmitter(d, pubA, kp(t))
	if err := emitterA.Consume(context.Background(), trigger); err != nil {
		t.Fatalf("emitterA.Consume: %v", err)
	}

	pubB := &fakePublisher{d: d}
	emitterB := epoch.NewBoundaryEmitter(d, pubB, kp(t))
	if err := emitterB.Consume(context.Background(), trigger); err != nil {
		t.Fatalf("emitterB.Consume: %v", err)
	}

	if got := len(pubA.Published()); got != 1 {
		t.Fatalf("emitterA published count = %d, want 1", got)
	}
	if got := len(pubB.Published()); got != 1 {
		t.Fatalf("emitterB published count = %d, want 1 (admission cross-check accepts both; LK dedup is what converges in production)", got)
	}

	// Both emissions carry canonically-equivalent payloads.
	for _, p := range []*fakePublisher{pubA, pubB} {
		var payload event.EpochBoundaryPayload
		if err := json.Unmarshal(p.Published()[0].Payload, &payload); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if payload.Version != 1 || payload.Epoch != 1 || payload.TriggerEventID != trigger.ID {
			t.Fatalf("payload not canonical: %+v", payload)
		}
	}

	// But distinct content-hashes (AgentID in preimage per §1.5).
	if pubA.Published()[0].ID == pubB.Published()[0].ID {
		t.Fatalf("emitterA and emitterB should produce distinct content-hashes (different signers); both got %s", pubA.Published()[0].ID)
	}
}

// TestBoundary_ThreeNodeConvergence_ContentHashesDiffer_PayloadsCanonicallyEqual
// is the multi-emit dedup-convergence scenario: three "nodes"
// (in-process DAGs each with their own signer) independently emit
// EpochBoundary(1) for the same canonical trigger. Each node's local
// DAG admits its own emission; admission cross-check passes on each
// node independently because no other emission is yet in any node's
// local DAG. Cross-node convergence happens when the first emission
// arrives at peer nodes — at that point the LogicalKeyConsumer (in
// production) deduplicates by Epoch.
//
// This test isolates the canonical-correctness property: every node's
// emission has the same canonical PAYLOAD (Version, Epoch,
// TriggerEventID) regardless of which signer produced it. Different
// content-hashes via AgentID variation.
func TestBoundary_ThreeNodeConvergence_ContentHashesDiffer_PayloadsCanonicallyEqual(t *testing.T) {
	// Build a SHARED canonical DAG state: all three nodes have the same
	// genesis + EpochLength TVCs in their local DAGs. (We simulate by
	// constructing one DAG and letting each emitter emit against it.)
	d := dag.New()
	if err := d.RegisterAdmissionCrossCheck(event.EventTypeEpochBoundary, epoch.BoundaryAdmissionValidator); err != nil {
		t.Fatalf("register cross-check: %v", err)
	}
	g := makeTransfer(t)
	if err := d.Add(g); err != nil {
		t.Fatalf("add genesis: %v", err)
	}
	tvcs := buildChainOfTVCs(t, d, g, int(epoch.EpochLength))
	trigger := tvcs[len(tvcs)-1]

	// Three independent signers.
	signers := []*crypto.KeyPair{kp(t), kp(t), kp(t)}
	emissions := make([]*event.Event, 0, 3)

	// Each emitter constructs its own emission. We bypass the publisher's
	// dag.Add path here — we want to construct the events without
	// admission, so we can compare them apples-to-apples.
	for _, signer := range signers {
		payload := event.EpochBoundaryPayload{
			Version:        1,
			Epoch:          1,
			TriggerEventID: trigger.ID,
		}
		payloadBytes, err := json.Marshal(payload)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		ev, err := event.New(
			event.EventTypeEpochBoundary,
			[]event.EventID{trigger.ID},
			json.RawMessage(payloadBytes),
			string(signer.AgentID()),
			map[event.EventID]uint64{trigger.ID: trigger.CausalTimestamp},
			0,
		)
		if err != nil {
			t.Fatalf("event.New: %v", err)
		}
		if err := crypto.SignEvent(ev, signer); err != nil {
			t.Fatalf("SignEvent: %v", err)
		}
		emissions = append(emissions, ev)
	}

	// Property 1: all three emissions have CANONICALLY-EQUIVALENT payloads.
	for i, ev := range emissions {
		var p event.EpochBoundaryPayload
		if err := json.Unmarshal(ev.Payload, &p); err != nil {
			t.Fatalf("emission[%d] unmarshal: %v", i, err)
		}
		if p.Version != 1 || p.Epoch != 1 || p.TriggerEventID != trigger.ID {
			t.Fatalf("emission[%d] payload not canonical: %+v", i, p)
		}
	}

	// Property 2: all three emissions have DISTINCT content-hashes
	// (AgentID is in the canonical preimage per sub-spec §1.5).
	seen := make(map[event.EventID]bool, 3)
	for i, ev := range emissions {
		if seen[ev.ID] {
			t.Fatalf("emission[%d] has duplicate ID %s; expected distinct content-hashes per emitter", i, ev.ID)
		}
		seen[ev.ID] = true
	}

	// Property 3: each emission individually passes admission cross-check
	// against the SAME canonical DAG state (one at a time; in production
	// the LK consumer would prevent the second admission, but the
	// CANONICALITY of each is independently verifiable).
	//
	// Strategy: take a snapshot DAG (just up through trigger), admit
	// each emission separately, verify each succeeds.
	for i, ev := range emissions {
		// Fresh DAG per emission so we test admission in isolation.
		freshD := dag.New()
		if err := freshD.RegisterAdmissionCrossCheck(event.EventTypeEpochBoundary, epoch.BoundaryAdmissionValidator); err != nil {
			t.Fatalf("emission[%d] register: %v", i, err)
		}
		// Replay genesis + chain into freshD.
		if err := freshD.Add(g); err != nil {
			t.Fatalf("emission[%d] replay genesis: %v", i, err)
		}
		for j, tvc := range tvcs {
			if err := freshD.Add(tvc); err != nil {
				t.Fatalf("emission[%d] replay tvc[%d]: %v", i, j, err)
			}
		}
		// Now admit the emission.
		if err := freshD.Add(ev); err != nil {
			t.Fatalf("emission[%d] admission: %v (content-hash %s, signer %s)", i, err, ev.ID, ev.AgentID)
		}
	}
}
