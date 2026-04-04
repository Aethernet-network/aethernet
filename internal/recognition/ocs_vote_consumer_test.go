package recognition_test

import (
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/Aethernet-network/aethernet/internal/crypto"
	"github.com/Aethernet-network/aethernet/internal/event"
	"github.com/Aethernet-network/aethernet/internal/recognition"
)

// mockOCSVoteAcceptor records AcceptPeerVote calls.
type mockOCSVoteAcceptor struct {
	mu       sync.Mutex
	votes    []acceptedVote
	pending  map[event.EventID]bool
}

type acceptedVote struct {
	TargetID event.EventID
	VoterID  crypto.AgentID
	Verdict  bool
}

func newMockVoteAcceptor() *mockOCSVoteAcceptor {
	return &mockOCSVoteAcceptor{
		pending: make(map[event.EventID]bool),
	}
}

func (m *mockOCSVoteAcceptor) AcceptPeerVote(eventID event.EventID, voterID crypto.AgentID, verdict bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.votes = append(m.votes, acceptedVote{eventID, voterID, verdict})
	return nil
}

func (m *mockOCSVoteAcceptor) IsPending(eventID event.EventID) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.pending[eventID]
}

func (m *mockOCSVoteAcceptor) setPending(id event.EventID) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.pending[id] = true
}

func (m *mockOCSVoteAcceptor) voteCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.votes)
}

func makeVoteEvent(targetID, voterID string, verdict bool) *event.Event {
	vp := recognition.VotePayload{
		Version:       1,
		TargetEventID: targetID,
		VoterID:       voterID,
		Verdict:       "accepted",
		VerifiedValue: 5000,
		Timestamp:     time.Now().Unix(),
	}
	if !verdict {
		vp.Verdict = "rejected"
	}
	payload, _ := json.Marshal(vp)
	return &event.Event{
		ID:              event.EventID("vote-" + voterID + "-" + targetID),
		Type:            event.EventTypeVerificationVote,
		Payload:         payload,
		SettlementState: event.SettlementOptimistic,
	}
}

// TestOCSVoteConsumer_LocalVoteRecognized verifies that a locally published
// VerificationVote is recognized by the fabric.
func TestOCSVoteConsumer_LocalVoteRecognized(t *testing.T) {
	acceptor := newMockVoteAcceptor()
	acceptor.setPending("target-evt-1")

	consumer := recognition.NewOCSVoteConsumer(acceptor)

	store := recognition.NewMemoryIndexStore()
	idx := recognition.NewIndex(store)
	rm := makeTestReadModel()

	bus := recognition.NewBus(recognition.BusConfig{QueueSize: 64, Workers: 1}, idx, rm)
	bus.Register(consumer)
	bus.Start()
	defer bus.Stop()

	// Target event is in DAG and OCS-pending.
	targetEv := &event.Event{ID: "target-evt-1", Type: event.EventTypeTransfer}
	rm.events[targetEv.ID] = targetEv

	voteEv := makeVoteEvent("target-evt-1", "voter-1", true)
	rm.events[voteEv.ID] = voteEv

	recognition.EmitCommit(bus, voteEv, recognition.SourceLocal, false)

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if acceptor.voteCount() > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	if acceptor.voteCount() != 1 {
		t.Fatalf("AcceptPeerVote calls = %d; want 1", acceptor.voteCount())
	}
}

// TestOCSVoteConsumer_RemoteVoteRecognized verifies that a remotely synced
// VerificationVote DAG event is recognized by the fabric.
func TestOCSVoteConsumer_RemoteVoteRecognized(t *testing.T) {
	acceptor := newMockVoteAcceptor()
	acceptor.setPending("target-evt-2")

	consumer := recognition.NewOCSVoteConsumer(acceptor)

	store := recognition.NewMemoryIndexStore()
	idx := recognition.NewIndex(store)
	rm := makeTestReadModel()

	bus := recognition.NewBus(recognition.BusConfig{QueueSize: 64, Workers: 1}, idx, rm)
	bus.Register(consumer)
	bus.Start()
	defer bus.Stop()

	targetEv := &event.Event{ID: "target-evt-2", Type: event.EventTypeTransfer}
	rm.events[targetEv.ID] = targetEv

	voteEv := makeVoteEvent("target-evt-2", "voter-2", true)
	rm.events[voteEv.ID] = voteEv

	recognition.EmitCommit(bus, voteEv, recognition.SourceRemoteFastPath, false)

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if acceptor.voteCount() > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	if acceptor.voteCount() != 1 {
		t.Fatalf("AcceptPeerVote calls = %d; want 1", acceptor.voteCount())
	}
}

