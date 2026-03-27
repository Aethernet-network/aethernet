package network_test

import (
	"encoding/json"
	"testing"

	"github.com/Aethernet-network/aethernet/internal/event"
	"github.com/Aethernet-network/aethernet/internal/network"
)

func TestEventHeader_JSONRoundtrip(t *testing.T) {
	h := network.EventHeader{
		EventID:         "abc123",
		Type:            event.EventTypeTransfer,
		CausalRefs:      []event.EventID{"parent1", "parent2"},
		AgentID:         "agent-1",
		CausalTimestamp: 42,
		StakeAmount:     1000,
		BodyCommitment:  "deadbeef",
		Signature:       []byte{0x01, 0x02},
	}

	data, err := json.Marshal(h)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var got network.EventHeader
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if got.EventID != h.EventID {
		t.Errorf("EventID = %q; want %q", got.EventID, h.EventID)
	}
	if got.Type != h.Type {
		t.Errorf("Type = %q; want %q", got.Type, h.Type)
	}
	if len(got.CausalRefs) != 2 {
		t.Errorf("CausalRefs length = %d; want 2", len(got.CausalRefs))
	}
	if got.CausalTimestamp != 42 {
		t.Errorf("CausalTimestamp = %d; want 42", got.CausalTimestamp)
	}
	if got.BodyCommitment != "deadbeef" {
		t.Errorf("BodyCommitment = %q; want deadbeef", got.BodyCommitment)
	}
}

func TestEventBody_JSONRoundtrip(t *testing.T) {
	b := network.EventBody{
		EventID: "ev-1",
		Payload: json.RawMessage(`{"from_agent":"alice","to_agent":"bob","amount":1000}`),
	}

	data, err := json.Marshal(b)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var got network.EventBody
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if got.EventID != "ev-1" {
		t.Errorf("EventID = %q; want ev-1", got.EventID)
	}
	if string(got.Payload) != `{"from_agent":"alice","to_agent":"bob","amount":1000}` {
		t.Errorf("Payload = %s", got.Payload)
	}
}

func TestHelloV2_JSONRoundtrip(t *testing.T) {
	h := network.HelloV2{
		ProtocolVersion: 2,
		Features:        []string{"fast_path", "body_split", "repair"},
		MaxBodySize:     1 << 20,
		FrontierTips:    []event.EventID{"tip-a", "tip-b"},
	}

	data, err := json.Marshal(h)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var got network.HelloV2
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if got.ProtocolVersion != 2 {
		t.Errorf("ProtocolVersion = %d; want 2", got.ProtocolVersion)
	}
	if len(got.Features) != 3 {
		t.Errorf("Features length = %d; want 3", len(got.Features))
	}
	if got.MaxBodySize != 1<<20 {
		t.Errorf("MaxBodySize = %d; want %d", got.MaxBodySize, 1<<20)
	}
	if len(got.FrontierTips) != 2 {
		t.Errorf("FrontierTips length = %d; want 2", len(got.FrontierTips))
	}
}

func TestFrontierSummary_JSONRoundtrip(t *testing.T) {
	f := network.FrontierSummary{
		Tips:         []event.EventID{"a", "b", "c"},
		MaxTimestamp: 100,
		EventCount:   500,
	}

	data, _ := json.Marshal(f)
	var got network.FrontierSummary
	_ = json.Unmarshal(data, &got)

	if len(got.Tips) != 3 || got.MaxTimestamp != 100 || got.EventCount != 500 {
		t.Errorf("FrontierSummary roundtrip failed: %+v", got)
	}
}

func TestRepairRequest_JSONRoundtrip(t *testing.T) {
	r := network.RepairRequest{
		MissingIDs:    []event.EventID{"ev-1", "ev-2"},
		FromTimestamp: 10,
		ToTimestamp:   20,
	}

	data, _ := json.Marshal(r)
	var got network.RepairRequest
	_ = json.Unmarshal(data, &got)

	if len(got.MissingIDs) != 2 {
		t.Errorf("MissingIDs length = %d; want 2", len(got.MissingIDs))
	}
	if got.FromTimestamp != 10 || got.ToTimestamp != 20 {
		t.Errorf("timestamps: %d-%d; want 10-20", got.FromTimestamp, got.ToTimestamp)
	}
}

func TestAckHint_JSONRoundtrip(t *testing.T) {
	a := network.AckHint{
		Received:   []event.EventID{"h1", "h2"},
		NeedBodies: []network.BodyRef{{EventID: "h1", BodyCommitment: "bc1"}},
	}

	data, _ := json.Marshal(a)
	var got network.AckHint
	_ = json.Unmarshal(data, &got)

	if len(got.Received) != 2 {
		t.Errorf("Received length = %d; want 2", len(got.Received))
	}
	if len(got.NeedBodies) != 1 || got.NeedBodies[0].EventID != "h1" {
		t.Errorf("NeedBodies = %+v", got.NeedBodies)
	}
}

func TestNackMissingParent_JSONRoundtrip(t *testing.T) {
	n := network.NackMissingParent{
		EventID:        "child-1",
		MissingParents: []event.EventID{"parent-1", "parent-2"},
	}

	data, _ := json.Marshal(n)
	var got network.NackMissingParent
	_ = json.Unmarshal(data, &got)

	if got.EventID != "child-1" || len(got.MissingParents) != 2 {
		t.Errorf("NackMissingParent roundtrip failed: %+v", got)
	}
}

func TestOverloaded_JSONRoundtrip(t *testing.T) {
	o := network.Overloaded{RetryAfterMs: 5000}

	data, _ := json.Marshal(o)
	var got network.Overloaded
	_ = json.Unmarshal(data, &got)

	if got.RetryAfterMs != 5000 {
		t.Errorf("RetryAfterMs = %d; want 5000", got.RetryAfterMs)
	}
}

func TestPeerStatus_JSONRoundtrip(t *testing.T) {
	s := network.PeerStatus{
		QueueDepth:    12,
		IngestRate:    150,
		DAGEventCount: 10000,
		Healthy:       true,
	}

	data, _ := json.Marshal(s)
	var got network.PeerStatus
	_ = json.Unmarshal(data, &got)

	if !got.Healthy || got.QueueDepth != 12 || got.IngestRate != 150 || got.DAGEventCount != 10000 {
		t.Errorf("PeerStatus roundtrip failed: %+v", got)
	}
}

func TestCheckpointSummary_JSONRoundtrip(t *testing.T) {
	c := network.CheckpointSummary{
		Timestamp:  50,
		Tips:       []event.EventID{"t1"},
		EventCount: 200,
		StateHash:  "abcdef",
		NodeID:     "node-1",
		Signature:  []byte{0xFF},
	}

	data, _ := json.Marshal(c)
	var got network.CheckpointSummary
	_ = json.Unmarshal(data, &got)

	if got.Timestamp != 50 || got.StateHash != "abcdef" || got.NodeID != "node-1" {
		t.Errorf("CheckpointSummary roundtrip failed: %+v", got)
	}
}

func TestWindowDigest_JSONRoundtrip(t *testing.T) {
	w := network.WindowDigest{
		FromTimestamp: 1,
		ToTimestamp:   10,
		EventIDs:      []event.EventID{"e1", "e2", "e3"},
	}

	data, _ := json.Marshal(w)
	var got network.WindowDigest
	_ = json.Unmarshal(data, &got)

	if got.FromTimestamp != 1 || got.ToTimestamp != 10 || len(got.EventIDs) != 3 {
		t.Errorf("WindowDigest roundtrip failed: %+v", got)
	}
}
