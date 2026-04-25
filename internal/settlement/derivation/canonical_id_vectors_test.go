package derivation

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"

	"github.com/Aethernet-network/aethernet/internal/auth"
)

// Fixed-vector tests for ComputeCanonicalID per multi-AI ChatGPT Item 3
// (V#5 hardening, 2026-04-25). Pre-existing tests verified DIFFERENTIAL
// canonicality (Fix A nil distinct from non-nil; D-1 cross-invocation
// equality; canonical_id length=64 hex). These tests pin EXACT 64-char
// canonical_id bytes for known inputs — caught any future change to the
// hash algorithm, JCS canonicalization, preimage shape, or schema-locked
// field semantics that doesn't roll DerivationVersion.
//
// Update vectors only with intent: a new vector value indicates a
// canonical-state change requiring DerivationVersion bump per
// payout-artifact-schema.yaml v1.1's `derivation_version` semantics.

// vector1: worker payout, nil canonical_cutoff_anchor (Fix A nil branch).
const fixedVector1ID = "9bab99be7cdc659daebc65d4b3cee75d37b2861c761e984eee1b604ecb44c702"

// vector2: same record as vector1 but explicit non-nil anchor.
// Differential vs vector1 verifies Fix A discriminator changes hash.
const fixedVector2ID = "e47f85ba07e6c56cbb156b60b4b8058f4dd843932d1277140e070573943502da"

// vector3: validator distribution payout with non-zero ordinal.
// Pins the ordinal-in-preimage contribution per U-1.
const fixedVector3ID = "4b94e6a8caf853212ea77616bd0d2ab1c68170d7e519b65818816b0e3d8be548"

// fixedVector1 returns the canonical record for vector 1.
func fixedVector1() PayoutRecord {
	return PayoutRecord{
		DerivationVersion: DerivationVersion,
		SettlementKey: SettlementKey{
			RoundID:          "fixed-vector-round-1",
			TaskID:           "fixed-vector-task-1",
			FundingReference: "fixed-vector-funding-1",
		},
		Recipient: Recipient{ID: "fixed-vector-worker", Role: RoleWorker},
		Amount:    Amount{Value: 73000, Currency: CurrencyAET},
		Purpose:   Purpose{Tag: TagWorkerPayout, Ordinal: 0},
		Provenance: Provenance{
			RoundVerdict:               VerdictAccept,
			CanonicalCutoffAnchor:      "",
			CanonicalCutoffAnchorIsNil: true,
		},
	}
}

func fixedVector2() PayoutRecord {
	r := fixedVector1()
	r.Provenance.CanonicalCutoffAnchor = "fixed-vector-anchor"
	r.Provenance.CanonicalCutoffAnchorIsNil = false
	return r
}

func fixedVector3() PayoutRecord {
	return PayoutRecord{
		DerivationVersion: DerivationVersion,
		SettlementKey: SettlementKey{
			RoundID:          "fixed-vector-round-1",
			TaskID:           "fixed-vector-task-1",
			FundingReference: "fixed-vector-funding-1",
		},
		Recipient: Recipient{ID: "fixed-vector-validator-a", Role: RoleValidator},
		Amount:    Amount{Value: 11500, Currency: CurrencyAET},
		Purpose:   Purpose{Tag: TagValidatorDistribution, Ordinal: 2},
		Provenance: Provenance{
			RoundVerdict:               VerdictAccept,
			CanonicalCutoffAnchor:      "",
			CanonicalCutoffAnchorIsNil: true,
		},
	}
}

// TestCanonicalID_FixedVector_NilCutoffAnchor pins the canonical_id for
// a fully-specified PayoutRecord with nil cutoff_anchor. If this test
// fails, ANY ONE of the following has changed without a DerivationVersion
// bump: SHA-256 algorithm, RFC 8785 JCS canonicalization, preimage map
// shape, field-name spellings, JSON-null-vs-omission discipline for
// optional fields, or the schema-locked enum string values
// (RoleWorker / TagWorkerPayout / VerdictAccept / CurrencyAET).
func TestCanonicalID_FixedVector_NilCutoffAnchor(t *testing.T) {
	t.Parallel()
	got, err := ComputeCanonicalID(fixedVector1())
	if err != nil {
		t.Fatalf("ComputeCanonicalID: %v", err)
	}
	if got != fixedVector1ID {
		t.Fatalf("canonical_id changed without DerivationVersion bump\n  want %s\n  got  %s", fixedVector1ID, got)
	}
}

