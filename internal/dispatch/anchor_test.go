package dispatch

import (
	"errors"
	"testing"

	"github.com/Aethernet-network/aethernet/internal/event"
)

type stubDAG struct {
	tips      []event.EventID
	ancestors map[[2]event.EventID]bool
	events    map[event.EventID]*event.Event
}

func (s *stubDAG) Tips() []event.EventID { return s.tips }

func (s *stubDAG) IsAncestor(ancestor, descendant event.EventID) (bool, error) {
	key := [2]event.EventID{ancestor, descendant}
	if v, ok := s.ancestors[key]; ok {
		return v, nil
	}
	return false, errors.New("not reachable")
}

func (s *stubDAG) Get(id event.EventID) (*event.Event, error) {
	if e, ok := s.events[id]; ok {
		return e, nil
	}
	return nil, errors.New("not found")
}

func TestDAGAnchor_EmptyAnchor_FirstAdmission(t *testing.T) {
	dag := &stubDAG{tips: []event.EventID{"tip1"}}
	if err := verifyAnchor(dag, ""); err != nil {
		t.Fatalf("empty anchor should pass: %v", err)
	}
}

func TestDAGAnchor_AnchorEqualsTip(t *testing.T) {
	dag := &stubDAG{tips: []event.EventID{"tip1", "tip2"}}
	if err := verifyAnchor(dag, "tip1"); err != nil {
		t.Fatalf("anchor == tip should pass: %v", err)
	}
}

func TestDAGAnchor_ValidAncestorRelationship(t *testing.T) {
	dag := &stubDAG{
		tips: []event.EventID{"tip1"},
		ancestors: map[[2]event.EventID]bool{
			{"old-anchor", "tip1"}: true,
		},
	}
	if err := verifyAnchor(dag, "old-anchor"); err != nil {
		t.Fatalf("valid ancestor should pass: %v", err)
	}
}

func TestDAGAnchor_CorruptedAnchor(t *testing.T) {
	dag := &stubDAG{
		tips:      []event.EventID{"tip1"},
		ancestors: map[[2]event.EventID]bool{},
	}
	err := verifyAnchor(dag, "unreachable-anchor")
	if !errors.Is(err, ErrCorruptedAdmissionState) {
		t.Fatalf("want ErrCorruptedAdmissionState, got %v", err)
	}
}

func TestDAGAnchor_EmptyTips(t *testing.T) {
	dag := &stubDAG{tips: nil}
	err := verifyAnchor(dag, "some-anchor")
	if !errors.Is(err, ErrCorruptedAdmissionState) {
		t.Fatalf("empty tips should fail: %v", err)
	}
}
