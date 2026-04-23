package conformance

import (
	"context"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/Aethernet-network/aethernet/internal/crypto"
	"github.com/Aethernet-network/aethernet/internal/dag"
	"github.com/Aethernet-network/aethernet/internal/dispatch"
	"github.com/Aethernet-network/aethernet/internal/event"
	"github.com/Aethernet-network/aethernet/internal/recognition"
)

// ReplayCorpus describes the events to populate the DAG before replay
// begins. Implementations construct fresh, signed events per sub-test
// for isolation. Events MUST be returned in causal order — parents
// before children — because the harness inserts them sequentially via
// dag.Add and dag.Add rejects events whose CausalRefs are not yet in
// the DAG.
//
// The corpus is independent of the consumer under test; the consumer's
// Interested() filter decides which events it cares about. The harness
// asserts that Apply fires exactly for the matching subset.
type ReplayCorpus interface {
	// Events returns the historical event corpus for one sub-test. Called
	// fresh per sub-test so the corpus may be reconstructed (and resigned)
	// without mutation across runs. Returns parents before children.
	Events(t *testing.T) []*event.Event
}

// ReplayCorpusFunc adapts a function literal to the ReplayCorpus
// interface, enabling tests to declare corpora inline:
//
//	corpus := conformance.ReplayCorpusFunc(func(t *testing.T) []*event.Event {
//	    return []*event.Event{makeTransfer(t, "alice", 100), ...}
//	})
type ReplayCorpusFunc func(t *testing.T) []*event.Event

// Events implements ReplayCorpus.
func (f ReplayCorpusFunc) Events(t *testing.T) []*event.Event { return f(t) }

// RunReplayConformance asserts that a consumer added to a populated DAG
// receives every interested historical event via the recognition replay
// path, and that re-running the replay does NOT cause re-application.
//
// Sub-tests:
//
//   - PopulatedDAGReplay: pre-populate the DAG with the corpus, register
//     a fresh consumer wrapped as a recognition.CommitConsumer, run
//     ReplayHistoricalToBusConsumers, then assert Apply fired exactly
//     once for every corpus event the consumer is Interested() in.
//
//   - ReplayIdempotent: run the replay twice; assert the second run
//     does NOT cause Apply to fire again (per-consumer
//     MarkRecognizedOnce in Bus.processConsumer is the gate).
//
//   - NonInterestedSkipped: ensure a corpus event the consumer rejects
//     via Interested() does not trigger Apply.
//
// Wiring rationale (chosen Option A from the F4A step 2 brief):
// the harness wraps the dispatch.Consumer in an unexported
// recognition.CommitConsumer adapter and drives the FULL bus →
// consumer chain. This catches a class of wiring bugs that direct
// dispatch.Consumer testing would miss — exactly the bugs §8.1 of the
// F4 plan exists to prevent (events committed via LoadFromStore never
// reaching bus consumers because the bus pathway was bypassed).
//
// Logical-key counterpart: RunLogicalKeyReplayConformance in
// logical_key_replay.go mirrors this template for Type E consumers
// (per-key assertions rather than per-event; drives the full bus →
// dispatcher → LogicalKeyConsumer chain end-to-end).
func RunReplayConformance(t *testing.T, factory ConsumerFactory, corpus ReplayCorpus) {
	t.Helper()

	t.Run("PopulatedDAGReplay", func(t *testing.T) {
		runReplayPopulatedDAG(t, factory, corpus)
	})

	t.Run("ReplayIdempotent", func(t *testing.T) {
		runReplayIdempotent(t, factory, corpus)
	})

	t.Run("NonInterestedSkipped", func(t *testing.T) {
		runReplayNonInterestedSkipped(t, factory, corpus)
	})
}

