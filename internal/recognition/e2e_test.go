package recognition_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/Aethernet-network/aethernet/internal/event"
	"github.com/Aethernet-network/aethernet/internal/recognition"
)

// TestE2E_FullTaskSettlementPipeline proves the complete lifecycle through
// the recognition fabric:
// 1. Transfer event → OCS pending via ocs_submit
// 2. Vote event → ocs_vote (deferred until target pending, then activated)
// 3. TaskSubmitted → task_lifecycle + evidence_readiness
// 4. Settlement → settlement consumer
// All paths converge through the same bus regardless of source.
func TestE2E_FullTaskSettlementPipeline(t *testing.T) {
	store := recognition.NewMemoryIndexStore()
	idx := recognition.NewIndex(store)
	rm := makeTestReadModel()

	bus := recognition.NewBus(recognition.BusConfig{QueueSize: 128, Workers: 2}, idx, rm)

	// Wire all consumers.
	ocsSubmitter := &mockOCSSubmitter{}
	ocsSubmitConsumer := recognition.NewOCSSubmitConsumer(ocsSubmitter)
	activator := recognition.NewActivator(idx, bus)
	ocsSubmitConsumer.SetActivator(activator)

	ocsAcceptor := newMockVoteAcceptor()
	ocsVoteConsumer := recognition.NewOCSVoteConsumer(ocsAcceptor)

	taskApplier := &mockTaskApplier{}
	taskConsumer := recognition.NewTaskLifecycleConsumer(taskApplier)

	evidenceMarker := &mockEvidenceMarker{}
	evidenceConsumer := recognition.NewEvidenceReadinessConsumer(evidenceMarker, nil)

	settlementApplier := newMockSettlementApplier()
	settlementConsumer := recognition.NewSettlementConsumer(settlementApplier)

	bus.Register(ocsSubmitConsumer)
	bus.Register(ocsVoteConsumer)
	bus.Register(taskConsumer)
	bus.Register(evidenceConsumer)
	bus.Register(settlementConsumer)
	bus.Start()
	defer bus.Stop()

	// Step 1: Transfer event committed → ocs_submit recognizes it.
	transferEv := &event.Event{
		ID:              "transfer-001",
		Type:            event.EventTypeTransfer,
		SettlementState: event.SettlementOptimistic,
	}
	rm.events[transferEv.ID] = transferEv
	ocsAcceptor.setPending("transfer-001")

	recognition.EmitCommit(bus, transferEv, recognition.SourceLocal, false)

	waitFor(t, func() bool { return ocsSubmitter.callCount() >= 1 }, "ocs submit")
	if ocsSubmitter.callCount() != 1 {
		t.Fatalf("ocs submit = %d; want 1", ocsSubmitter.callCount())
	}

	// Step 2: Vote for the transfer arrives from a remote peer.
	// The target is already pending, so the vote should be immediately ready.
	voteEv := makeVoteEvent("transfer-001", "validator-1", true)
	rm.events[voteEv.ID] = voteEv

	recognition.EmitCommit(bus, voteEv, recognition.SourceRemoteFastPath, false)

	waitFor(t, func() bool { return ocsAcceptor.voteCount() >= 1 }, "vote accept")
	if ocsAcceptor.voteCount() != 1 {
		t.Fatalf("vote count = %d; want 1", ocsAcceptor.voteCount())
	}

	// Step 3: TaskPosted + TaskSubmitted with evidence.
	taskPostedEv := &event.Event{
		ID:   "task-posted-001",
		Type: event.EventTypeTaskPosted,
	}
	rm.events[taskPostedEv.ID] = taskPostedEv
	recognition.EmitCommit(bus, taskPostedEv, recognition.SourceRemoteLegacy, false)

	taskSubmittedPayload, _ := json.Marshal(map[string]interface{}{
		"task_id":            "task-001",
		"evidence_body_hash": "",
	})
	taskSubmittedEv := &event.Event{
		ID:              "task-submitted-001",
		Type:            event.EventTypeTaskSubmitted,
		Payload:         taskSubmittedPayload,
		SettlementState: event.SettlementOptimistic,
	}
	rm.events[taskSubmittedEv.ID] = taskSubmittedEv
	recognition.EmitCommit(bus, taskSubmittedEv, recognition.SourceRepair, false)

	waitFor(t, func() bool { return taskApplier.callCount() >= 2 }, "task apply")
	waitFor(t, func() bool { return evidenceMarker.markCount() >= 1 }, "evidence mark")

	if taskApplier.callCount() != 2 {
		t.Errorf("task apply = %d; want 2 (posted + submitted)", taskApplier.callCount())
	}
	if evidenceMarker.markCount() != 1 {
		t.Errorf("evidence mark = %d; want 1", evidenceMarker.markCount())
	}

	// Step 4: Settlement event.
	settlementPayload, _ := json.Marshal(map[string]interface{}{
		"v":               1,
		"target_event_id": "transfer-001",
		"verdict":         "accepted",
		"verified_value":  5000,
	})
	settlementEv := &event.Event{
		ID:              "settlement-001",
		Type:            event.EventTypeSettlement,
		Payload:         settlementPayload,
		SettlementState: event.SettlementOptimistic,
	}
	rm.events[settlementEv.ID] = settlementEv
	recognition.EmitCommit(bus, settlementEv, recognition.SourceLocal, false)

	waitFor(t, func() bool { return settlementApplier.totalApplies() >= 1 }, "settlement apply")

	if settlementApplier.totalApplies() != 1 {
		t.Fatalf("settlement applies = %d; want 1", settlementApplier.totalApplies())
	}

	// Verify index stats.
	stats := idx.Stats()
	if stats.Total == 0 {
		t.Error("index should have entries")
	}
	if stats.Deferred != 0 {
		t.Errorf("deferred = %d; want 0 (all should be resolved)", stats.Deferred)
	}
}

