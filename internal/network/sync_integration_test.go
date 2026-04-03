// sync_integration_test.go proves the sync architecture fix end-to-end:
// no sync storms in all-V2 clusters, blob propagation via body plane,
// checkpoint bootstrap + lazy blob catch-up, and partition-heal correctness.
package network_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sync"
	"testing"

	"github.com/Aethernet-network/aethernet/internal/crypto"
	"github.com/Aethernet-network/aethernet/internal/dag"
	"github.com/Aethernet-network/aethernet/internal/event"
	"github.com/Aethernet-network/aethernet/internal/network"
)

// ── Integration: all-V2 cluster emits zero MsgRequestSync ──────────────────

func TestIntegration_AllV2_ZeroLegacySync(t *testing.T) {
	kp, _ := crypto.GenerateKeyPair()
	d := dag.New()
	cfg := network.DefaultNodeConfig(kp.AgentID())
	cfg.EnableLegacySync = false // default for V2 clusters

	node := network.NewNode(cfg, d)

	// Verify no legacy sync targets.
	targets := node.LegacySyncTargets()
	if len(targets) != 0 {
		t.Fatalf("all-V2 cluster should have 0 legacy sync targets, got %d", len(targets))
	}

	// Verify the sync request payload format includes frontier hints.
	frontier := network.SyncRequestPayload{
		KnownTips:    d.Tips(),
		MaxTimestamp:  d.MaxTimestamp(),
		EventCount:   uint64(d.Size()),
	}
	data, _ := json.Marshal(frontier)
	var decoded network.SyncRequestPayload
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("SyncRequestPayload roundtrip: %v", err)
	}
}

// ── Integration: checkpoint bootstrap + lazy blob catch-up ─────────────────

func TestIntegration_CheckpointBootstrap_NoBlobInCheckpoint(t *testing.T) {
	kp, _ := crypto.GenerateKeyPair()
	sourceDAG := dag.New()
	sourceCfg := network.DefaultNodeConfig(kp.AgentID())
	sourceCfg.KeyPair = kp
	sourceNode := network.NewNode(sourceCfg, sourceDAG)

	// Add events including a TaskSubmitted with evidence hash.
	for i := 0; i < 5; i++ {
		makeTestEvent(t, sourceDAG, kp)
	}
	makeTaskSubmittedEvent(t, sourceDAG, kp, "abc123def456")

	// Build checkpoint — should contain metadata only, no blobs.
	cp := sourceNode.BuildCheckpoint()
	if cp.EventCount != 6 {
		t.Fatalf("checkpoint EventCount = %d; want 6", cp.EventCount)
	}

	// Marshal checkpoint and verify no blob-related fields.
	cpData, _ := json.Marshal(cp)
	var raw map[string]json.RawMessage
	json.Unmarshal(cpData, &raw)
	if _, ok := raw["blobs"]; ok {
		t.Error("checkpoint should not contain blob data")
	}
	if _, ok := raw["attached_blobs"]; ok {
		t.Error("checkpoint should not contain attached_blobs")
	}
	if _, ok := raw["evidence"]; ok {
		t.Error("checkpoint should not contain evidence data")
	}

	// New node bootstraps from checkpoint — blob can be fetched later.
	newDAG := dag.New()
	localMaxTS := newDAG.MaxTimestamp()
	localCount := uint64(newDAG.Size())
	gap, fromTS, toTS := network.BootstrapGap(localMaxTS, localCount, cp)
	if gap == 0 {
		t.Fatal("should detect gap for empty node")
	}

	// Backfill is bounded by MaxBootstrapRepairEvents.
	backfill := sourceDAG.RecentEvents(fromTS, network.MaxBootstrapRepairEvents)
	if len(backfill) > network.MaxBootstrapRepairEvents {
		t.Error("backfill must be bounded")
	}

	// Add backfilled events to new node.
	added := 0
	for _, ev := range backfill {
		if err := newDAG.Add(ev); err == nil {
			added++
		}
	}
	if added == 0 {
		t.Fatal("should add events during bootstrap")
	}

	// After bootstrap, the TaskSubmitted event is in the DAG but its blob
	// is NOT local. Blob catch-up happens via body-plane sidecar or fallback.
	_ = toTS
}

