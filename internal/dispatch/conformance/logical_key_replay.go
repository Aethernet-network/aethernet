package conformance

import (
	"context"
	"errors"
	"sort"
	"sync"
	"testing"
	"time"

	badger "github.com/dgraph-io/badger/v4"

	"github.com/Aethernet-network/aethernet/internal/dispatch"
	"github.com/Aethernet-network/aethernet/internal/event"
	"github.com/Aethernet-network/aethernet/internal/recognition"
)

// RunLogicalKeyReplayConformance asserts that a logical-key consumer
// added to a populated DAG receives its historical events via the
// replay path AND enforces the per-key Apply guarantee (plan v2 §4.5
// step (g): "Future events for this key are observed but do not trigger
// Apply").
//
// This is the F4B counterpart of RunReplayConformance, which covers
// content-hash dispatch.Consumer. Logical-key admission is structurally
// different — the exactly-once boundary is (consumer, key) rather than
// (consumer, event) — so the assertions here are per-key, not per-event.
//
// Sub-tests:
//
//   - PopulatedDAGReplay_PerKey: pre-populate the DAG with a corpus that
//     includes MULTIPLE byte-distinct events for the same logical key
//     (e.g., two TVConsensus events for one RoundID). Replay the DAG
//     through the bus → dispatcher → logical-key consumer. Assert:
//       (a) Apply fired for every distinct logical key the consumer is
//           Interested() in.
//       (b) Apply fired at most ONCE per key, regardless of how many
//           events for that key appear in the DAG (the load-bearing
//           property of logical-key admission).
//
//   - ReplayIdempotent_PerKey: run the replay twice. Assert the second
//     run causes no re-Apply. The per-(consumer, key) admission record
//     transitions to StateApplied on the first Apply and short-circuits
//     all subsequent admissions.
//
//   - NonInterestedSkipped: corpus includes a non-matching event (the
//     makeNonMatchingProbe returns a Generation event, distinct from the
//     typical TVConsensus/Settlement event types LK consumers care about).
//     Assert Apply does NOT fire for the non-matching event. Mirrors
//     the same-named sub-test in RunReplayConformance.
//
// Wiring (mirrors §5.2.1 production pattern):
//
//   - buildPopulatedDAG: insert corpus events into a fresh in-memory DAG
//   - in-memory AdmissionStore + stub DAGAnchorReader for the dispatcher
//   - dispatch.NewDispatcher + RegisterLogicalKey(trackingConsumer)
//   - a routingCommitConsumer that calls dispatcher.Admit for every
//     event it sees (mirrors recognition.TaskVerificationConsensusConsumer
//     which routes events via SetDispatcher-injected ref)
//   - bus.Register(routingCommitConsumer) + bus.Start
//   - recognition.ReplayHistoricalToBusConsumers(ctx, dag, bus)
//   - assertions
//
// The consumer returned by factory is wrapped in lkApplyTracker, which
// proxies every LogicalKeyConsumer method call and records Apply
// invocations by logical key.
func RunLogicalKeyReplayConformance(
	t *testing.T,
	factory LogicalKeyConsumerFactory,
	corpus ReplayCorpus,
) {
	t.Helper()

	t.Run("PopulatedDAGReplay_PerKey", func(t *testing.T) {
		runLKReplayPerKey(t, factory, corpus)
	})

	t.Run("ReplayIdempotent_PerKey", func(t *testing.T) {
		runLKReplayIdempotent(t, factory, corpus)
	})

	t.Run("NonInterestedSkipped", func(t *testing.T) {
		runLKReplayNonInterestedSkipped(t, factory, corpus)
	})
}

