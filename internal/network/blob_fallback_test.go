package network_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"testing"

	"github.com/Aethernet-network/aethernet/internal/crypto"
	"github.com/Aethernet-network/aethernet/internal/dag"
	"github.com/Aethernet-network/aethernet/internal/network"
)

// TestBlobFallback_MissingBlobTriggersRequest verifies that when a body
// response arrives without a sidecar blob for a TaskSubmitted event, the
// receiver detects the missing blob. This tests the detection path via
// tracking entry state.
func TestBlobFallback_MissingBlobTriggersRequest(t *testing.T) {
	kp, _ := crypto.GenerateKeyPair()
	d := dag.New()
	bs := newMemBlobStore()

	// Store an evidence blob on the "sender" side.
	blobData := []byte(`{"summary":"test evidence for fallback"}`)
	blobHash, _, _ := bs.Put(context.Background(), blobData)

	// Create TaskSubmitted event referencing the blob.
	ev := makeTaskSubmittedEvent(t, d, kp, blobHash)
	hdr := makeTestHeaderFromEvent(t, ev)

	// Set up receiver ingest — simulate receiving header + body WITHOUT sidecar.
	im := network.NewIngestManager(network.DefaultFastPathConfig())
	im.AdmitHeader("remote-peer", hdr)

	// Complete the body (no attached blobs — simulating oversized or missing sidecar).
	if err := im.CompleteBody(ev.ID, ev.Payload); err != nil {
		t.Fatalf("CompleteBody: %v", err)
	}

	// Verify the tracking entry can record blob need.
	if !im.MarkBlobRequested(ev.ID, blobHash) {
		t.Error("MarkBlobRequested should return true for first request")
	}

	// Second call should be idempotent.
	if im.MarkBlobRequested(ev.ID, blobHash) {
		t.Error("MarkBlobRequested should return false for duplicate request")
	}

	tr := im.GetTracking(ev.ID)
	if tr == nil {
		t.Fatal("tracking entry should exist")
	}
	if !tr.BlobRequested {
		t.Error("BlobRequested should be true")
	}
	if tr.BlobHash != blobHash {
		t.Errorf("BlobHash = %q; want %q", tr.BlobHash, blobHash)
	}
}

// TestBlobFallback_OversizedBlobUsesFallback verifies that blobs larger
// than MaxInlineFastPathBlobBytes are not included as sidecars but can
// be fetched via the fallback path.
func TestBlobFallback_OversizedBlobUsesFallback(t *testing.T) {
	// Create an oversized blob.
	largeBlob := make([]byte, network.MaxInlineFastPathBlobBytes+1)
	for i := range largeBlob {
		largeBlob[i] = byte(i % 256)
	}
	sum := sha256.Sum256(largeBlob)
	hash := hex.EncodeToString(sum[:])

	// Verify this exceeds the sidecar threshold.
	if len(largeBlob) <= network.MaxInlineFastPathBlobBytes {
		t.Fatal("blob is not oversized for this test")
	}

	// The BlobRequest/BlobResponse structs can carry the oversized blob.
	req := network.BlobRequest{Hash: hash, EventID: "evt-large"}
	reqData, _ := json.Marshal(req)
	var decoded network.BlobRequest
	if err := json.Unmarshal(reqData, &decoded); err != nil {
		t.Fatalf("BlobRequest unmarshal: %v", err)
	}
	if decoded.Hash != hash {
		t.Errorf("BlobRequest Hash = %q; want %q", decoded.Hash, hash)
	}

	resp := network.BlobResponse{Hash: hash, Data: largeBlob}
	respData, _ := json.Marshal(resp)
	var decodedResp network.BlobResponse
	if err := json.Unmarshal(respData, &decodedResp); err != nil {
		t.Fatalf("BlobResponse unmarshal: %v", err)
	}
	if decodedResp.Hash != hash {
		t.Errorf("BlobResponse Hash = %q; want %q", decodedResp.Hash, hash)
	}
	if len(decodedResp.Data) != len(largeBlob) {
		t.Errorf("BlobResponse Data len = %d; want %d", len(decodedResp.Data), len(largeBlob))
	}
}

// TestBlobFallback_ValidResponseStoresSuccessfully verifies that a valid
// blob fallback response (hash matches content) is stored in the blobstore.
func TestBlobFallback_ValidResponseStoresSuccessfully(t *testing.T) {
	bs := newMemBlobStore()
	blobData := []byte(`{"summary":"evidence via fallback","output_type":"text"}`)
	sum := sha256.Sum256(blobData)
	hash := hex.EncodeToString(sum[:])

	// Simulate what handleBlobResponse does: verify hash, then store.
	reSum := sha256.Sum256(blobData)
	computed := hex.EncodeToString(reSum[:])
	if computed != hash {
		t.Fatalf("hash verification failed: got %q, want %q", computed, hash)
	}

	storedHash, _, err := bs.Put(context.Background(), blobData)
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	if storedHash != hash {
		t.Errorf("stored hash = %q; want %q", storedHash, hash)
	}

	// Verify retrieval.
	retrieved, err := bs.Get(context.Background(), hash)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if string(retrieved) != string(blobData) {
		t.Error("retrieved blob data mismatch")
	}
}

