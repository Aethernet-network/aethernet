package taskverification

import (
	"encoding/json"
	"testing"

	"github.com/Aethernet-network/aethernet/internal/event"
)

func TestRoundID_DeterministicGeneration(t *testing.T) {
	eid := event.EventID("abc123def456")
	id1 := NewRoundID(eid)
	id2 := NewRoundID(eid)
	if id1 != id2 {
		t.Errorf("same event ID produced different round IDs: %s vs %s", id1, id2)
	}
	if id1 == "" {
		t.Error("round ID should not be empty")
	}
}

func TestRoundID_Different(t *testing.T) {
	id1 := NewRoundID(event.EventID("event-a"))
	id2 := NewRoundID(event.EventID("event-b"))
	if id1 == id2 {
		t.Errorf("different event IDs produced same round ID: %s", id1)
	}
}

func TestRoundState_StringRoundtrip(t *testing.T) {
	states := []RoundState{
		RoundStateOpen,
		RoundStateFinalizedAccept,
		RoundStateFinalizedReject,
		RoundStateDisputed,
		RoundStateExpired,
	}
	for _, s := range states {
		name := s.String()
		if name == "" {
			t.Errorf("RoundState(%d).String() returned empty", int(s))
		}
		parsed, ok := roundStateFromName[name]
		if !ok {
			t.Errorf("RoundState(%d).String() = %q not in roundStateFromName", int(s), name)
		}
		if parsed != s {
			t.Errorf("roundtrip failed: %d → %q → %d", int(s), name, int(parsed))
		}
	}
}

func TestVerdict_StringRoundtrip(t *testing.T) {
	verdicts := []Verdict{VerdictPass, VerdictFail, VerdictAbstain}
	for _, v := range verdicts {
		name := v.String()
		if name == "" {
			t.Errorf("Verdict(%d).String() returned empty", int(v))
		}
		parsed, ok := verdictFromName[name]
		if !ok {
			t.Errorf("Verdict(%d).String() = %q not in verdictFromName", int(v), name)
		}
		if parsed != v {
			t.Errorf("roundtrip failed: %d → %q → %d", int(v), name, int(parsed))
		}
	}
}

func TestRoundState_JSONMarshal(t *testing.T) {
	states := []struct {
		state RoundState
		want  string
	}{
		{RoundStateOpen, `"open"`},
		{RoundStateFinalizedAccept, `"finalized_accept"`},
		{RoundStateFinalizedReject, `"finalized_reject"`},
		{RoundStateDisputed, `"disputed"`},
		{RoundStateExpired, `"expired"`},
	}
	for _, tc := range states {
		data, err := json.Marshal(tc.state)
		if err != nil {
			t.Fatalf("Marshal(%d): %v", int(tc.state), err)
		}
		if string(data) != tc.want {
			t.Errorf("Marshal(%d) = %s; want %s", int(tc.state), data, tc.want)
		}
		var got RoundState
		if err := json.Unmarshal(data, &got); err != nil {
			t.Fatalf("Unmarshal(%s): %v", data, err)
		}
		if got != tc.state {
			t.Errorf("Unmarshal(%s) = %d; want %d", data, int(got), int(tc.state))
		}
	}
}

func TestVerdict_JSONMarshal(t *testing.T) {
	verdicts := []struct {
		verdict Verdict
		want    string
	}{
		{VerdictPass, `"pass"`},
		{VerdictFail, `"fail"`},
		{VerdictAbstain, `"abstain"`},
	}
	for _, tc := range verdicts {
		data, err := json.Marshal(tc.verdict)
		if err != nil {
			t.Fatalf("Marshal(%d): %v", int(tc.verdict), err)
		}
		if string(data) != tc.want {
			t.Errorf("Marshal(%d) = %s; want %s", int(tc.verdict), data, tc.want)
		}
		var got Verdict
		if err := json.Unmarshal(data, &got); err != nil {
			t.Fatalf("Unmarshal(%s): %v", data, err)
		}
		if got != tc.verdict {
			t.Errorf("Unmarshal(%s) = %d; want %d", data, int(got), int(tc.verdict))
		}
	}
}

func TestRoundState_Valid(t *testing.T) {
	if !RoundStateOpen.Valid() {
		t.Error("RoundStateOpen should be valid")
	}
	if RoundState(99).Valid() {
		t.Error("RoundState(99) should not be valid")
	}
}

func TestVerdict_Valid(t *testing.T) {
	if !VerdictPass.Valid() {
		t.Error("VerdictPass should be valid")
	}
	if Verdict(99).Valid() {
		t.Error("Verdict(99) should not be valid")
	}
}
