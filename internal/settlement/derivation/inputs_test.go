package derivation

import (
	"strings"
	"testing"

	"github.com/Aethernet-network/aethernet/internal/crypto"
	"github.com/Aethernet-network/aethernet/internal/event"
	"github.com/Aethernet-network/aethernet/internal/genesis"
)

// canonicalTreasury is the only TreasuryID NewDerivationInputs accepts
// per multi-AI Item 1 composite (2026-04-25). Tests that need to drive
// the constructor's happy path use this value; tests that exercise the
// rejection path use anything else.
const canonicalTreasury crypto.AgentID = crypto.AgentID(genesis.BucketTreasury)

// validConstructorArgs returns a fully-populated tuple of
// NewDerivationInputs arguments that should succeed validation. Each
// validation-failure test below mutates one argument.
func validConstructorArgs() (
	WProjections,
	QualityProjections,
	AnchorReader,
	EscrowLookup,
	event.EventID,
	event.EventID,
	crypto.AgentID,
) {
	return WProjections{Stub: NeutralBPStubW{}},
		QualityProjections{Stub: NeutralQualityStub{}},
		&fakeAnchorReader{events: map[event.EventID]*event.Event{}},
		&fakeEscrow{},
		ReputationActivationEventID,
		QualityActivationEventID,
		canonicalTreasury
}

// TestNewDerivationInputs_HappyPath verifies the canonical wiring
// (treasury == genesis.BucketTreasury, all required services non-nil)
// constructs a valid bundle without error.
func TestNewDerivationInputs_HappyPath(t *testing.T) {
	t.Parallel()

	w, q, dr, em, rep, qual, tr := validConstructorArgs()
	in, err := NewDerivationInputs(w, q, dr, em, rep, qual, tr)
	if err != nil {
		t.Fatalf("NewDerivationInputs (happy path): %v", err)
	}
	if in.treasuryID != canonicalTreasury {
		t.Fatalf("treasuryID = %q, want %q", in.treasuryID, canonicalTreasury)
	}
	if in.dagReader == nil || in.escrowMgr == nil || in.w.Stub == nil || in.quality.Stub == nil {
		t.Fatalf("required fields missing in constructed inputs: %+v", in)
	}
}

// TestNewDerivationInputs_RejectsWrongTreasury verifies validation rule
// (1): TreasuryID must equal genesis.BucketTreasury.
func TestNewDerivationInputs_RejectsWrongTreasury(t *testing.T) {
	t.Parallel()

	w, q, dr, em, rep, qual, _ := validConstructorArgs()
	_, err := NewDerivationInputs(w, q, dr, em, rep, qual, "treasury-imposter")
	if err == nil {
		t.Fatal("NewDerivationInputs accepted non-canonical treasuryID; expected rejection")
	}
	if !strings.Contains(err.Error(), "treasuryID") || !strings.Contains(err.Error(), genesis.BucketTreasury) {
		t.Fatalf("error should name treasuryID and the canonical bucket; got: %v", err)
	}
}

// TestNewDerivationInputs_RejectsNilWStub verifies validation rule (2)
// for W.Stub.
func TestNewDerivationInputs_RejectsNilWStub(t *testing.T) {
	t.Parallel()

	_, q, dr, em, rep, qual, tr := validConstructorArgs()
	_, err := NewDerivationInputs(WProjections{Stub: nil}, q, dr, em, rep, qual, tr)
	if err == nil {
		t.Fatal("NewDerivationInputs accepted nil W.Stub; expected rejection")
	}
	if !strings.Contains(err.Error(), "W.Stub") {
		t.Fatalf("error should name W.Stub; got: %v", err)
	}
}

// TestNewDerivationInputs_RejectsNilQualityStub verifies validation rule
// (2) for Quality.Stub.
func TestNewDerivationInputs_RejectsNilQualityStub(t *testing.T) {
	t.Parallel()

	w, _, dr, em, rep, qual, tr := validConstructorArgs()
	_, err := NewDerivationInputs(w, QualityProjections{Stub: nil}, dr, em, rep, qual, tr)
	if err == nil {
		t.Fatal("NewDerivationInputs accepted nil Quality.Stub; expected rejection")
	}
	if !strings.Contains(err.Error(), "Quality.Stub") {
		t.Fatalf("error should name Quality.Stub; got: %v", err)
	}
}

// TestNewDerivationInputs_RejectsNilDAGReader verifies validation rule
// (2) for dagReader.
func TestNewDerivationInputs_RejectsNilDAGReader(t *testing.T) {
	t.Parallel()

	w, q, _, em, rep, qual, tr := validConstructorArgs()
	_, err := NewDerivationInputs(w, q, nil, em, rep, qual, tr)
	if err == nil {
		t.Fatal("NewDerivationInputs accepted nil dagReader; expected rejection")
	}
	if !strings.Contains(err.Error(), "dagReader") {
		t.Fatalf("error should name dagReader; got: %v", err)
	}
}

// TestNewDerivationInputs_RejectsNilEscrowMgr verifies validation rule
// (2) for escrowMgr.
func TestNewDerivationInputs_RejectsNilEscrowMgr(t *testing.T) {
	t.Parallel()

	w, q, dr, _, rep, qual, tr := validConstructorArgs()
	_, err := NewDerivationInputs(w, q, dr, nil, rep, qual, tr)
	if err == nil {
		t.Fatal("NewDerivationInputs accepted nil escrowMgr; expected rejection")
	}
	if !strings.Contains(err.Error(), "escrowMgr") {
		t.Fatalf("error should name escrowMgr; got: %v", err)
	}
}

// TestNewDerivationInputs_AcceptsEmptyActivationIDs verifies validation
// rule (3): activation EventIDs accepted as-is, including the empty-
// string pre-locked-workstream placeholder. This is the canonical
// configuration that ships today.
func TestNewDerivationInputs_AcceptsEmptyActivationIDs(t *testing.T) {
	t.Parallel()

	w, q, dr, em, _, _, tr := validConstructorArgs()
	in, err := NewDerivationInputs(w, q, dr, em, "", "", tr)
	if err != nil {
		t.Fatalf("NewDerivationInputs rejected empty activation EventIDs; expected acceptance: %v", err)
	}
	if in.reputationActivationEventID != "" || in.qualityActivationEventID != "" {
		t.Fatalf("activation EventIDs should be empty; got rep=%q qual=%q",
			in.reputationActivationEventID, in.qualityActivationEventID)
	}
}
