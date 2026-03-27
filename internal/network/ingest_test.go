package network_test

import (
	"testing"
	"time"

	"github.com/Aethernet-network/aethernet/internal/event"
	"github.com/Aethernet-network/aethernet/internal/network"
)

func testHeader(id string) network.EventHeader {
	return network.EventHeader{
		EventID:         event.EventID(id),
		Type:            event.EventTypeTransfer,
		CausalRefs:      []event.EventID{},
		AgentID:         "agent-1",
		CausalTimestamp: 1,
		BodyCommitment:  "abc123",
	}
}

func TestAdmitHeader_CreatesAnnouncedState(t *testing.T) {
	im := network.NewIngestManager(network.DefaultFastPathConfig())

	ok := im.AdmitHeader("peer-1", testHeader("ev-1"))
	if !ok {
		t.Fatal("AdmitHeader should return true for new header")
	}

	tr := im.GetTracking("ev-1")
	if tr == nil {
		t.Fatal("GetTracking should return non-nil for admitted header")
	}
	if tr.Stage != network.StageAnnounced {
		t.Errorf("Stage = %v; want Announced", tr.Stage)
	}
	if tr.SourcePeer != "peer-1" {
		t.Errorf("SourcePeer = %q; want peer-1", tr.SourcePeer)
	}
	if tr.ReceivedAt.IsZero() {
		t.Error("ReceivedAt should be set")
	}
}

func TestAdmitHeader_DuplicateDeduped(t *testing.T) {
	im := network.NewIngestManager(network.DefaultFastPathConfig())

	ok1 := im.AdmitHeader("peer-1", testHeader("ev-dup"))
	ok2 := im.AdmitHeader("peer-2", testHeader("ev-dup"))

	if !ok1 {
		t.Fatal("first admit should succeed")
	}
	if ok2 {
		t.Fatal("duplicate admit should return false")
	}
	if im.TrackedCount() != 1 {
		t.Errorf("TrackedCount = %d; want 1", im.TrackedCount())
	}
}