// runReplayPopulatedDAG pre-populates the DAG with the corpus, registers
// a fresh consumer, runs the replay, and asserts Apply fired exactly
// once for every interested event.
func runReplayPopulatedDAG(t *testing.T, factory ConsumerFactory, corpus ReplayCorpus) {
	t.Helper()

	consumer, cleanup := factory()
	defer cleanup()

	tracker := newApplyTracker(consumer)

	events := corpus.Events(t)
	d := buildPopulatedDAG(t, events)
	bus := buildBusWith(tracker)
	bus.Start()
	defer bus.Stop()

	expected := expectedInterested(consumer, events)

	if err := recognition.ReplayHistoricalToBusConsumers(context.Background(), d, bus); err != nil {
		t.Fatalf("ReplayHistoricalToBusConsumers: %v", err)
	}

	waitForApplies(t, tracker, len(expected))

	got := tracker.appliedIDs()
	missing := diffIDSets(expected, got)
	extra := diffIDSets(got, expected)

	if len(missing) > 0 || len(extra) > 0 {
		t.Errorf("PopulatedDAGReplay: Apply did not fire for the expected interested events.\n"+
			"  expected (%d): %v\n"+
			"  got      (%d): %v\n"+
			"  missing  (%d): %v\n"+
			"  extra    (%d): %v",
			len(expected), expected, len(got), got,
			len(missing), missing, len(extra), extra)
	}
}

// runReplayIdempotent runs the replay twice and asserts the second run
// does not re-fire Apply for any event already applied by the first run.
func runReplayIdempotent(t *testing.T, factory ConsumerFactory, corpus ReplayCorpus) {
	t.Helper()

	consumer, cleanup := factory()
	defer cleanup()

	tracker := newApplyTracker(consumer)

	events := corpus.Events(t)
	d := buildPopulatedDAG(t, events)
	bus := buildBusWith(tracker)
	bus.Start()
	defer bus.Stop()

	expected := expectedInterested(consumer, events)

	// First replay populates the recognition index and fires Apply.
	if err := recognition.ReplayHistoricalToBusConsumers(context.Background(), d, bus); err != nil {
		t.Fatalf("first ReplayHistoricalToBusConsumers: %v", err)
	}
	waitForApplies(t, tracker, len(expected))
	firstCount := tracker.applyCount()

	// Second replay must be a no-op for already-recognized events.
	if err := recognition.ReplayHistoricalToBusConsumers(context.Background(), d, bus); err != nil {
		t.Fatalf("second ReplayHistoricalToBusConsumers: %v", err)
	}
	// Give the bus workers a brief window to drain anything that might
	// (incorrectly) have been re-emitted, so we observe a true count.
	time.Sleep(150 * time.Millisecond)

	secondCount := tracker.applyCount()
	if secondCount != firstCount {
		t.Errorf("ReplayIdempotent: Apply re-fired across replays.\n"+
			"  after first replay:  %d invocations\n"+
			"  after second replay: %d invocations\n"+
			"Per-consumer MarkRecognizedOnce should have prevented re-emission.",
			firstCount, secondCount)
	}
}

// runReplayNonInterestedSkipped pre-populates the DAG with the corpus
// plus a synthetic non-matching event, asserts the consumer's Apply
// does NOT fire for the non-matching event.
func runReplayNonInterestedSkipped(t *testing.T, factory ConsumerFactory, corpus ReplayCorpus) {
	t.Helper()

	consumer, cleanup := factory()
	defer cleanup()

	// Probe whether the consumer can be made to reject any candidate
	// event we know how to construct. If it accepts everything, the
	// "non-interested" axis is degenerate for this consumer and we skip.
	probe := makeNonMatchingProbe(t)
	if consumer.Interested(probe) {
		t.Skip("consumer is interested in every event the harness can construct; " +
			"NonInterestedSkipped sub-test is degenerate for this consumer.")
	}

	tracker := newApplyTracker(consumer)

	events := corpus.Events(t)
	d := buildPopulatedDAG(t, events)
	// Append the non-matching event last so it's a topological leaf.
	if err := d.Add(probe); err != nil {
		t.Fatalf("add non-matching probe to DAG: %v", err)
	}
	bus := buildBusWith(tracker)
	bus.Start()
	defer bus.Stop()

	expected := expectedInterested(consumer, events)

	if err := recognition.ReplayHistoricalToBusConsumers(context.Background(), d, bus); err != nil {
		t.Fatalf("ReplayHistoricalToBusConsumers: %v", err)
	}
	waitForApplies(t, tracker, len(expected))

	if tracker.wasApplied(probe.ID) {
		t.Errorf("NonInterestedSkipped: Apply fired for non-matching event %s; "+
			"the bus dispatch must respect Interested() during replay.", probe.ID)
	}
}

