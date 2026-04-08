package taskverification

import (
	"context"
	"testing"

	badger "github.com/dgraph-io/badger/v4"

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
	if rep.AgreementRate() != 1.0 {
		t.Errorf("AgreementRate = %f; want 1.0", rep.AgreementRate())
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
	if rep.AgreementRate() != 0.0 {
		t.Errorf("AgreementRate = %f; want 0.0", rep.AgreementRate())
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
	// 2 agreeing / 3 total = 0.667
	rate := rep.AgreementRate()
	if rate < 0.66 || rate > 0.67 {
		t.Errorf("AgreementRate = %f; want ~0.667", rate)
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
	// No history → neutral 1.0
	q := s.ValidatorQScore(ctx, "new-validator", verification.FamilyDeterministicHeuristic, "research")
	if q != 1.0 {
		t.Errorf("Q for new validator = %f; want 1.0 (neutral)", q)
	}
}

func TestReputation_ValidatorQScore_WithHistory(t *testing.T) {
	s := newTestReputationStore(t)
	ctx := context.Background()
	// 3 agreeing, 1 deviating → rate = 0.75
	for i := 0; i < 3; i++ {
		_ = s.RecordVote(ctx, "v1", verification.FamilyDeterministicHeuristic, "research", true, false, int64(1000+i))
	}
	_ = s.RecordVote(ctx, "v1", verification.FamilyDeterministicHeuristic, "research", false, false, 1003)
	q := s.ValidatorQScore(ctx, "v1", verification.FamilyDeterministicHeuristic, "research")
	if q < 0.74 || q > 0.76 {
		t.Errorf("Q = %f; want 0.75", q)
	}
}
