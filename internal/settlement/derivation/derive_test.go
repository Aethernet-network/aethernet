package derivation

import (
	"context"
	"testing"
)

// TestDeriveSettlement_NilRoundReturnsError confirms the precondition
// guard at the top of DeriveSettlement. Replaces the breakpoint-1
// errNotImplemented sentinel test now that the function body is in
// place.
func TestDeriveSettlement_NilRoundReturnsError(t *testing.T) {
	t.Parallel()

	_, err := DeriveSettlement(context.Background(), nil, "test-trigger", TerminalAccept, DerivationInputs{})
	if err == nil {
		t.Fatalf("DeriveSettlement(nil round) should error")
	}
}

// TestStubsReturnNeutralBP confirms the two stub projections return
// NeutralBP without error. These are pure compile-time checks that the
// interfaces are satisfied and the stub implementations behave as
// specified.
func TestStubsReturnNeutralBP(t *testing.T) {
	t.Parallel()

	wBP, err := NeutralBPStubW{}.Lookup("v", "family", "category", "contributor", 1000, 7)
	if err != nil {
		t.Fatalf("NeutralBPStubW.Lookup returned error: %v", err)
	}
	if wBP != 10000 {
		t.Fatalf("NeutralBPStubW.Lookup returned %d, want 10000", wBP)
	}

	qBP, err := NeutralQualityStub{}.Lookup("ancestor", 7)
	if err != nil {
		t.Fatalf("NeutralQualityStub.Lookup returned error: %v", err)
	}
	if qBP != 10000 {
		t.Fatalf("NeutralQualityStub.Lookup returned %d, want 10000", qBP)
	}
}

// TestComputeCanonicalIDFixANilSemantic confirms that the Fix A nil
// semantic is preserved end-to-end: two otherwise-identical records
// that differ only in CanonicalCutoffAnchorIsNil produce different
// canonical IDs. This is the U-1 uniqueness guarantee extended to the
// nil-vs-non-nil boundary.
func TestComputeCanonicalIDFixANilSemantic(t *testing.T) {
	t.Parallel()

	base := PayoutRecord{
		DerivationVersion: DerivationVersion,
		SettlementKey: SettlementKey{
			RoundID:          "round-1",
			TaskID:           "task-1",
			FundingReference: "funding-1",
		},
		Recipient: Recipient{ID: "alice", Role: RoleWorker},
		Amount:    Amount{Value: 1000, Currency: CurrencyAET},
		Purpose:   Purpose{Tag: TagWorkerPayout, Ordinal: 0},
		Provenance: Provenance{
			RoundVerdict:               VerdictAccept,
			CanonicalCutoffAnchor:      "",
			CanonicalCutoffAnchorIsNil: true,
		},
	}

	withAnchor := base
	withAnchor.Provenance.CanonicalCutoffAnchor = "some-anchor"
	withAnchor.Provenance.CanonicalCutoffAnchorIsNil = false

	nilID, err := ComputeCanonicalID(base)
	if err != nil {
		t.Fatalf("ComputeCanonicalID(nil cutoff) returned error: %v", err)
	}
	anchorID, err := ComputeCanonicalID(withAnchor)
	if err != nil {
		t.Fatalf("ComputeCanonicalID(anchor cutoff) returned error: %v", err)
	}

	if nilID == anchorID {
		t.Fatalf("Fix A nil semantic violated: nil cutoff and anchor cutoff produced the same canonical_id %q", nilID)
	}
	if len(nilID) != 64 || len(anchorID) != 64 {
		t.Fatalf("canonical_id length != 64 (expected SHA-256 hex): %d / %d", len(nilID), len(anchorID))
	}
}
