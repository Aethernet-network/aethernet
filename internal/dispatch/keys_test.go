package dispatch

import (
	"strings"
	"testing"

	"github.com/Aethernet-network/aethernet/internal/event"
)

func makeTestEvent(t *testing.T, agent, payload string) *event.Event {
	t.Helper()
	e, err := event.New(
		event.EventTypeTransfer,
		nil,
		event.TransferPayload{
			Version:   1,
			FromAgent: agent,
			ToAgent:   "bob",
			Amount:    100,
			Currency:  "AET",
		},
		agent,
		nil,
		0,
	)
	if err != nil {
		t.Fatalf("construct event: %v", err)
	}
	return e
}

func TestAdmissionKey_DeterministicFromCanonicalBytes(t *testing.T) {
	ev := makeTestEvent(t, "alice", "payload1")
	key1, err := AdmissionKey(ev)
	if err != nil {
		t.Fatalf("AdmissionKey: %v", err)
	}
	key2, err := AdmissionKey(ev)
	if err != nil {
		t.Fatalf("AdmissionKey: %v", err)
	}
	if key1 != key2 {
		t.Errorf("same event produced different keys: %s vs %s", key1, key2)
	}
	if !strings.HasPrefix(key1, admissionKeyPrefix) {
		t.Errorf("key %q missing prefix %q", key1, admissionKeyPrefix)
	}
}

func TestAdmissionKey_DifferentEventsProduceDifferentKeys(t *testing.T) {
	ev1 := makeTestEvent(t, "alice", "payload1")
	ev2 := makeTestEvent(t, "charlie", "payload2")

	key1, err := AdmissionKey(ev1)
	if err != nil {
		t.Fatalf("key1: %v", err)
	}
	key2, err := AdmissionKey(ev2)
	if err != nil {
		t.Fatalf("key2: %v", err)
	}
	if key1 == key2 {
		t.Errorf("different events produced same key: %s", key1)
	}
}

func TestAdmissionKey_ContentHashNotEventID(t *testing.T) {
	ev := makeTestEvent(t, "alice", "payload1")
	key, err := AdmissionKey(ev)
	if err != nil {
		t.Fatalf("AdmissionKey: %v", err)
	}
	trimmed := strings.TrimPrefix(key, admissionKeyPrefix)
	if trimmed == string(ev.ID) {
		t.Errorf("admission key hash equals EventID; expected BLAKE3 vs SHA-256 to differ")
	}
}
