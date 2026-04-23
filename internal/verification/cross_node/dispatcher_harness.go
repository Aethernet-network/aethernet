// Dispatcher-integrated harness variant for the cross-node byte-equality
// test bed. This file extends the direct-Apply harness in cluster.go with
// a parallel delivery path that routes events through dispatch.Dispatcher
// per node, exactly as production does.
//
// Why a parallel path (not a replacement): F4A's harness was deliberately
// scoped to the consumer-Apply boundary because the bug it characterizes
// (the per-task selection race) lives inside TVConsensusConsumer.Apply.
// Calling Apply directly was the simplest way to reproduce that race with
// zero ambiguity about whether some upstream surface might be deduping the
// emits. F4B's protocol fix, however, is logical-key admission — and that
// fix operates AT the dispatcher layer. A harness that bypasses the
// dispatcher cannot validate the fix end-to-end. Hence this file: a
// dispatcher-integrated variant whose explicit job is to (a) reproduce the
// SAME divergence on the current unfixed code path with the dispatcher in
// the middle, and (b) flip RED → GREEN once F4B's logical-key admission
// lands.
//
// What this file adds to the harness:
//
//   - Two fields on Node: Dispatcher and AdmissionStore. Wired in newNode
//     after the consumer is constructed and registered. AdmissionStore is
//     a per-node in-memory implementation (memAdmissionStore below); the
//     dispatcher's behavior under content-hash admission is identical
//     whether the backing store is BadgerDB or the in-memory map, so
//     keeping the harness store in-process avoids unnecessary disk wiring.
//   - nilDAGAnchorReader: a stub *dag.DAG that returns no tips and never
//     resolves any ancestor / event lookup. With no tips, the dispatcher's
//     currentAnchor() returns "" (empty event ID); with the stored anchor
//     empty, VerifyAnchor passes unconditionally on every admission. This
//     matches the harness's design constraint: the harness models the
//     post-DAG-converged state, the DAG itself is not what's being tested.
//   - Node.AdmitViaDispatcher(ctx, ev): the production-equivalent
//     invocation that the new transport helper calls.
//   - testnetwork.Transport.DeliverViaDispatcher: parallel to DeliverInOrder /
//     BroadcastDeterministic, but routes via Dispatcher.Admit.
//
// What this file does NOT change:
//
//   - cluster.go's existing newNode behavior is unchanged at the
//     consumer-Apply boundary. The new Dispatcher / AdmissionStore fields
//     are additive; existing tests that call node.Apply directly still
//     hit the exact same code path they always did.
//   - No new public types beyond the two struct field additions and the
//     dispatcher-routing helpers.
package cross_node

import (
	"context"
	"errors"
	"sync"

	"github.com/Aethernet-network/aethernet/internal/dispatch"
	"github.com/Aethernet-network/aethernet/internal/event"
)

// memAdmissionStore is a per-node in-memory implementation of
// dispatch.AdmissionStore. Mirrors the pattern in the dispatcher's own
// unit tests (internal/dispatch/dispatcher_test.go). Each node gets its
// own store so admission state is per-node, exactly as in production.
//
// Concurrency: protected by a single Mutex. The harness's transport is
// synchronous and serialized per node, so contention is zero in practice;
// the lock is structural, matching the production *store.Store contract.
//
// Defensive copies: like the dispatcher's test store, GetAdmission and
// AllAdmissions return deep copies (Consumers map cloned) so caller
// mutations don't leak into stored records. PutAdmission deep-copies
// inbound records for the same reason. This matches the BadgerDB-backed
// production store's semantics (every read deserializes fresh JSON).
type memAdmissionStore struct {
	mu      sync.Mutex
	records map[string]*dispatch.AdmissionRecord
}

func newMemAdmissionStore() *memAdmissionStore {
	return &memAdmissionStore{records: make(map[string]*dispatch.AdmissionRecord)}
}

func (m *memAdmissionStore) GetAdmission(key string) (*dispatch.AdmissionRecord, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	rec, ok := m.records[key]
	if !ok {
		// The dispatcher's isNotFound predicate compares err.Error() to
		// "Key not found" (the BadgerDB sentinel string). Match exactly.
		return nil, errors.New("Key not found")
	}
	return cloneAdmissionRecord(rec), nil
}

func (m *memAdmissionStore) PutAdmission(key string, rec *dispatch.AdmissionRecord) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.records[key] = cloneAdmissionRecord(rec)
	return nil
}

func (m *memAdmissionStore) DeleteAdmission(key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.records, key)
	return nil
}