func TestAdmitHeader_EnqueuesAnnounce(t *testing.T) {
	im := network.NewIngestManager(network.DefaultFastPathConfig())

	im.AdmitHeader("peer-1", testHeader("ev-q"))

	select {
	case id := <-im.AnnounceQ():
		if id != "ev-q" {
			t.Errorf("AnnounceQ event = %q; want ev-q", id)
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("AnnounceQ should have an item")
	}
}

func TestStageTransitions_ForwardOnly(t *testing.T) {
	im := network.NewIngestManager(network.DefaultFastPathConfig())
	im.AdmitHeader("peer-1", testHeader("ev-stages"))

	// Announced → Completed
	if !im.MarkCompleted("ev-stages") {
		t.Fatal("MarkCompleted should succeed from Announced")
	}
	tr := im.GetTracking("ev-stages")
	if tr.Stage != network.StageCompleted {
		t.Errorf("Stage = %v; want Completed", tr.Stage)
	}
	if tr.CompletedAt.IsZero() {
		t.Error("CompletedAt should be set after MarkCompleted")
	}

	// Completed → Validated
	if !im.MarkValidated("ev-stages") {
		t.Fatal("MarkValidated should succeed from Completed")
	}
	tr = im.GetTracking("ev-stages")
	if tr.Stage != network.StageValidated {
		t.Errorf("Stage = %v; want Validated", tr.Stage)
	}

	// Validated → Materialized
	if !im.MarkMaterialized("ev-stages") {
		t.Fatal("MarkMaterialized should succeed from Validated")
	}
	tr = im.GetTracking("ev-stages")
	if tr.Stage != network.StageMaterialized {
		t.Errorf("Stage = %v; want Materialized", tr.Stage)
	}
}

func TestStageTransitions_CannotGoBackward(t *testing.T) {
	im := network.NewIngestManager(network.DefaultFastPathConfig())
	im.AdmitHeader("peer-1", testHeader("ev-back"))
	im.MarkCompleted("ev-back")

	// Completed → Announced (backward) should fail.
	tr := im.GetTracking("ev-back")
	if tr.CanAdvanceTo(network.StageAnnounced) {
		t.Error("should not be able to advance backward to Announced")
	}

	// Completed → Completed (same) should fail.
	if tr.CanAdvanceTo(network.StageCompleted) {
		t.Error("should not be able to advance to same stage")
	}
}

func TestStageTransitions_UnknownEventFails(t *testing.T) {
	im := network.NewIngestManager(network.DefaultFastPathConfig())

	if im.MarkCompleted("nonexistent") {
		t.Error("MarkCompleted on unknown event should return false")
	}
	if im.MarkValidated("nonexistent") {
		t.Error("MarkValidated on unknown event should return false")
	}
	if im.MarkMaterialized("nonexistent") {
		t.Error("MarkMaterialized on unknown event should return false")
	}
}

func TestStageCount(t *testing.T) {
	im := network.NewIngestManager(network.DefaultFastPathConfig())

	im.AdmitHeader("p", testHeader("a"))
	im.AdmitHeader("p", testHeader("b"))
	im.AdmitHeader("p", testHeader("c"))
	im.MarkCompleted("b")

	counts := im.StageCount()
	if counts[network.StageAnnounced] != 2 {
		t.Errorf("Announced = %d; want 2", counts[network.StageAnnounced])
	}
	if counts[network.StageCompleted] != 1 {
		t.Errorf("Completed = %d; want 1", counts[network.StageCompleted])
	}
}

func TestTTLExpiration_AnnouncedEviction(t *testing.T) {
	cfg := network.FastPathConfig{
		AnnounceTTL:          1 * time.Millisecond,
		CompleteTTL:          1 * time.Hour,
		GCInterval:           50 * time.Millisecond,
		MaxTracked:           100,
		MaxAnnounceQueueSize: 64,
		MaxRelayQueueSize:    64,
	}
	im := network.NewIngestManager(cfg)
	im.Start()
	defer im.Stop()

	im.AdmitHeader("p", testHeader("stale-1"))
	im.AdmitHeader("p", testHeader("stale-2"))

	// Wait for TTL + GC cycle.
	time.Sleep(200 * time.Millisecond)

	if im.TrackedCount() != 0 {
		t.Errorf("TrackedCount = %d; want 0 (stale items should be GC'd)", im.TrackedCount())
	}
}

func TestTTLExpiration_CompletedEviction(t *testing.T) {
	cfg := network.FastPathConfig{
		AnnounceTTL:          1 * time.Hour,
		CompleteTTL:          1 * time.Millisecond,
		GCInterval:           50 * time.Millisecond,
		MaxTracked:           100,
		MaxAnnounceQueueSize: 64,
		MaxRelayQueueSize:    64,
	}
	im := network.NewIngestManager(cfg)
	im.Start()
	defer im.Stop()

	im.AdmitHeader("p", testHeader("comp-stale"))
	im.MarkCompleted("comp-stale")

	time.Sleep(200 * time.Millisecond)

	tr := im.GetTracking("comp-stale")
	if tr != nil {
		t.Error("completed stale item should be GC'd")
	}
}

func TestCapacityEviction(t *testing.T) {
	cfg := network.DefaultFastPathConfig()
	cfg.MaxTracked = 3
	im := network.NewIngestManager(cfg)

	im.AdmitHeader("p", testHeader("first"))
	time.Sleep(time.Millisecond) // ensure ordering
	im.AdmitHeader("p", testHeader("second"))
	time.Sleep(time.Millisecond)
	im.AdmitHeader("p", testHeader("third"))
	time.Sleep(time.Millisecond)

	// At capacity. Fourth should evict the oldest.
	ok := im.AdmitHeader("p", testHeader("fourth"))
	if !ok {
		t.Fatal("fourth admit should succeed after evicting oldest")
	}

	if im.IsTracked("first") {
		t.Error("oldest entry (first) should have been evicted")
	}
	if im.TrackedCount() != 3 {
		t.Errorf("TrackedCount = %d; want 3", im.TrackedCount())
	}
}

func TestRemove(t *testing.T) {
	im := network.NewIngestManager(network.DefaultFastPathConfig())
	im.AdmitHeader("p", testHeader("rm"))
	im.Remove("rm")
	if im.IsTracked("rm") {
		t.Error("event should not be tracked after Remove")
	}
}

func TestEventStage_String(t *testing.T) {
	tests := []struct {
		stage network.EventStage
		want  string
	}{
		{network.StageAnnounced, "Announced"},
		{network.StageCompleted, "Completed"},
		{network.StageValidated, "Validated"},
		{network.StageMaterialized, "Materialized"},
		{0, "Unknown"},
	}
	for _, tt := range tests {
		if got := tt.stage.String(); got != tt.want {
			t.Errorf("EventStage(%d).String() = %q; want %q", tt.stage, got, tt.want)
		}
	}
}