// TestOCSVoteConsumer_DeferredWhenTargetNotPending verifies that a vote
// arriving before its target is OCS-pending is deferred, not dropped.
func TestOCSVoteConsumer_DeferredWhenTargetNotPending(t *testing.T) {
	acceptor := newMockVoteAcceptor()
	// Target is NOT pending yet.

	consumer := recognition.NewOCSVoteConsumer(acceptor)

	store := recognition.NewMemoryIndexStore()
	idx := recognition.NewIndex(store)
	rm := makeTestReadModel()

	bus := recognition.NewBus(recognition.BusConfig{QueueSize: 64, Workers: 1}, idx, rm)
	bus.Register(consumer)
	bus.Start()
	defer bus.Stop()

	// Target exists in DAG but not in OCS pending.
	targetEv := &event.Event{ID: "target-evt-3", Type: event.EventTypeTransfer}
	rm.events[targetEv.ID] = targetEv

	voteEv := makeVoteEvent("target-evt-3", "voter-3", true)
	rm.events[voteEv.ID] = voteEv

	recognition.EmitCommit(bus, voteEv, recognition.SourceRemoteLegacy, false)

	time.Sleep(200 * time.Millisecond)

	// Vote should NOT have been counted — target not pending.
	if acceptor.voteCount() != 0 {
		t.Errorf("AcceptPeerVote calls = %d; want 0 (target not pending)", acceptor.voteCount())
	}

	// But it should be deferred in the index.
	state, err := idx.Get("ocs_vote", voteEv.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !state.Recognized {
		t.Error("vote should be recognized")
	}
	if state.Ready {
		t.Error("vote should NOT be ready (deferred)")
	}
	expectedKey := recognition.PrerequisiteKeyOCSPending("target-evt-3")
	if state.PrerequisiteKey != expectedKey {
		t.Errorf("PrerequisiteKey = %q; want %q", state.PrerequisiteKey, expectedKey)
	}
}

// TestOCSVoteConsumer_TargetActivatesDeferredVote verifies that when a
// target event becomes OCS-pending, the deferred vote is activated.
func TestOCSVoteConsumer_TargetActivatesDeferredVote(t *testing.T) {
	acceptor := newMockVoteAcceptor()
	mockSubmitter := &mockOCSSubmitter{}

	submitConsumer := recognition.NewOCSSubmitConsumer(mockSubmitter)
	voteConsumer := recognition.NewOCSVoteConsumer(acceptor)

	store := recognition.NewMemoryIndexStore()
	idx := recognition.NewIndex(store)
	rm := makeTestReadModel()

	bus := recognition.NewBus(recognition.BusConfig{QueueSize: 64, Workers: 1}, idx, rm)
	activator := recognition.NewActivator(idx, bus)
	submitConsumer.SetActivator(activator)

	bus.Register(submitConsumer)
	bus.Register(voteConsumer)
	bus.Start()
	defer bus.Stop()

	// Both events are in the DAG.
	targetEv := &event.Event{
		ID:              "target-evt-4",
		Type:            event.EventTypeTransfer,
		SettlementState: event.SettlementOptimistic,
	}
	rm.events[targetEv.ID] = targetEv

	voteEv := makeVoteEvent("target-evt-4", "voter-4", true)
	rm.events[voteEv.ID] = voteEv

	// Step 1: Vote arrives FIRST (target not yet OCS-pending).
	recognition.EmitCommit(bus, voteEv, recognition.SourceRemoteFastPath, false)

	// Wait for vote to be deferred.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		s, err := idx.Get("ocs_vote", voteEv.ID)
		if err == nil && s.Recognized && !s.Ready {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	// Vote should be deferred, not counted.
	if acceptor.voteCount() != 0 {
		t.Fatalf("vote should be deferred, got %d AcceptPeerVote calls", acceptor.voteCount())
	}

	// Step 2: Target arrives — ocs_submit consumes it and signals activation.
	// Make the target OCS-pending so the vote's Ready check passes.
	acceptor.setPending("target-evt-4")
	recognition.EmitCommit(bus, targetEv, recognition.SourceRemoteFastPath, false)

	// Wait for activation cascade: submit → activator.Signal → vote consumes.
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if acceptor.voteCount() > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	if acceptor.voteCount() != 1 {
		t.Fatalf("AcceptPeerVote calls = %d; want 1 (vote should be activated)", acceptor.voteCount())
	}
}

// TestOCSVoteConsumer_DuplicateVoteNotDoubleCounted verifies that the same
// vote arriving via both MsgVote and DAG event does not double-count.
func TestOCSVoteConsumer_DuplicateVoteNotDoubleCounted(t *testing.T) {
	acceptor := newMockVoteAcceptor()
	acceptor.setPending("target-evt-5")

	consumer := recognition.NewOCSVoteConsumer(acceptor)

	store := recognition.NewMemoryIndexStore()
	idx := recognition.NewIndex(store)
	rm := makeTestReadModel()

	bus := recognition.NewBus(recognition.BusConfig{QueueSize: 64, Workers: 1}, idx, rm)
	bus.Register(consumer)
	bus.Start()
	defer bus.Stop()

	targetEv := &event.Event{ID: "target-evt-5", Type: event.EventTypeTransfer}
	rm.events[targetEv.ID] = targetEv

	voteEv := makeVoteEvent("target-evt-5", "voter-5", true)
	rm.events[voteEv.ID] = voteEv

	// Emit twice — simulates MsgVote + DAG sync both triggering.
	recognition.EmitCommit(bus, voteEv, recognition.SourceLocal, false)
	recognition.EmitCommit(bus, voteEv, recognition.SourceRemoteFastPath, false)

	time.Sleep(200 * time.Millisecond)

	// Recognition index ensures idempotency — only one AcceptPeerVote call.
	if acceptor.voteCount() != 1 {
		t.Errorf("AcceptPeerVote calls = %d; want 1 (idempotent)", acceptor.voteCount())
	}
}

// TestOCSVoteConsumer_ReplayReconstructsState verifies that replayed vote
// events are recognized consistently through the fabric.
func TestOCSVoteConsumer_ReplayReconstructsState(t *testing.T) {
	acceptor := newMockVoteAcceptor()
	acceptor.setPending("target-evt-6")

	consumer := recognition.NewOCSVoteConsumer(acceptor)

	store := recognition.NewMemoryIndexStore()
	idx := recognition.NewIndex(store)
	rm := makeTestReadModel()

	bus := recognition.NewBus(recognition.BusConfig{QueueSize: 64, Workers: 1}, idx, rm)
	bus.Register(consumer)
	bus.Start()
	defer bus.Stop()

	targetEv := &event.Event{ID: "target-evt-6", Type: event.EventTypeTransfer}
	rm.events[targetEv.ID] = targetEv

	voteEv := makeVoteEvent("target-evt-6", "voter-6", true)
	rm.events[voteEv.ID] = voteEv

	// Replay emission.
	recognition.EmitCommit(bus, voteEv, recognition.SourceReplay, true)

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if acceptor.voteCount() > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	if acceptor.voteCount() != 1 {
		t.Fatalf("AcceptPeerVote calls = %d; want 1 (replay)", acceptor.voteCount())
	}
}

// TestOCSVoteConsumer_InvalidPayloadNotDeferred verifies that a vote with
// an unparseable payload is recognized but not deferred (consumed as no-op).
func TestOCSVoteConsumer_InvalidPayloadNotDeferred(t *testing.T) {
	acceptor := newMockVoteAcceptor()
	consumer := recognition.NewOCSVoteConsumer(acceptor)

	store := recognition.NewMemoryIndexStore()
	idx := recognition.NewIndex(store)
	rm := makeTestReadModel()

	bus := recognition.NewBus(recognition.BusConfig{QueueSize: 64, Workers: 1}, idx, rm)
	bus.Register(consumer)
	bus.Start()
	defer bus.Stop()

	// Event with invalid payload.
	badEv := &event.Event{
		ID:      "vote-bad-payload",
		Type:    event.EventTypeVerificationVote,
		Payload: json.RawMessage(`{invalid}`),
	}
	rm.events[badEv.ID] = badEv

	recognition.EmitCommit(bus, badEv, recognition.SourceRemoteLegacy, false)

	time.Sleep(200 * time.Millisecond)

	// Should be recognized and ready (no-op), NOT deferred.
	state, err := idx.Get("ocs_vote", badEv.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !state.Recognized {
		t.Error("should be recognized")
	}
	if !state.Ready {
		t.Error("invalid payload should be marked ready (no-op), not deferred")
	}
	if acceptor.voteCount() != 0 {
		t.Error("AcceptPeerVote should not be called for invalid payload")
	}
}