// ---------------------------------------------------------------------------
// Adapter: dispatch.Consumer → recognition.CommitConsumer.
//
// The recognition bus knows nothing about the dispatch.Consumer
// interface; it speaks CommitConsumer. The adapter bridges the two so
// the harness can drive the full bus → consumer chain end-to-end. This
// mirrors the production wiring done in cmd/node/main.go where
// recognition consumers (e.g., TaskVerificationConsensusConsumer) wrap
// and route to the dispatcher; here we route directly to the
// dispatch.Consumer's Apply since the harness's purpose is to validate
// delivery, not the dispatcher's admission state machine.
// ---------------------------------------------------------------------------

// applyTracker wraps a dispatch.Consumer and records every Apply
// invocation. Goroutine-safe; the bus dispatches from a worker pool.
type applyTracker struct {
	inner dispatch.Consumer

	mu      sync.Mutex
	applies []event.EventID // in invocation order; duplicates preserved
	applied map[event.EventID]int
}

func newApplyTracker(inner dispatch.Consumer) *applyTracker {
	return &applyTracker{
		inner:   inner,
		applied: make(map[event.EventID]int),
	}
}

// Name implements recognition.CommitConsumer.
func (t *applyTracker) Name() string { return t.inner.Name() }

// Interested implements recognition.CommitConsumer.
func (t *applyTracker) Interested(ev *event.Event) bool { return t.inner.Interested(ev) }

// Ready implements recognition.CommitConsumer. The harness assumes the
// consumer is always ready during replay; consumers that return false
// from Interested() filter out events they don't care about, so this
// path is unconditional. If a consumer requires prerequisites, that's a
// dispatch.Consumer-level concern handled by the dispatcher's Admit
// pipeline, not by the recognition bus.
func (t *applyTracker) Ready(_ context.Context, _ *event.Event, _ recognition.ReadModel) (bool, string, error) {
	return true, "", nil
}

// Consume implements recognition.CommitConsumer. Records the invocation
// then delegates to the wrapped consumer's Apply.
func (t *applyTracker) Consume(ctx context.Context, ev *event.Event) error {
	t.mu.Lock()
	t.applies = append(t.applies, ev.ID)
	t.applied[ev.ID]++
	t.mu.Unlock()
	return t.inner.Apply(ctx, ev)
}

func (t *applyTracker) applyCount() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.applies)
}

func (t *applyTracker) wasApplied(id event.EventID) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.applied[id] > 0
}