// runLKReplayPerKey pre-populates the DAG with the corpus (which SHOULD
// include multi-emit — multiple events for the same logical key), runs
// the replay, and asserts Apply fired exactly once per distinct
// interested logical key.
func runLKReplayPerKey(t *testing.T, factory LogicalKeyConsumerFactory, corpus ReplayCorpus) {
	t.Helper()

	consumer, cleanup := factory()
	defer cleanup()

	tracker := newLKApplyTracker(consumer)

	events := corpus.Events(t)
	d := buildPopulatedDAG(t, events)

	disp, bus := buildLKDispatcherAndBus(t, tracker)
	defer bus.Stop()

	expectedKeys := expectedInterestedKeys(t, consumer, events)

	if err := recognition.ReplayHistoricalToBusConsumers(context.Background(), d, bus); err != nil {
		t.Fatalf("ReplayHistoricalToBusConsumers: %v", err)
	}

	waitForLKApplies(t, tracker, len(expectedKeys))

	gotKeys := tracker.appliedKeys()
	missing := diffKeys(expectedKeys, gotKeys)
	extra := diffKeys(gotKeys, expectedKeys)

	if len(missing) > 0 || len(extra) > 0 {
		t.Errorf("PopulatedDAGReplay_PerKey: Apply did not fire once per distinct interested key.\n"+
			"  expected (%d): %v\n"+
			"  got      (%d): %v\n"+
			"  missing  (%d): %v\n"+
			"  extra    (%d): %v",
			len(expectedKeys), expectedKeys, len(gotKeys), gotKeys,
			len(missing), missing, len(extra), extra)
	}

	// Per-key Apply guarantee: each key appears EXACTLY ONCE in the
	// invocation history, even if multiple events for that key were in
	// the corpus.
	for k, count := range tracker.applyCountByKey() {
		if count != 1 {
			t.Errorf("per-key Apply guarantee violated: key %q fired Apply %d times; expected exactly 1",
				k, count)
		}
	}

	// Keep the dispatcher reachable so the linter doesn't flag it as
	// unused. (The dispatcher is wired into the bus via the routing
	// consumer; this line is documentation, not behavior.)
	_ = disp
}

// runLKReplayIdempotent runs replay twice and asserts the second run
// does not cause any re-Apply. Per-(consumer, key) admission records
// transition to StateApplied on first success and short-circuit future
// admissions.
func runLKReplayIdempotent(t *testing.T, factory LogicalKeyConsumerFactory, corpus ReplayCorpus) {
	t.Helper()

	consumer, cleanup := factory()
	defer cleanup()

	tracker := newLKApplyTracker(consumer)

	events := corpus.Events(t)
	d := buildPopulatedDAG(t, events)

	_, bus := buildLKDispatcherAndBus(t, tracker)
	defer bus.Stop()

	expectedKeys := expectedInterestedKeys(t, consumer, events)

	if err := recognition.ReplayHistoricalToBusConsumers(context.Background(), d, bus); err != nil {
		t.Fatalf("first ReplayHistoricalToBusConsumers: %v", err)
	}
	waitForLKApplies(t, tracker, len(expectedKeys))
	firstCount := tracker.applyCallCount()

	// Second replay — per-(consumer, key) StateApplied short-circuits.
	if err := recognition.ReplayHistoricalToBusConsumers(context.Background(), d, bus); err != nil {
		t.Fatalf("second ReplayHistoricalToBusConsumers: %v", err)
	}
	// Give bus workers a brief window to (incorrectly) drain additional
	// Apply calls, so we observe a true count.
	time.Sleep(150 * time.Millisecond)

	secondCount := tracker.applyCallCount()
	if secondCount != firstCount {
		t.Errorf("ReplayIdempotent_PerKey: Apply re-fired across replays.\n"+
			"  after first replay:  %d invocations\n"+
			"  after second replay: %d invocations\n"+
			"The dispatcher's per-(consumer, key) admission record MUST short-circuit "+
			"second-run admissions when the first run transitioned the record to StateApplied.",
			firstCount, secondCount)
	}
}