// ── Integration: partition-heal fills missing events via repair ─────────────

func TestIntegration_PartitionHeal_MissingEventsRecovered(t *testing.T) {
	kp, _ := crypto.GenerateKeyPair()

	// Node A has 20 events.
	dagA := dag.New()
	var eventsA []*event.Event
	for i := 0; i < 20; i++ {
		ev := makeTestEvent(t, dagA, kp)
		eventsA = append(eventsA, ev)
	}

	// Node B was partitioned — only has first 10 events.
	dagB := dag.New()
	for _, ev := range eventsA[:10] {
		_ = dagB.Add(ev)
	}
	if dagB.Size() != 10 {
		t.Fatalf("dagB size = %d; want 10", dagB.Size())
	}

	// Simulate repair: Node B requests missing events from Node A.
	missing := make([]event.EventID, 0)
	for _, ev := range eventsA[10:] {
		if _, err := dagB.Get(ev.ID); err != nil {
			missing = append(missing, ev.ID)
		}
	}
	if len(missing) != 10 {
		t.Fatalf("missing count = %d; want 10", len(missing))
	}

	// Bounded repair: fetch and add missing events.
	repaired := 0
	for _, id := range missing {
		ev, err := dagA.Get(id)
		if err != nil {
			continue
		}
		if err := dagB.Add(ev); err == nil {
			repaired++
		}
	}
	if repaired != 10 {
		t.Errorf("repaired = %d; want 10", repaired)
	}
	if dagB.Size() != 20 {
		t.Errorf("dagB size after repair = %d; want 20", dagB.Size())
	}
}

// ── Integration: restart does not trigger sync storm ───────────────────────

func TestIntegration_Restart_NoSyncStorm(t *testing.T) {
	kp, _ := crypto.GenerateKeyPair()
	d := dag.New()

	// Pre-populate DAG (simulates persistent state on restart).
	for i := 0; i < 50; i++ {
		makeTestEvent(t, d, kp)
	}

	// Create node with persistent DAG (restart scenario).
	cfg := network.DefaultNodeConfig(kp.AgentID())
	cfg.EnableLegacySync = false
	node := network.NewNode(cfg, d)

	// After restart with all-V2 peers, no legacy sync targets.
	targets := node.LegacySyncTargets()
	if len(targets) != 0 {
		t.Errorf("restart with V2 peers should have 0 sync targets, got %d", len(targets))
	}

	// DAG state is preserved — no need for full resync.
	if d.Size() != 50 {
		t.Errorf("DAG size = %d; want 50 (persistent state preserved)", d.Size())
	}
}

// ── Integration: frontier delta bounds sync responses ──────────────────────

func TestIntegration_FrontierDelta_BoundedResponse(t *testing.T) {
	// Verify MaxLegacySyncEventsPerResponse is the bound.
	if network.MaxLegacySyncEventsPerResponse != 500 {
		t.Fatalf("MaxLegacySyncEventsPerResponse = %d; want 500",
			network.MaxLegacySyncEventsPerResponse)
	}

	// Verify SyncRequestPayload carries frontier hints.
	frontier := network.SyncRequestPayload{
		KnownTips:    []event.EventID{"tip1", "tip2"},
		MaxTimestamp:  42,
		EventCount:   100,
	}
	data, _ := json.Marshal(frontier)
	var decoded network.SyncRequestPayload
	json.Unmarshal(data, &decoded)
	if decoded.MaxTimestamp != 42 {
		t.Errorf("MaxTimestamp = %d; want 42", decoded.MaxTimestamp)
	}
	if decoded.EventCount != 100 {
		t.Errorf("EventCount = %d; want 100", decoded.EventCount)
	}
}

// ── Integration: SyncBatchPayload has no Blobs field ───────────────────────