// appliedIDs returns the de-duplicated set of applied event IDs in
// canonical sort order.
func (t *applyTracker) appliedIDs() []event.EventID {
	t.mu.Lock()
	defer t.mu.Unlock()
	ids := make([]event.EventID, 0, len(t.applied))
	// safe: iteration order does not affect canonical state (non-canonical local surface, or commutative effect)
	for id := range t.applied {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids
}

// ---------------------------------------------------------------------------
// Harness wiring helpers.
// ---------------------------------------------------------------------------

// buildPopulatedDAG creates a fresh DAG and inserts every event in the
// supplied slice via dag.Add. The DAG is left without a SetOnCommit hook —
// this is intentional: it mirrors the LoadFromStore-before-SetOnCommit
// gap §8.1 closes. The replay function under test must observe these
// events from a clean DAG snapshot, not from a SetOnCommit-fired stream.
//
// Takes a pre-materialized event slice (rather than a ReplayCorpus) so
// callers can compute the corpus once per sub-test and reuse it for both
// DAG population and the Interested() expectation set. Calling
// corpus.Events(t) twice within a sub-test would generate two unrelated
// fixtures whose EventIDs do not match, breaking the assertion.
func buildPopulatedDAG(t *testing.T, events []*event.Event) *dag.DAG {
	t.Helper()
	d := dag.New()
	for i, ev := range events {
		if err := d.Add(ev); err != nil {
			t.Fatalf("buildPopulatedDAG: add event[%d] %s: %v", i, ev.ID, err)
		}
	}
	return d
}

// buildBusWith creates a fresh recognition.Bus with the given tracker
// registered. The bus uses a small in-memory index store so per-consumer
// MarkRecognizedOnce idempotency works as in production. Caller is
// responsible for Start/Stop.
func buildBusWith(tracker *applyTracker) *recognition.Bus {
	idx := recognition.NewIndex(recognition.NewMemoryIndexStore())
	rm := newReplayHarnessReadModel()
	bus := recognition.NewBus(
		recognition.BusConfig{QueueSize: 256, Workers: 2},
		idx,
		rm,
	)
	// Register cannot fail for a fresh bus and a single consumer.
	_ = bus.Register(tracker)
	return bus
}

// replayHarnessReadModel is a no-op ReadModel: the harness's tracker
// always returns ready=true, so the bus never invokes the read model.
// Provided to satisfy the bus constructor's contract.
type replayHarnessReadModel struct{}

func newReplayHarnessReadModel() *replayHarnessReadModel { return &replayHarnessReadModel{} }
func (*replayHarnessReadModel) HasEvent(event.EventID) bool        { return true }
func (*replayHarnessReadModel) GetEvent(event.EventID) *event.Event { return nil }

// expectedInterested returns the de-duplicated, sorted set of corpus
// EventIDs the consumer is Interested() in.
func expectedInterested(c dispatch.Consumer, events []*event.Event) []event.EventID {
	seen := make(map[event.EventID]struct{}, len(events))
	for _, ev := range events {
		if c.Interested(ev) {
			seen[ev.ID] = struct{}{}
		}
	}
	ids := make([]event.EventID, 0, len(seen))
	// safe: iteration order does not affect canonical state (non-canonical local surface, or commutative effect)
	for id := range seen {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids
}

// diffIDSets returns elements in a not present in b.
func diffIDSets(a, b []event.EventID) []event.EventID {
	bset := make(map[event.EventID]struct{}, len(b))
	for _, id := range b {
		bset[id] = struct{}{}
	}
	var out []event.EventID
	for _, id := range a {
		if _, ok := bset[id]; !ok {
			out = append(out, id)
		}
	}
	return out
}

// waitForApplies polls the tracker until it observes at least want
// invocations or a generous deadline elapses. Bus dispatch is async and
// runs on a small worker pool; in practice 3-event corpora drain in
// single-digit milliseconds. The deadline exists only so a broken
// implementation fails fast with a clear message rather than hanging.
//
// Does NOT t.Fatal on a short count — assertions belong in the caller
// so the failure message can describe what was expected vs observed.
func waitForApplies(t *testing.T, tracker *applyTracker, want int) {
	t.Helper()
	if want == 0 {
		// Even with no expected applies we want to give the bus a chance
		// to (incorrectly) emit something so an over-application surfaces.
		time.Sleep(150 * time.Millisecond)
		return
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if tracker.applyCount() >= want {
			// Brief grace period to catch any spurious extra emissions.
			time.Sleep(50 * time.Millisecond)
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// makeNonMatchingProbe builds a signed Generation event the test can
// insert into the DAG to verify Interested()-based filtering during
// replay. Generation is structurally similar to Transfer (both root-level
// payload-bearing events) but uses a different EventType — consumers
// that filter on type will reject it cleanly.
func makeNonMatchingProbe(t *testing.T) *event.Event {
	t.Helper()
	kp, err := crypto.GenerateKeyPair()
	if err != nil {
		t.Fatalf("makeNonMatchingProbe: keypair: %v", err)
	}
	aid := string(kp.AgentID())
	ev, err := event.New(
		event.EventTypeGeneration,
		nil,
		map[string]string{"reason": "replay-conformance-non-matching-probe"},
		aid,
		nil,
		0,
	)
	if err != nil {
		t.Fatalf("makeNonMatchingProbe: event.New: %v", err)
	}
	if err := crypto.SignEvent(ev, kp); err != nil {
		t.Fatalf("makeNonMatchingProbe: sign: %v", err)
	}
	return ev
}