// runLKReplayNonInterestedSkipped pre-populates the DAG with the corpus
// plus a non-matching probe event; asserts the consumer's Apply does
// NOT fire for it.
func runLKReplayNonInterestedSkipped(t *testing.T, factory LogicalKeyConsumerFactory, corpus ReplayCorpus) {
	t.Helper()

	consumer, cleanup := factory()
	defer cleanup()

	probe := makeNonMatchingProbe(t)
	if consumer.Interested(probe) {
		t.Skip("consumer is interested in every event the harness can construct; " +
			"NonInterestedSkipped sub-test is degenerate for this consumer.")
	}

	tracker := newLKApplyTracker(consumer)

	events := corpus.Events(t)
	d := buildPopulatedDAG(t, events)
	// Probe appended so it's a topological leaf (no incoming edges).
	if err := d.Add(probe); err != nil {
		t.Fatalf("add non-matching probe to DAG: %v", err)
	}

	_, bus := buildLKDispatcherAndBus(t, tracker)
	defer bus.Stop()

	expectedKeys := expectedInterestedKeys(t, consumer, events)

	if err := recognition.ReplayHistoricalToBusConsumers(context.Background(), d, bus); err != nil {
		t.Fatalf("ReplayHistoricalToBusConsumers: %v", err)
	}
	waitForLKApplies(t, tracker, len(expectedKeys))

	// The probe's key (if the consumer could even extract one) MUST NOT
	// appear in the applied set. Use the Interested() filter to guard
	// against Key() side-effects on non-interested events.
	if tracker.didFireForEvent(probe) {
		t.Errorf("NonInterestedSkipped: Apply fired for non-matching event %s; "+
			"the dispatcher MUST respect Interested() before invoking logical-key admission.",
			probe.ID)
	}
}

// ---------------------------------------------------------------------------
// lkApplyTracker — proxy that records Apply invocations by logical key
// ---------------------------------------------------------------------------

// lkApplyTracker wraps a dispatch.LogicalKeyConsumer and records every
// Apply invocation. The tracker is goroutine-safe; the bus dispatches
// from a worker pool.
type lkApplyTracker struct {
	inner dispatch.LogicalKeyConsumer

	mu             sync.Mutex
	appliesInOrder []dispatch.LogicalKey
	appliedByKey   map[dispatch.LogicalKey]int
	appliedEvents  map[event.EventID]struct{} // events whose dispatch reached Apply
}

func newLKApplyTracker(inner dispatch.LogicalKeyConsumer) *lkApplyTracker {
	return &lkApplyTracker{
		inner:         inner,
		appliedByKey:  make(map[dispatch.LogicalKey]int),
		appliedEvents: make(map[event.EventID]struct{}),
	}
}

// Name returns the wrapped consumer's name so the dispatcher's
// cross-kind uniqueness check works.
func (t *lkApplyTracker) Name() string { return t.inner.Name() }

func (t *lkApplyTracker) Interested(ev *event.Event) bool {
	return t.inner.Interested(ev)
}

func (t *lkApplyTracker) Key(ev *event.Event) (dispatch.LogicalKey, error) {
	return t.inner.Key(ev)
}

func (t *lkApplyTracker) RoundState(ctx context.Context, key dispatch.LogicalKey) (dispatch.RoundState, error) {
	return t.inner.RoundState(ctx, key)
}

func (t *lkApplyTracker) IsComplete(rs dispatch.RoundState) (bool, error) {
	return t.inner.IsComplete(rs)
}

func (t *lkApplyTracker) DeriveOutcome(rs dispatch.RoundState) (dispatch.Outcome, error) {
	return t.inner.DeriveOutcome(rs)
}

// Apply records the invocation then delegates to the wrapped consumer.
// The tracking MUST happen before the inner call so that even if the
// inner Apply panics, the test sees the invocation.
func (t *lkApplyTracker) Apply(ctx context.Context, triggerEventID event.EventID, key dispatch.LogicalKey, outcome dispatch.Outcome) error {
	t.mu.Lock()
	t.appliesInOrder = append(t.appliesInOrder, key)
	t.appliedByKey[key]++
	t.mu.Unlock()
	return t.inner.Apply(ctx, triggerEventID, key, outcome)
}

func (t *lkApplyTracker) RecoveryProbe(ctx context.Context, key dispatch.LogicalKey) (dispatch.RecoveryStatus, error) {
	return t.inner.RecoveryProbe(ctx, key)
}

func (t *lkApplyTracker) applyCallCount() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.appliesInOrder)
}

func (t *lkApplyTracker) applyCountByKey() map[dispatch.LogicalKey]int {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make(map[dispatch.LogicalKey]int, len(t.appliedByKey))
	// safe: map-to-map copy; no iteration-order observable effect
	for k, v := range t.appliedByKey {
		out[k] = v
	}
	return out
}