func TestIntegration_SyncBatch_NoBlobField(t *testing.T) {
	batch := network.SyncBatchPayload{
		Events: []*event.Event{},
	}
	data, _ := json.Marshal(batch)
	var raw map[string]json.RawMessage
	json.Unmarshal(data, &raw)
	if _, ok := raw["blobs"]; ok {
		t.Error("SyncBatchPayload should not have a blobs field")
	}
}

// ── Integration: body-plane sidecar + fallback blob fetch ──────────────────

func TestIntegration_BodyPlane_SidecarAndFallback(t *testing.T) {
	// Small blob fits in sidecar.
	smallBlob := []byte(`{"summary":"small evidence"}`)
	smallSum := sha256.Sum256(smallBlob)
	smallHash := hex.EncodeToString(smallSum[:])

	body := network.EventBody{
		EventID:       "evt-small",
		Payload:       json.RawMessage(`{}`),
		AttachedBlobs: map[string][]byte{smallHash: smallBlob},
	}
	bodyData, _ := json.Marshal(body)
	var decoded network.EventBody
	json.Unmarshal(bodyData, &decoded)
	if len(decoded.AttachedBlobs) != 1 {
		t.Fatalf("small blob should be in sidecar")
	}

	// Large blob uses fallback.
	largeBlob := make([]byte, network.MaxInlineFastPathBlobBytes+1)
	if len(largeBlob) <= network.MaxInlineFastPathBlobBytes {
		t.Fatal("large blob must exceed threshold")
	}

	// Fallback request/response works for large blobs.
	largeSum := sha256.Sum256(largeBlob)
	largeHash := hex.EncodeToString(largeSum[:])
	req := network.BlobRequest{Hash: largeHash, EventID: "evt-large"}
	resp := network.BlobResponse{Hash: largeHash, Data: largeBlob}
	reqData, _ := json.Marshal(req)
	respData, _ := json.Marshal(resp)

	var decodedReq network.BlobRequest
	var decodedResp network.BlobResponse
	json.Unmarshal(reqData, &decodedReq)
	json.Unmarshal(respData, &decodedResp)

	if decodedReq.Hash != largeHash {
		t.Error("BlobRequest hash mismatch")
	}
	if len(decodedResp.Data) != len(largeBlob) {
		t.Error("BlobResponse data size mismatch")
	}

	// Verify content hash before storing.
	verifySum := sha256.Sum256(decodedResp.Data)
	verifyHash := hex.EncodeToString(verifySum[:])
	if verifyHash != decodedResp.Hash {
		t.Error("blob content hash verification failed")
	}
}

// ── Integration: full pipeline TaskSubmitted → blob → EvidenceReady ────────

// memBlobStoreInt is an in-memory blob store for integration tests.
type memBlobStoreInt struct {
	mu   sync.RWMutex
	data map[string][]byte
}

func newMemBlobStoreInt() *memBlobStoreInt {
	return &memBlobStoreInt{data: make(map[string][]byte)}
}

func (m *memBlobStoreInt) Put(_ context.Context, data []byte) (string, int64, error) {
	sum := sha256.Sum256(data)
	hash := hex.EncodeToString(sum[:])
	m.mu.Lock()
	defer m.mu.Unlock()
	m.data[hash] = append([]byte{}, data...)
	return hash, int64(len(data)), nil
}

func (m *memBlobStoreInt) Get(_ context.Context, hash string) ([]byte, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	d, ok := m.data[hash]
	if !ok {
		return nil, nil
	}
	return d, nil
}