// TestE2E_VoteDeferralAndActivation proves the cross-node vote aggregation
// fix: votes arriving before their target is OCS-pending are deferred and
// later activated when the target lands.
func TestE2E_VoteDeferralAndActivation(t *testing.T) {
	store := recognition.NewMemoryIndexStore()
	idx := recognition.NewIndex(store)
	rm := makeTestReadModel()

	bus := recognition.NewBus(recognition.BusConfig{QueueSize: 128, Workers: 2}, idx, rm)

	ocsSubmitter := &mockOCSSubmitter{}
	ocsSubmitConsumer := recognition.NewOCSSubmitConsumer(ocsSubmitter)
	activator := recognition.NewActivator(idx, bus)
	ocsSubmitConsumer.SetActivator(activator)

	ocsAcceptor := newMockVoteAcceptor()
	voteConsumer := recognition.NewOCSVoteConsumer(ocsAcceptor)

	bus.Register(ocsSubmitConsumer)
	bus.Register(voteConsumer)
	bus.Start()
	defer bus.Stop()

	// Both events exist in DAG.
	transferEv := &event.Event{
		ID:              "target-deferred",
		Type:            event.EventTypeTransfer,
		SettlementState: event.SettlementOptimistic,
	}
	rm.events[transferEv.ID] = transferEv

	voteEv := makeVoteEvent("target-deferred", "voter-deferred", true)
	rm.events[voteEv.ID] = voteEv

	// Step 1: Vote arrives FIRST — target not OCS-pending.
	recognition.EmitCommit(bus, voteEv, recognition.SourceRemoteFastPath, false)

	// Wait for vote to be deferred.
	waitFor(t, func() bool {
		s, _ := idx.Get("ocs_vote", voteEv.ID)
		return s != nil && s.Recognized && !s.Ready
	}, "vote deferred")

	if ocsAcceptor.voteCount() != 0 {
		t.Fatalf("vote should be deferred, got %d", ocsAcceptor.voteCount())
	}

	// Step 2: Target arrives — ocs_submit processes it, activator signals.
	ocsAcceptor.setPending("target-deferred")
	recognition.EmitCommit(bus, transferEv, recognition.SourceLocal, false)

	// Wait for activation cascade.
	waitFor(t, func() bool { return ocsAcceptor.voteCount() >= 1 }, "vote activated")

	if ocsAcceptor.voteCount() != 1 {
		t.Fatalf("vote count after activation = %d; want 1", ocsAcceptor.voteCount())
	}

	// Index should show both recognized and ready.
	stats := idx.Stats()
	if stats.Deferred != 0 {
		t.Errorf("deferred = %d; want 0 after activation", stats.Deferred)
	}
}

// TestE2E_ReplayRestoresRecognitionState proves that the recognition index
// survives restart/reload and prevents double-processing.
func TestE2E_ReplayRestoresRecognitionState(t *testing.T) {
	// Phase 1: Simulate initial run with some recognized events.
	store := recognition.NewMemoryIndexStore()
	idx1 := recognition.NewIndex(store)
	rm := makeTestReadModel()

	bus1 := recognition.NewBus(recognition.BusConfig{QueueSize: 64, Workers: 1}, idx1, rm)

	ocsSubmitter := &mockOCSSubmitter{}
	consumer := recognition.NewOCSSubmitConsumer(ocsSubmitter)
	bus1.Register(consumer)
	bus1.Start()

	ev := &event.Event{
		ID:              "replay-target",
		Type:            event.EventTypeTransfer,
		SettlementState: event.SettlementOptimistic,
	}
	rm.events[ev.ID] = ev
	recognition.EmitCommit(bus1, ev, recognition.SourceLocal, false)

	waitFor(t, func() bool { return ocsSubmitter.callCount() >= 1 }, "initial recognize")
	bus1.Stop()

	// Phase 2: Simulate restart — new index from same store.
	idx2 := recognition.NewIndex(store)
	if err := idx2.LoadFromStore(); err != nil {
		t.Fatalf("LoadFromStore: %v", err)
	}

	// Verify state survived.
	state, err := idx2.Get("ocs_submit", "replay-target")
	if err != nil {
		t.Fatalf("Get after reload: %v", err)
	}
	if !state.Recognized || !state.Ready {
		t.Error("recognition state should survive restart")
	}

	// Phase 3: Replay the event — should not double-process.
	ocsSubmitter2 := &mockOCSSubmitter{}
	consumer2 := recognition.NewOCSSubmitConsumer(ocsSubmitter2)
	bus2 := recognition.NewBus(recognition.BusConfig{QueueSize: 64, Workers: 1}, idx2, rm)
	bus2.Register(consumer2)
	bus2.Start()
	defer bus2.Stop()

	recognition.EmitCommit(bus2, ev, recognition.SourceReplay, true)
	time.Sleep(200 * time.Millisecond)

	// Already recognized + ready in index → no new SubmitFromSync call.
	if ocsSubmitter2.callCount() != 0 {
		t.Errorf("replay should not double-process; got %d calls", ocsSubmitter2.callCount())
	}
}