// TestCanonicalID_FixedVector_NonNilCutoffAnchor pins the canonical_id
// for the same record as vector 1 but with explicit non-nil cutoff_anchor.
// Pair with NilCutoffAnchor: a single value of CanonicalCutoffAnchorIsNil
// flipping must produce a distinct canonical_id (Fix A discriminator).
func TestCanonicalID_FixedVector_NonNilCutoffAnchor(t *testing.T) {
	t.Parallel()
	got, err := ComputeCanonicalID(fixedVector2())
	if err != nil {
		t.Fatalf("ComputeCanonicalID: %v", err)
	}
	if got != fixedVector2ID {
		t.Fatalf("canonical_id changed without DerivationVersion bump\n  want %s\n  got  %s", fixedVector2ID, got)
	}
	if got == fixedVector1ID {
		t.Fatalf("Fix A discriminator broken: nil-cutoff and non-nil-cutoff records produced same canonical_id")
	}
}

// TestCanonicalID_FixedVector_ValidatorOrdinal pins a third vector
// covering different recipient role + non-zero ordinal. Catches changes
// to the ordinal field's preimage contribution (U-1).
func TestCanonicalID_FixedVector_ValidatorOrdinal(t *testing.T) {
	t.Parallel()
	got, err := ComputeCanonicalID(fixedVector3())
	if err != nil {
		t.Fatalf("ComputeCanonicalID: %v", err)
	}
	if got != fixedVector3ID {
		t.Fatalf("canonical_id changed without DerivationVersion bump\n  want %s\n  got  %s", fixedVector3ID, got)
	}
}

// TestCanonicalID_PreimageEncodesNilAsJSONNull verifies the byte-level
// JCS encoding of the canonical_cutoff_anchor field when nil: the
// literal substring `"canonical_cutoff_anchor":null` MUST appear in
// the JCS-canonical preimage. Per Fix A discipline + payout-artifact-
// schema.yaml v1.1 §"CANONICAL HASH ALGORITHM": optional fields use
// explicit JSON null when nil, never omission. Omission would create
// preimage ambiguity (two different absences hashing to the same
// preimage) and break U-1.
//
// This test reproduces the preimage-marshal pipeline locally
// (json.Marshal + auth.CanonicalizeJSON) so any future change to the
// JSON-null-vs-omission discipline in canonical_id.go's
// payoutRecordPreimage helper surfaces here.
func TestCanonicalID_PreimageEncodesNilAsJSONNull(t *testing.T) {
	t.Parallel()
	rec := fixedVector1()
	preimage := payoutRecordPreimage(rec)
	raw, err := json.Marshal(preimage)
	if err != nil {
		t.Fatalf("json.Marshal preimage: %v", err)
	}
	canonical, err := auth.CanonicalizeJSON(raw)
	if err != nil {
		t.Fatalf("auth.CanonicalizeJSON: %v", err)
	}
	canonStr := string(canonical)
	const wantSubstr = `"canonical_cutoff_anchor":null`
	if !strings.Contains(canonStr, wantSubstr) {
		t.Fatalf("Fix A discipline violated: preimage does NOT contain literal %q\n  preimage: %s",
			wantSubstr, canonStr)
	}
	// Sanity: also assert the SHA-256 of these canonical bytes equals
	// the pinned vector1 ID, proving the preimage path matches what
	// ComputeCanonicalID actually hashes.
	sum := sha256.Sum256(canonical)
	reproduced := hex.EncodeToString(sum[:])
	if reproduced != fixedVector1ID {
		t.Fatalf("preimage SHA-256 = %s, want %s (preimage helper drift)", reproduced, fixedVector1ID)
	}
}

// TestCanonicalID_PreimageEncodesNonNilAsString verifies the
// complementary case: when CanonicalCutoffAnchorIsNil=false, the
// preimage encodes the EventID as a JSON string, NOT as null.
func TestCanonicalID_PreimageEncodesNonNilAsString(t *testing.T) {
	t.Parallel()
	rec := fixedVector2()
	preimage := payoutRecordPreimage(rec)
	raw, err := json.Marshal(preimage)
	if err != nil {
		t.Fatalf("json.Marshal preimage: %v", err)
	}
	canonical, err := auth.CanonicalizeJSON(raw)
	if err != nil {
		t.Fatalf("auth.CanonicalizeJSON: %v", err)
	}
	canonStr := string(canonical)
	const wantSubstr = `"canonical_cutoff_anchor":"fixed-vector-anchor"`
	if !strings.Contains(canonStr, wantSubstr) {
		t.Fatalf("Fix A non-nil encoding violated: preimage does NOT contain literal %q\n  preimage: %s",
			wantSubstr, canonStr)
	}
	const forbiddenSubstr = `"canonical_cutoff_anchor":null`
	if strings.Contains(canonStr, forbiddenSubstr) {
		t.Fatalf("Fix A non-nil case leaked null encoding: preimage contains %q\n  preimage: %s",
			forbiddenSubstr, canonStr)
	}
}