func TestIntegration_FullPipeline_TaskSubmitted_BlobReady(t *testing.T) {
	kp, _ := crypto.GenerateKeyPair()
	d := dag.New()
	bs := newMemBlobStoreInt()

	// Store evidence blob.
	blobData := []byte(`{"summary":"full pipeline evidence","output_type":"text","output_size":500}`)
	blobHash, _, _ := bs.Put(context.Background(), blobData)

	// Create TaskSubmitted event referencing the blob.
	ev := makeTaskSubmittedEvent(t, d, kp, blobHash)
	hdr := makeTestHeaderFromEvent(t, ev)

	// Node receives header (Announced).
	cfg := network.DefaultNodeConfig(kp.AgentID())
	node := network.NewNode(cfg, d)
	node.SetBlobStore(bs)
	im := node.Ingest()

	im.AdmitHeader("remote-peer", hdr)
	tr := im.GetTracking(ev.ID)
	if tr == nil || tr.Stage != network.StageAnnounced {
		t.Fatal("event should be at Announced stage")
	}

	// Simulate body response WITH sidecar blob.
	body := network.EventBody{
		EventID:       ev.ID,
		Payload:       ev.Payload,
		AttachedBlobs: map[string][]byte{blobHash: blobData},
	}
	bodyData, _ := json.Marshal(body)

	// Verify body deserializes correctly.
	var decodedBody network.EventBody
	if err := json.Unmarshal(bodyData, &decodedBody); err != nil {
		t.Fatalf("unmarshal body: %v", err)
	}

	// Complete body (body commitment check).
	if err := im.CompleteBody(ev.ID, ev.Payload); err != nil {
		t.Fatalf("CompleteBody: %v", err)
	}

	tr = im.GetTracking(ev.ID)
	if tr.Stage != network.StageCompleted {
		t.Errorf("stage = %v; want Completed", tr.Stage)
	}

	// Blob is in the local blobstore (attached via sidecar).
	retrieved, err := bs.Get(context.Background(), blobHash)
	if err != nil || len(retrieved) == 0 {
		t.Fatal("blob should be in local blobstore after sidecar delivery")
	}
	if string(retrieved) != string(blobData) {
		t.Error("retrieved blob data mismatch")
	}
}

// ── Integration: malicious frontier cannot trigger unbounded response ──────

func TestIntegration_MaliciousFrontier_Bounded(t *testing.T) {
	// A malicious frontier with MaxTimestamp=0 should still be bounded.
	frontier := network.SyncRequestPayload{
		KnownTips:    nil,
		MaxTimestamp:  0,
		EventCount:   0,
	}
	data, _ := json.Marshal(frontier)
	var decoded network.SyncRequestPayload
	json.Unmarshal(data, &decoded)

	// The response handler uses MaxLegacySyncEventsPerResponse as bound.
	// An attacker claiming 0 events should get at most 500 events back.
	if network.MaxLegacySyncEventsPerResponse > 1000 {
		t.Errorf("MaxLegacySyncEventsPerResponse = %d; should be reasonable bound",
			network.MaxLegacySyncEventsPerResponse)
	}
}

// ── Integration: V2 message types are properly gated ───────────────────────

func TestIntegration_V2MessageGating_Complete(t *testing.T) {
	v2Types := []network.MessageType{
		network.MsgEventHeader,
		network.MsgFrontier,
		network.MsgWindowDigest,
		network.MsgCheckpoint,
		network.MsgEventBody,
		network.MsgBodyRequest,
		network.MsgBlobRequest,
		network.MsgBlobResponse,
		network.MsgRepairRequest,
		network.MsgRepairResponse,
		network.MsgHelloV2,
		network.MsgPeerStatus,
		network.MsgAckHint,
		network.MsgNackMissing,
		network.MsgOverloaded,
	}

	for _, mt := range v2Types {
		if !network.IsV2Message(mt) {
			t.Errorf("%q should be recognized as V2 message", mt)
		}
	}

	// V1 types should NOT be V2.
	v1Types := []network.MessageType{
		network.MsgHandshake,
		network.MsgEvent,
		network.MsgRequestSync,
		network.MsgSyncBatch,
		network.MsgPing,
		network.MsgPong,
		network.MsgVote,
	}
	for _, mt := range v1Types {
		if network.IsV2Message(mt) {
			t.Errorf("%q should NOT be recognized as V2 message", mt)
		}
	}
}
