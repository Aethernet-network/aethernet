package taskverification

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	badger "github.com/dgraph-io/badger/v4"

	"github.com/Aethernet-network/aethernet/internal/crypto"
	"github.com/Aethernet-network/aethernet/internal/protocolmath"
	"github.com/Aethernet-network/aethernet/internal/verification"
)

const prefixValidatorReputation = "tvr:"

// ValidatorReputation tracks a validator's vote agreement history for a
// specific (family, category) combination. This is VALIDATOR reputation
// (vote consistency), not worker reputation (task completion quality).
type ValidatorReputation struct {
	ValidatorID        crypto.AgentID       `json:"validator_id"`
	Family             verification.FamilyID `json:"family"`
	Category           string               `json:"category"`
	TotalVotes         uint64               `json:"total_votes"`
	AgreeingVotes      uint64               `json:"agreeing_votes"`
	DeviatingVotes     uint64               `json:"deviating_votes"`
	AbstainedVotes     uint64               `json:"abstained_votes"`
	EquivocationEvents uint64               `json:"equivocation_events"`
	LastUpdated        int64                `json:"last_updated"`
}

// AgreementRate returns the fraction of votes that agreed with consensus,
// expressed in basis points (10000 = 1.0). Returns 0 when no votes have
// been recorded.
//
// This is a numeric-representation change from the prior float64 return
// type. The computation becomes (AgreeingVotes * 10000) / TotalVotes —
// integer division, quantized at return time to one of 10001 discrete
// buckets in [0, 10000]. The prior float version quantized only to IEEE-754
// precision at computation time. For fractions not representable in
// 10000ths (e.g. 1/3, 2/3) the two representations differ in the last
// bucket. See the canonical-distribution-integer-migration workstream for
// the rationale.
//
// Overflow: AgreeingVotes * 10000 stays within uint64 until AgreeingVotes
// exceeds ~1.8e15; no realistic vote count reaches that bound.
func (r *ValidatorReputation) AgreementRate() protocolmath.BasisPoints {
	if r.TotalVotes == 0 {
		return 0
	}
	return protocolmath.BasisPoints((r.AgreeingVotes * 10000) / r.TotalVotes)
}

// ValidatorReputationStore persists validator vote agreement metrics.
//
// projections:lint ignore "scheduled for deletion in step-4 of the reputation workstream which replaces it with EvidenceStore; intentionally unregistered to avoid churn — see docs/plans/2026-04-12-reputation-and-consensus-integrity.md §17 step 3"
type ValidatorReputationStore struct {
	db *badger.DB
	mu sync.Mutex
}

// NewValidatorReputationStore creates a reputation store backed by BadgerDB.
func NewValidatorReputationStore(db *badger.DB) *ValidatorReputationStore {
	return &ValidatorReputationStore{db: db}
}

func reputationKey(validatorID crypto.AgentID, family verification.FamilyID, category string) []byte {
	return []byte(prefixValidatorReputation + string(validatorID) + ":" + string(family) + ":" + category)
}

// Get retrieves the reputation record for a (validator, family, category).
// Returns a zero-valued record (not an error) if no record exists.
func (s *ValidatorReputationStore) Get(_ context.Context, validatorID crypto.AgentID, family verification.FamilyID, category string) (*ValidatorReputation, error) {
	rep := &ValidatorReputation{
		ValidatorID: validatorID,
		Family:      family,
		Category:    category,
	}
	err := s.db.View(func(txn *badger.Txn) error {
		item, err := txn.Get(reputationKey(validatorID, family, category))
		if err != nil {
			return nil // not found → zero record
		}
		return item.Value(func(val []byte) error {
			return json.Unmarshal(val, rep)
		})
	})
	return rep, err
}

// RecordVote updates the vote agreement counter for a validator.
func (s *ValidatorReputationStore) RecordVote(
	_ context.Context,
	validatorID crypto.AgentID,
	family verification.FamilyID,
	category string,
	agreed bool,
	abstained bool,
	now int64,
) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	key := reputationKey(validatorID, family, category)
	rep := &ValidatorReputation{
		ValidatorID: validatorID,
		Family:      family,
		Category:    category,
	}

	// Load existing.
	_ = s.db.View(func(txn *badger.Txn) error {
		item, err := txn.Get(key)
		if err != nil {
			return nil
		}
		return item.Value(func(val []byte) error {
			return json.Unmarshal(val, rep)
		})
	})

	rep.TotalVotes++
	if abstained {
		rep.AbstainedVotes++
	} else if agreed {
		rep.AgreeingVotes++
	} else {
		rep.DeviatingVotes++
	}
	rep.LastUpdated = now

	data, err := json.Marshal(rep)
	if err != nil {
		return fmt.Errorf("reputation: marshal: %w", err)
	}
	return s.db.Update(func(txn *badger.Txn) error {
		return txn.Set(key, data)
	})
}

// RecordEquivocation increments the equivocation counter.
func (s *ValidatorReputationStore) RecordEquivocation(
	_ context.Context,
	validatorID crypto.AgentID,
	family verification.FamilyID,
	category string,
	now int64,
) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	key := reputationKey(validatorID, family, category)
	rep := &ValidatorReputation{
		ValidatorID: validatorID,
		Family:      family,
		Category:    category,
	}

	_ = s.db.View(func(txn *badger.Txn) error {
		item, err := txn.Get(key)
		if err != nil {
			return nil
		}
		return item.Value(func(val []byte) error {
			return json.Unmarshal(val, rep)
		})
	})

	rep.EquivocationEvents++
	rep.LastUpdated = now

	data, _ := json.Marshal(rep)
	return s.db.Update(func(txn *badger.Txn) error {
		return txn.Set(key, data)
	})
}

// ValidatorQScore computes the Quality Score Q for a validator in a specific
// (family, category) context. This is used for Q-weighted validator fee
// distribution in the settler.
//
// Currently implements only the Consistency term (α₄) from the paper v4.1
// Q formula: Q = AgreementRate(validator, family, category).
//
// TODO prompt future: wire the full Q formula:
//   Q = (α₁·CVD_norm + α₂·ChallengeSurvival + α₃·ReplicationRate + α₄·Consistency) / Σα
// CVD_norm requires the verification diversity infrastructure.
// ChallengeSurvival requires the challenge/slash infrastructure from prompt 09.
// ReplicationRate requires the replay coordinator's verification history.
// For now, only α₄ (Consistency = AgreementRate) is active; others default to 1.0.
//
// New validators with no history return NeutralBP (10000, equivalent to
// the prior 1.0) so they can earn while building reputation. A validator
// with full-deviation history returns the 1% floor (100 basis points,
// equivalent to the prior 0.01) to avoid zero-weight permanently excluding
// them from fee distribution.
//
// Return type migrated from float64 to protocolmath.BasisPoints; see the
// canonical-distribution-integer-migration workstream. The neutral-1.0
// and floor-0.01 conventions are preserved exactly in basis-point form.
func (s *ValidatorReputationStore) ValidatorQScore(
	ctx context.Context,
	validatorID crypto.AgentID,
	family verification.FamilyID,
	category string,
) protocolmath.BasisPoints {
	rep, err := s.Get(ctx, validatorID, family, category)
	if err != nil || rep.TotalVotes == 0 {
		return protocolmath.NeutralBP // 10000 == prior 1.0
	}
	rate := rep.AgreementRate()
	if rate == 0 {
		return 100 // 1% floor; prior float 0.01 converted to basis points
	}
	return rate
}
