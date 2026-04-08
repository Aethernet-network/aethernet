package taskverification

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	badger "github.com/dgraph-io/badger/v4"

	"github.com/Aethernet-network/aethernet/internal/crypto"
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

// AgreementRate returns the fraction of votes that agreed with consensus.
// Returns 0.0 if no votes have been recorded.
func (r *ValidatorReputation) AgreementRate() float64 {
	if r.TotalVotes == 0 {
		return 0
	}
	return float64(r.AgreeingVotes) / float64(r.TotalVotes)
}

// ValidatorReputationStore persists validator vote agreement metrics.
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
// New validators with no history return 1.0 (neutral) so they can earn
// while building reputation.
func (s *ValidatorReputationStore) ValidatorQScore(
	ctx context.Context,
	validatorID crypto.AgentID,
	family verification.FamilyID,
	category string,
) float64 {
	rep, err := s.Get(ctx, validatorID, family, category)
	if err != nil || rep.TotalVotes == 0 {
		return 1.0 // neutral for new validators
	}
	rate := rep.AgreementRate()
	if rate == 0 {
		return 0.01 // minimum floor to avoid zero-weight (allows recovery)
	}
	return rate
}