// TestE2E_MultipleSourcesNoDuplication proves that the same event arriving
// from different source paths results in exactly one recognition.
func TestE2E_MultipleSourcesNoDuplication(t *testing.T) {
	store := recognition.NewMemoryIndexStore()
	idx := recognition.NewIndex(store)
	rm := makeTestReadModel()

	bus := recognition.NewBus(recognition.BusConfig{QueueSize: 64, Workers: 2}, idx, rm)

	ocsSubmitter := &mockOCSSubmitter{}
	consumer := recognition.NewOCSSubmitConsumer(ocsSubmitter)
	bus.Register(consumer)
	bus.Start()
	defer bus.Stop()

	ev := &event.Event{
		ID:              "multi-source",
		Type:            event.EventTypeTransfer,
		SettlementState: event.SettlementOptimistic,
	}
	rm.events[ev.ID] = ev

	// Emit from 4 different sources.
	recognition.EmitCommit(bus, ev, recognition.SourceLocal, false)
	recognition.EmitCommit(bus, ev, recognition.SourceRemoteFastPath, false)
	recognition.EmitCommit(bus, ev, recognition.SourceRemoteLegacy, false)
	recognition.EmitCommit(bus, ev, recognition.SourceRepair, false)

	time.Sleep(300 * time.Millisecond)

	// Only one SubmitFromSync call despite 4 emissions.
	if ocsSubmitter.callCount() != 1 {
		t.Errorf("SubmitFromSync calls = %d; want 1 (idempotent across sources)", ocsSubmitter.callCount())
	}
}

// TestE2E_IndexStats provides observability verification.
func TestE2E_IndexStats(t *testing.T) {
	store := recognition.NewMemoryIndexStore()
	idx := recognition.NewIndex(store)
	rm := makeTestReadModel()

	bus := recognition.NewBus(recognition.BusConfig{QueueSize: 64, Workers: 1}, idx, rm)

	ocsAcceptor := newMockVoteAcceptor()
	voteConsumer := recognition.NewOCSVoteConsumer(ocsAcceptor)
	bus.Register(voteConsumer)

	ocsSubmitter := &mockOCSSubmitter{}
	submitConsumer := recognition.NewOCSSubmitConsumer(ocsSubmitter)
	bus.Register(submitConsumer)
	bus.Start()
	defer bus.Stop()

	// Transfer (optimistic) → recognized + ready.
	transferEv := &event.Event{
		ID:              "stats-transfer",
		Type:            event.EventTypeTransfer,
		SettlementState: event.SettlementOptimistic,
	}
	rm.events[transferEv.ID] = transferEv
	recognition.EmitCommit(bus, transferEv, recognition.SourceLocal, false)

	// Vote without target pending → recognized + deferred.
	targetEv := &event.Event{ID: "stats-target", Type: event.EventTypeTransfer}
	rm.events[targetEv.ID] = targetEv
	voteEv := makeVoteEvent("stats-target", "stats-voter", true)
	rm.events[voteEv.ID] = voteEv
	recognition.EmitCommit(bus, voteEv, recognition.SourceRemoteFastPath, false)

	time.Sleep(300 * time.Millisecond)

	stats := idx.Stats()
	if stats.Total < 2 {
		t.Errorf("total = %d; want >= 2", stats.Total)
	}
	if stats.Recognized < 2 {
		t.Errorf("recognized = %d; want >= 2", stats.Recognized)
	}
	// At least 1 ready (ocs_submit for transfer) and 1 deferred (ocs_vote).
	if stats.Ready < 1 {
		t.Errorf("ready = %d; want >= 1", stats.Ready)
	}
	if stats.Deferred < 1 {
		t.Errorf("deferred = %d; want >= 1 (vote should be deferred)", stats.Deferred)
	}
}

// waitFor polls a condition with a 2-second deadline.
func waitFor(t *testing.T, cond func() bool, label string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for: %s", label)
}