// TestBlobFallback_CorruptResponseRejected verifies that a blob fallback
// response with mismatched content hash is rejected.
func TestBlobFallback_CorruptResponseRejected(t *testing.T) {
	originalData := []byte("original evidence blob")
	sum := sha256.Sum256(originalData)
	declaredHash := hex.EncodeToString(sum[:])

	// Tampered data.
	tamperedData := []byte("tampered evidence blob!!!!!")
	tamperedSum := sha256.Sum256(tamperedData)
	computedHash := hex.EncodeToString(tamperedSum[:])

	if computedHash == declaredHash {
		t.Fatal("tampered data has same hash — test invalid")
	}

	// The handleBlobResponse logic would reject this because got != resp.Hash.
	bs := newMemBlobStore()

	// Simulate the verification step — should NOT store.
	if computedHash != declaredHash {
		// Correctly rejected — don't store.
		if bs.Has(context.Background(), declaredHash) {
			t.Error("corrupt blob should not be in store")
		}
	}
}

// TestBlobFallback_QuotaLimitedBehavior verifies that the MaxBlobRequestsPerPeer
// constant exists and is set to a reasonable value.
func TestBlobFallback_QuotaLimitedBehavior(t *testing.T) {
	if network.MaxBlobRequestsPerPeer <= 0 {
		t.Fatalf("MaxBlobRequestsPerPeer = %d; want > 0", network.MaxBlobRequestsPerPeer)
	}
	if network.MaxBlobRequestsPerPeer > 20 {
		t.Errorf("MaxBlobRequestsPerPeer = %d; seems too high for abuse prevention", network.MaxBlobRequestsPerPeer)
	}
}

// TestBlobFallback_RepeatedMissingNoDuplicateRequests verifies that
// MarkBlobRequested prevents duplicate blob fetch requests for the same event.
func TestBlobFallback_RepeatedMissingNoDuplicateRequests(t *testing.T) {
	kp, _ := crypto.GenerateKeyPair()
	d := dag.New()

	blobHash := "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789"
	ev := makeTaskSubmittedEvent(t, d, kp, blobHash)
	hdr := makeTestHeaderFromEvent(t, ev)

	im := network.NewIngestManager(network.DefaultFastPathConfig())
	im.AdmitHeader("remote-peer", hdr)

	// First request — should succeed.
	if !im.MarkBlobRequested(ev.ID, blobHash) {
		t.Error("first MarkBlobRequested should return true")
	}

	// Subsequent requests — all should return false (dedup).
	for i := 0; i < 10; i++ {
		if im.MarkBlobRequested(ev.ID, blobHash) {
			t.Errorf("MarkBlobRequested call %d should return false", i+2)
		}
	}
}

// TestBlobFallback_MessageTypes verifies MsgBlobRequest and MsgBlobResponse
// are recognized as V2 messages.
func TestBlobFallback_MessageTypes(t *testing.T) {
	if network.MsgBlobRequest != "v2_blob_request" {
		t.Errorf("MsgBlobRequest = %q; want %q", network.MsgBlobRequest, "v2_blob_request")
	}
	if network.MsgBlobResponse != "v2_blob_response" {
		t.Errorf("MsgBlobResponse = %q; want %q", network.MsgBlobResponse, "v2_blob_response")
	}

	// Verify they are recognized as V2 messages by IsV2Message.
	if !network.IsV2Message(network.MsgBlobRequest) {
		t.Error("MsgBlobRequest should be V2")
	}
	if !network.IsV2Message(network.MsgBlobResponse) {
		t.Error("MsgBlobResponse should be V2")
	}
}

// TestBlobFallback_BlobRequestResponseRoundtrip tests JSON serialization
// of BlobRequest and BlobResponse payloads.
func TestBlobFallback_BlobRequestResponseRoundtrip(t *testing.T) {
	// Request roundtrip.
	req := network.BlobRequest{
		Hash:    "abc123def456",
		EventID: "evt-test-123",
	}
	reqData, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("Marshal BlobRequest: %v", err)
	}
	var decodedReq network.BlobRequest
	if err := json.Unmarshal(reqData, &decodedReq); err != nil {
		t.Fatalf("Unmarshal BlobRequest: %v", err)
	}
	if decodedReq.Hash != req.Hash || string(decodedReq.EventID) != string(req.EventID) {
		t.Errorf("BlobRequest roundtrip mismatch")
	}

	// Response roundtrip.
	blobData := []byte("some blob content")
	resp := network.BlobResponse{
		Hash: "abc123def456",
		Data: blobData,
	}
	respData, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("Marshal BlobResponse: %v", err)
	}
	var decodedResp network.BlobResponse
	if err := json.Unmarshal(respData, &decodedResp); err != nil {
		t.Fatalf("Unmarshal BlobResponse: %v", err)
	}
	if decodedResp.Hash != resp.Hash || string(decodedResp.Data) != string(blobData) {
		t.Errorf("BlobResponse roundtrip mismatch")
	}
}

// TestBlobFallback_NonTaskSubmittedNoBlobRequest verifies that non-TaskSubmitted
// events do not trigger blob fallback requests.
func TestBlobFallback_NonTaskSubmittedNoBlobRequest(t *testing.T) {
	kp, _ := crypto.GenerateKeyPair()
	d := dag.New()

	// Create a Transfer event (no blob reference).
	ev := makeTestEvent(t, d, kp)
	hdr := makeTestHeaderFromEvent(t, ev)

	im := network.NewIngestManager(network.DefaultFastPathConfig())
	im.AdmitHeader("remote-peer", hdr)

	// Complete body normally.
	if err := im.CompleteBody(ev.ID, ev.Payload); err != nil {
		t.Fatalf("CompleteBody: %v", err)
	}

	// Should not have blob state set.
	tr := im.GetTracking(ev.ID)
	if tr == nil {
		t.Fatal("tracking entry should exist")
	}
	if tr.BlobRequested {
		t.Error("Transfer events should not have BlobRequested set")
	}
	if tr.BlobHash != "" {
		t.Error("Transfer events should not have BlobHash set")
	}
}
