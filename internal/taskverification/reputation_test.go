package taskverification

import (
	"context"
	"testing"

	badger "github.com/dgraph-io/badger/v4"

	"github.com/Aethernet-network/aethernet/internal/protocolmath"
	"github.com/Aethernet-network/aethernet/internal/verification"
)

func newTestReputationStore(t *testing.T) *ValidatorReputationStore {
	t.Helper()
	db, err := badger.Open(badger.DefaultOptions("").WithInMemory(true).WithLogger(nil))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return NewValidatorReputationStore(db)
}

func TestReputation_RecordAgreement(t *testing.T) {
	s := newTestReputationStore(t)
	ctx := context.Background()
	_ = s.RecordVote(ctx, "v1", verification.FamilyDeterministicHeuristic, "research", true, false, 1000)
	rep, _ := s.Get(ctx, "v1", verification.FamilyDeterministicHeuristic, "research")
	if rep.TotalVotes != 1 {
		t.Errorf("TotalVotes = %d; want 1", rep.TotalVotes)
	}
	if rep.AgreeingVotes != 1 {
		t.Errorf("AgreeingVotes = %d; want 1", rep.AgreeingVotes)
	}
	if rep.AgreementRate() != protocolmath.NeutralBP {
		t.Errorf("AgreementRate = %d; want %d (NeutralBP)", rep.AgreementRate(), protocolmath.NeutralBP)
	}
}

func TestReputation_RecordDeviation(t *testing.T) {
	s := newTestReputationStore(t)
	ctx := context.Background()
	_ = s.RecordVote(ctx, "v1", verification.FamilyLLMSemantic, "code", false, false, 1000)
	rep, _ := s.Get(ctx, "v1", verification.FamilyLLMSemantic, "code")
	if rep.DeviatingVotes != 1 {
		t.Errorf("DeviatingVotes = %d; want 1", rep.DeviatingVotes)
	}
	if rep.AgreementRate() != 0 {
		t.Errorf("AgreementRate = %d; want 0", rep.AgreementRate())
	}
}

func TestReputation_RecordEquivocation(t *testing.T) {
	s := newTestReputationStore(t)
	ctx := context.Background()
	_ = s.RecordEquivocation(ctx, "v1", verification.FamilyDeterministicHeuristic, "research", 1000)
	rep, _ := s.Get(ctx, "v1", verification.FamilyDeterministicHeuristic, "research")
	if rep.EquivocationEvents != 1 {
		t.Errorf("EquivocationEvents = %d; want 1", rep.EquivocationEvents)
	}
}

func TestReputation_AgreementRate_Mixed(t *testing.T) {
	s := newTestReputationStore(t)
	ctx := context.Background()
	_ = s.RecordVote(ctx, "v1", verification.FamilyDeterministicHeuristic, "research", true, false, 1000)
	_ = s.RecordVote(ctx, "v1", verification.FamilyDeterministicHeuristic, "research", true, false, 1001)
	_ = s.RecordVote(ctx, "v1", verification.FamilyDeterministicHeuristic, "research", false, false, 1002)
	rep, _ := s.Get(ctx, "v1", verification.FamilyDeterministicHeuristic, "research")
	// 2 agreeing / 3 total: integer-quantized as (2 * 10000) / 3 = 6666.
	// Exact integer output; no float tolerance needed.
	rate := rep.AgreementRate()
	if rate != 6666 {
		t.Errorf("AgreementRate = %d; want 6666 (2/3 in basis points, integer-quantized)", rate)
	}
}

// TestAgreementRate_IntegerQuantization pins the early-quantization behavior
// introduced by the canonical-distribution-integer-migration workstream. The
// prior float representation of 1/3 was ~0.3333333; the BasisPoints
// representation is exactly 3333. This test guards against any future regression
// to float-at-computation semantics.
func TestAgreementRate_IntegerQuantization(t *testing.T) {
	rep := &ValidatorReputation{AgreeingVotes: 1, TotalVotes: 3}
	if got := rep.AgreementRate(); got != 3333 {
		t.Errorf("AgreementRate(1/3) = %d; want 3333 (integer quantization at return)", got)
	}
}

func TestReputation_Persistence(t *testing.T) {
	s := newTestReputationStore(t)
	ctx := context.Background()
	_ = s.RecordVote(ctx, "v1", verification.FamilyDeterministicHeuristic, "research", true, false, 1000)
	_ = s.RecordVote(ctx, "v1", verification.FamilyDeterministicHeuristic, "research", true, false, 1001)

	// Re-read from same store (simulating restart with same DB).
	rep, _ := s.Get(ctx, "v1", verification.FamilyDeterministicHeuristic, "research")
	if rep.TotalVotes != 2 {
		t.Errorf("TotalVotes = %d; want 2 (persisted)", rep.TotalVotes)
	}
}

func TestReputation_ValidatorQScore_Neutral(t *testing.T) {
	s := newTestReputationStore(t)
	ctx := context.Background()
	// No history → NeutralBP (10000, equivalent to the prior 1.0 float).
	q := s.ValidatorQScore(ctx, "new-validator", verification.FamilyDeterministicHeuristic, "research")
	if q != protocolmath.NeutralBP {
		t.Errorf("Q for new validator = %d; want %d (NeutralBP)", q, protocolmath.NeutralBP)
	}
}

func TestReputation_ValidatorQScore_WithHistory(t *testing.T) {
	s := newTestReputationStore(t)
	ctx := context.Background()
	// 3 agreeing, 1 deviating → rate = (3 * 10000) / 4 = 7500.
	for i := 0; i < 3; i++ {
		_ = s.RecordVote(ctx, "v1", verification.FamilyDeterministicHeuristic, "research", true, false, int64(1000+i))
	}
	_ = s.RecordVote(ctx, "v1", verification.FamilyDeterministicHeuristic, "research", false, false, 1003)
	q := s.ValidatorQScore(ctx, "v1", verification.FamilyDeterministicHeuristic, "research")
	if q != 7500 {
		t.Errorf("Q = %d; want 7500 (3/4 in basis points)", q)
	}
}

// TestReputation_ValidatorQScore_ZeroRateFloor pins the 1% (100 basis points)
// floor that replaces the prior float 0.01 floor. Without this floor, a
// fully-deviating validator would be permanently excluded from Q-weighted
// distribution with no path to recovery; the floor preserves the prior
// behavior exactly, in basis-point form.
func TestReputation_ValidatorQScore_ZeroRateFloor(t *testing.T) {
	s := newTestReputationStore(t)
	ctx := context.Background()
	// Record only deviating votes so AgreementRate returns 0; ValidatorQScore
	// should then return the floor (100 BP == prior 0.01 float) rather than 0.
	for i := 0; i < 3; i++ {
		_ = s.RecordVote(ctx, "v1", verification.FamilyDeterministicHeuristic, "research", false, false, int64(1000+i))
	}
	q := s.ValidatorQScore(ctx, "v1", verification.FamilyDeterministicHeuristic, "research")
	if q != 100 {
		t.Errorf("Q for fully-deviating validator = %d; want 100 (1%% floor)", q)
	}
}