func (m *memAdmissionStore) AllAdmissions() ([]*dispatch.AdmissionRecord, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]*dispatch.AdmissionRecord, 0, len(m.records))
	// safe: iteration order does not affect canonical state (non-canonical local surface, or commutative effect)
	for _, rec := range m.records {
		out = append(out, cloneAdmissionRecord(rec))
	}
	return out, nil
}

// cloneAdmissionRecord deep-copies an AdmissionRecord including the
// per-consumer status map. Used by every store accessor so caller
// mutations never alias stored state.
func cloneAdmissionRecord(rec *dispatch.AdmissionRecord) *dispatch.AdmissionRecord {
	cp := *rec
	if rec.Consumers != nil {
		cp.Consumers = make(map[string]dispatch.PerConsumerStatus, len(rec.Consumers))
		// safe: iteration order does not affect canonical state (non-canonical local surface, or commutative effect)
		for k, v := range rec.Consumers {
			cp.Consumers[k] = v
		}
	}
	if len(rec.MissingPrerequisites) > 0 {
		cp.MissingPrerequisites = append([]event.EventID(nil), rec.MissingPrerequisites...)
	}
	return &cp
}

// nilDAGAnchorReader is a stub dispatch.DAGAnchorReader. It returns no
// tips, no ancestor relationships, no events. Three implications for the
// dispatcher running over this stub:
//
//  1. currentAnchor() returns "" because Tips() returns an empty slice.
//     The reservation record's DAGAnchor field is therefore always "".
//  2. VerifyAnchor("") returns nil unconditionally per anchor.go's first
//     branch — the harness never blocks an Admit on anchor verification.
//  3. checkPrerequisites is exercised only if a registered consumer
//     declares a non-empty Prerequisites slice. TVConsensusConsumer
//     declares no prerequisites (Prerequisites returns nil), so the
//     prerequisite path is not entered for the corpus events the harness
//     drives. If a future harness corpus introduces a prerequisite-bearing
//     consumer, this stub would need to be extended; the failure mode at
//     that point is loud (errors.New("not reachable") propagated as a
//     forgery error from checkPrerequisites), not silent corruption.
//
// This matches the harness's design constraint: it models the
// post-DAG-converged state. The DAG itself is not the system under test;
// the dispatcher's admission state machine and the consumer's settlement
// invocation are.
type nilDAGAnchorReader struct{}

func (nilDAGAnchorReader) Tips() []event.EventID { return nil }

func (nilDAGAnchorReader) IsAncestor(_, _ event.EventID) (bool, error) {
	return false, errors.New("nilDAGAnchorReader: ancestor lookup not supported in harness")
}

func (nilDAGAnchorReader) Get(_ event.EventID) (*event.Event, error) {
	return nil, errors.New("nilDAGAnchorReader: event lookup not supported in harness")
}

// installDispatcher constructs and registers a Dispatcher on the node,
// using the node's existing TVConsensusConsumer. Called from newNode
// after the consumer is built so the dispatcher has something to register.
//
// The dispatcher's epochFn is a constant 1: the harness does not model
// epochs, and the only place epoch is observed is the deferral failover
// threshold check, which is unreachable here (no prerequisite-bearing
// consumers, no deferred records).
func installDispatcher(n *Node) {
	store := newMemAdmissionStore()
	d := dispatch.NewDispatcher(store, nilDAGAnchorReader{}, func() uint64 { return 1 })
	if err := d.Register(n.Consumer); err != nil {
		// Register only fails on duplicate name or invalid Type, both of
		// which are programming errors at this construction site (the
		// dispatcher is fresh, no other consumer has been registered).
		// Panic surfaces such bugs immediately rather than producing a
		// silent miswire that would only show up as "tests pass but the
		// dispatcher path was never exercised."
		panic("cross_node: dispatcher Register failed: " + err.Error())
	}
	n.Dispatcher = d
	n.AdmissionStore = store
}

// AdmitViaDispatcher routes ev through this node's Dispatcher.Admit,
// which is the production-equivalent invocation site for canonical events
// arriving from the recognition fabric. Used by the dispatcher-integrated
// transport variant.
//
// Returns the dispatcher's error directly. The dispatcher returns nil for
// successful admission (including the no-op short-circuit when a
// consumer's Apply returns nil for an already-terminal task), and returns
// non-nil only for storage failures, anchor-verification failures, or
// prerequisite-forgery — none of which arise in the harness's current
// corpora. The harness's primary signal is the post-delivery cluster
// snapshot, not per-Admit error returns; mirrors how cluster_test.go's
// runScenario ignores DeliverInOrder per-event errors.
func (n *Node) AdmitViaDispatcher(ctx context.Context, ev *event.Event) error {
	if n.Dispatcher == nil {
		return errors.New("cross_node: AdmitViaDispatcher called before installDispatcher")
	}
	return n.Dispatcher.Admit(ctx, ev)
}