func (t *lkApplyTracker) appliedKeys() []dispatch.LogicalKey {
	t.mu.Lock()
	defer t.mu.Unlock()
	keys := make([]dispatch.LogicalKey, 0, len(t.appliedByKey))
	// safe: iteration order does not affect canonical state (non-canonical local surface, or commutative effect)
	for k := range t.appliedByKey {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
	return keys
}

func (t *lkApplyTracker) didFireForEvent(ev *event.Event) bool {
	// The probe's logical key may not be extractable if the consumer
	// isn't Interested. Detect by checking whether Key() on the probe
	// matches ANY key the consumer fired Apply for.
	k, err := t.inner.Key(ev)
	if err != nil {
		// Consumer can't extract a key from this event; it shouldn't have
		// fired Apply for it anyway. Use the events map as a fallback.
		t.mu.Lock()
		_, ok := t.appliedEvents[ev.ID]
		t.mu.Unlock()
		return ok
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.appliedByKey[k] > 0
}

// ---------------------------------------------------------------------------
// routingCommitConsumer — mirrors production recognition consumer shape
// ---------------------------------------------------------------------------

// routingCommitConsumer is a recognition.CommitConsumer that routes
// every event (matching its Interested filter) to a dispatch.Dispatcher
// via Admit. Mirrors the production shape of recognition consumers
// that hold a dispatcher reference via SetDispatcher (e.g.,
// TaskVerificationConsensusConsumer, SettlementConsumer).
//
// Exists so the replay-conformance harness can drive the full chain:
//
//	ReplayHistoricalToBusConsumers → Bus.Emit → Bus.processConsumer →
//	  routingCommitConsumer.Consume → Dispatcher.Admit →
//	    (content-hash path OR logical-key path) → Consumer.Apply
//
// Filters on the embedded LK consumer's Interested() to avoid sending
// probe / non-matching events to Admit (which would succeed trivially
// because no LK consumer is interested).
type routingCommitConsumer struct {
	name       string
	tracker    *lkApplyTracker
	dispatcher *dispatch.Dispatcher
}

func (c *routingCommitConsumer) Name() string                   { return c.name }
func (c *routingCommitConsumer) Interested(ev *event.Event) bool { return c.tracker.Interested(ev) }

func (c *routingCommitConsumer) Ready(_ context.Context, _ *event.Event, _ recognition.ReadModel) (bool, string, error) {
	return true, "", nil
}

func (c *routingCommitConsumer) Consume(ctx context.Context, ev *event.Event) error {
	return c.dispatcher.Admit(ctx, ev)
}

// ---------------------------------------------------------------------------
// Harness wiring
// ---------------------------------------------------------------------------

// buildLKDispatcherAndBus constructs a dispatcher with the tracker-
// wrapped logical-key consumer registered, plus a recognition bus with
// a routing consumer that forwards events to Dispatcher.Admit. Returns
// both so the caller can issue replays. Caller is responsible for
// bus.Stop.
func buildLKDispatcherAndBus(t *testing.T, tracker *lkApplyTracker) (*dispatch.Dispatcher, *recognition.Bus) {
	t.Helper()

	store := newLKHarnessAdmissionStore()
	dag := &lkHarnessDAG{}
	disp := dispatch.NewDispatcher(store, dag, func() uint64 { return 0 })

	if err := disp.RegisterLogicalKey(tracker); err != nil {
		t.Fatalf("RegisterLogicalKey: %v", err)
	}

	idx := recognition.NewIndex(recognition.NewMemoryIndexStore())
	rm := newReplayHarnessReadModel()
	bus := recognition.NewBus(
		recognition.BusConfig{QueueSize: 256, Workers: 2},
		idx,
		rm,
	)
	routing := &routingCommitConsumer{
		name:       tracker.Name() + "_router",
		tracker:    tracker,
		dispatcher: disp,
	}
	// Register cannot fail for a fresh bus and a single consumer.
	_ = bus.Register(routing)
	bus.Start()
	return disp, bus
}

// expectedInterestedKeys computes the set of distinct logical keys the
// consumer is Interested() in across the corpus. Sorted for
// deterministic diff output.
func expectedInterestedKeys(t *testing.T, c dispatch.LogicalKeyConsumer, events []*event.Event) []dispatch.LogicalKey {
	t.Helper()
	seen := make(map[dispatch.LogicalKey]struct{}, len(events))
	for _, ev := range events {
		if !c.Interested(ev) {
			continue
		}
		k, err := c.Key(ev)
		if err != nil {
			t.Fatalf("expectedInterestedKeys: Key(%s): %v", ev.ID, err)
		}
		seen[k] = struct{}{}
	}
	keys := make([]dispatch.LogicalKey, 0, len(seen))
	// safe: iteration order does not affect canonical state (non-canonical local surface, or commutative effect)
	for k := range seen {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
	return keys
}

// diffKeys returns elements in a not present in b.
func diffKeys(a, b []dispatch.LogicalKey) []dispatch.LogicalKey {
	bset := make(map[dispatch.LogicalKey]struct{}, len(b))
	for _, k := range b {
		bset[k] = struct{}{}
	}
	var out []dispatch.LogicalKey
	for _, k := range a {
		if _, ok := bset[k]; !ok {
			out = append(out, k)
		}
	}
	return out
}

// waitForLKApplies polls the tracker until it observes at least want
// distinct-key invocations or a generous deadline elapses.
func waitForLKApplies(t *testing.T, tracker *lkApplyTracker, want int) {
	t.Helper()
	if want == 0 {
		time.Sleep(150 * time.Millisecond)
		return
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if len(tracker.appliedKeys()) >= want {
			time.Sleep(50 * time.Millisecond) // settle
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// ---------------------------------------------------------------------------
// In-memory harness stubs for dispatcher dependencies
// ---------------------------------------------------------------------------

// lkHarnessAdmissionStore is a thread-safe in-memory AdmissionStore for
// the harness. Mirrors the production *store.Store's AdmissionStore
// interface but stays in-memory so the test runs hermetically.
type lkHarnessAdmissionStore struct {
	mu      sync.Mutex
	records map[string]*dispatch.AdmissionRecord
}

func newLKHarnessAdmissionStore() *lkHarnessAdmissionStore {
	return &lkHarnessAdmissionStore{records: make(map[string]*dispatch.AdmissionRecord)}
}

func (s *lkHarnessAdmissionStore) GetAdmission(key string) (*dispatch.AdmissionRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, ok := s.records[key]
	if !ok {
		// Dispatcher's isNotFound() matches err.Error() == "Key not found"
		// (BadgerDB's sentinel). Return that exact error so reserveOrLoad
		// Logical takes the "create fresh record" branch rather than
		// surfacing an unexpected error.
		return nil, badger.ErrKeyNotFound
	}
	cp := *rec
	if rec.Consumers != nil {
		cp.Consumers = make(map[string]dispatch.PerConsumerStatus, len(rec.Consumers))
		// safe: iteration order does not affect canonical state (non-canonical local surface, or commutative effect)
		for k, v := range rec.Consumers {
			cp.Consumers[k] = v
		}
	}
	return &cp, nil
}

func (s *lkHarnessAdmissionStore) PutAdmission(key string, rec *dispatch.AdmissionRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := *rec
	if rec.Consumers != nil {
		cp.Consumers = make(map[string]dispatch.PerConsumerStatus, len(rec.Consumers))
		// safe: iteration order does not affect canonical state (non-canonical local surface, or commutative effect)
		for k, v := range rec.Consumers {
			cp.Consumers[k] = v
		}
	}
	s.records[key] = &cp
	return nil
}

func (s *lkHarnessAdmissionStore) DeleteAdmission(key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.records, key)
	return nil
}

func (s *lkHarnessAdmissionStore) AllAdmissions() ([]*dispatch.AdmissionRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*dispatch.AdmissionRecord, 0, len(s.records))
	// safe: iteration order does not affect canonical state (non-canonical local surface, or commutative effect)
	for _, rec := range s.records {
		cp := *rec
		out = append(out, &cp)
	}
	return out, nil
}

// lkHarnessDAG is a minimal DAGAnchorReader for the harness. Returns no
// tips, so VerifyAnchor short-circuits to nil (anchor verification is
// intentionally inert for the template — mirroring the existing
// dispatcher_harness.go shape).
type lkHarnessDAG struct{}

func (*lkHarnessDAG) Tips() []event.EventID { return nil }
func (*lkHarnessDAG) IsAncestor(_, _ event.EventID) (bool, error) {
	return false, errors.New("harness DAG: IsAncestor not implemented")
}
func (*lkHarnessDAG) Get(_ event.EventID) (*event.Event, error) {
	return nil, errors.New("harness DAG: Get not implemented")
}
