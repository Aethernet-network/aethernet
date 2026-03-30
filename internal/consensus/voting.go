// Package consensus implements reputation-weighted virtual voting for the
// AetherNet causal DAG.
//
// # Virtual voting
//
// In classical BFT consensus (PBFT, Tendermint) nodes broadcast explicit vote
// messages to one another. This is expensive in bandwidth and latency, and
// requires N² message complexity at scale.
//
// Virtual voting eliminates explicit vote messages. Instead, every correct node
// independently simulates what every other node would conclude, using only the
// local DAG view and a shared reputation registry. When a node's simulation
// reaches a supermajority, it knows every other correct node's simulation will
// reach the same conclusion — so it finalises locally without communication.
//
// This works because all correct nodes share the same deterministic weight
// function over the same registry state: identical inputs → identical outputs.
// Byzantine nodes can submit arbitrary data but cannot alter what weight correct
// nodes assign each voter.
//
// # Byzantine Fault Tolerance
//
// Correct consensus is guaranteed when the honest nodes control more than 2/3 of
// total stake-weighted reputation. The default 66.7% supermajority threshold
// ensures that no Byzantine coalition of ≤1/3 weight can finalize a conflicting
// order on any correct node.
//
// # Weight formula
//
// weight(agent) = (ReputationScore × StakedAmount) / 10000
//
// Reputation is measured in basis points (0–10000). StakedAmount is in
// micro-AET. Dividing by 10000 normalises reputation back to a [0,1] range so
// the effective weight scales linearly with stake, dampened by track record.
// An agent with no stake or no reputation has weight zero and cannot influence
// a finalization outcome even if their votes are counted.
package consensus

import (
	"errors"
	"fmt"
	"math/big"
	"sort"
	"sync"
	"time"

	"github.com/Aethernet-network/aethernet/internal/crypto"
	"github.com/Aethernet-network/aethernet/internal/event"
	"github.com/Aethernet-network/aethernet/internal/identity"
)

// bigDivisor is the constant 10000 used for weight normalization, pre-allocated
// as a *big.Int so it is not re-created on every computeWeight call.
var bigDivisor = big.NewInt(10000)

// PersistedVote is the minimal vote record written to and read from the
// persistence layer. Simple scalar fields avoid importing the store package
// from consensus (which would create a cycle). *store.Store satisfies
// VotePersistence by implementing these methods.
type PersistedVote struct {
	EventID string
	VoterID string
	Verdict bool
}

// VotePersistence is the interface that a durable store must implement to
// survive VotingRound state across node restarts. After each RegisterVote,
// the vote is written to the store. After finalization, votes are deleted.
// On startup, LoadPersistedVotes reloads pending votes from the store.
type VotePersistence interface {
	PutVote(eventID, voterID string, verdict bool) error
	GetVotes(eventID string) ([]PersistedVote, error)
	DeleteVotes(eventID string) error
	AllVoteEventIDs() ([]string, error)
}

// Sentinel errors for programmatic handling by callers.
var (
	// ErrEventNotFound is returned by GetRecord, IsFinalized, FinalOrder, and
	// TallyVotes when no VoteRecord exists for the given EventID.
	ErrEventNotFound = errors.New("consensus: event not found in voting records")

	// ErrDuplicateVote is returned by RegisterVote when the same voter submits
	// a second vote for an event they have already voted on.
	ErrDuplicateVote = errors.New("consensus: duplicate vote from same voter")

	// ErrAlreadyFinalized is returned by RegisterVote when the event's VoteRecord
	// has already been finalized. Votes after finalization are rejected.
	ErrAlreadyFinalized = errors.New("consensus: event already finalized")

	// ErrRoundExhausted is returned by RegisterVote when the event has exceeded
	// MaxRounds tally attempts without reaching a supermajority. Further votes
	// are rejected because the event is permanently failed.
	ErrRoundExhausted = errors.New("consensus: event exceeded max rounds without supermajority")

	// ErrNotFinalized is returned by FinalOrder when the event is known but has
	// not yet reached a finalized state.
	ErrNotFinalized = errors.New("consensus: event not yet finalized")
)

// ValidatorSetSource provides snapshot-bound vote eligibility and weight for
// consensus rounds. When set on VotingRound via SetValidatorSet, it replaces
// the identity registry as the canonical source of vote authority.
//
// The snapshot is immutable — weights do not change between votes in the same
// round. This eliminates the race where a voter's identity record changes
// between vote registration and tally, causing different nodes to compute
// different weights for the same voter.
//
// *validatorlifecycle.ValidatorSnapshot satisfies this interface.
type ValidatorSetSource interface {
	// VoteWeightByKey returns the consensus weight for the given operator key.
	// Returns 0, false if the key does not hold an eligible seat in the
	// snapshot (seat not found, non-participating status, or zero weight).
	VoteWeightByKey(operatorKey crypto.AgentID) (weight uint64, eligible bool)

	// SetVersion returns the monotonic validator set version number. Each
	// lifecycle event that changes the active validator set increments the
	// version. Consensus rounds record this version at open time so votes
	// for already-open rounds are evaluated against the correct snapshot.
	SetVersion() uint64

	// ActiveWeight returns the sum of weights of all participating seats in
	// the snapshot. Used as the BFT denominator: finalization requires
	// yesWeight >= 2/3 of ActiveWeight.
	ActiveWeight() uint64
}

// ErrVoterNotInSnapshot is returned when a voter's key is not found in the
// bound validator set snapshot.
var ErrVoterNotInSnapshot = errors.New("consensus: voter not in validator set snapshot")

// ErrVoterNotInCommittee is returned when a voter is in the snapshot but was
// not selected for the committee that serves a specific round.
var ErrVoterNotInCommittee = errors.New("consensus: voter not in committee for this round")

// CommitteeSource provides deterministic committee selection for consensus
// rounds. When set on VotingRound via SetCommitteeSource, each round binds
// to a committee and only committee members may vote.
//
// The function receives the event ID (round identifier) and returns the set
// of operator keys that are committee members, along with the committee size.
// Returns nil if committee filtering is disabled (all eligible voters accepted).
type CommitteeSource interface {
	// SelectForRound returns the set of operator keys that are committee
	// members for the given round. The returned map must be deterministic:
	// same eventID → same committee on every node.
	// Returns nil to disable committee filtering for this round.
	SelectForRound(eventID event.EventID) map[crypto.AgentID]bool
}

// NodeWeight captures the reputation and stake components that determine a
// node's influence in a virtual voting tally.
type NodeWeight struct {
	// AgentID identifies the node.
	AgentID crypto.AgentID

	// ReputationScore is the agent's 0–10000 basis-point score from the registry.
	ReputationScore uint64

	// StakedAmount is the agent's current collateral in micro-AET.
	StakedAmount uint64

	// Weight is the computed voting influence: (ReputationScore × StakedAmount) / 10000.
	Weight uint64
}

// VoteRecord tracks the evolving state of virtual votes for a single event.
// All fields are updated in place under the VotingRound's mutex.
type VoteRecord struct {
	// EventID is the content-addressed ID of the event under consideration.
	EventID event.EventID

	// ValidatorSetVersion is the snapshot version that was current when this
	// record was created. Votes for this event are evaluated against this
	// snapshot version, not the latest runtime view. Zero when no snapshot
	// was bound at record creation time (backward compatibility).
	ValidatorSetVersion uint64

	// BoundSnapshot is the validator set snapshot captured when this record
	// was created. All weight computations for this round use this snapshot,
	// guaranteeing that every node processing the same votes computes the
	// same tally regardless of when validator set changes propagate. Nil
	// when no snapshot was bound at record creation time (backward compat).
	BoundSnapshot ValidatorSetSource

	// TotalActiveWeight is the sum of weights of ALL eligible validators in
	// the BoundSnapshot. The BFT supermajority threshold is computed against
	// this value, not just the weight of voters who actually voted. Zero
	// when no snapshot was bound (falls back to received-vote weight).
	TotalActiveWeight uint64

	// Round is the number of completed tally iterations for this event.
	// It starts at 1 and increments after each TallyVotes call that does not
	// reach a supermajority. When Round exceeds MaxRounds the event is
	// permanently failed (Finalized stays false, FinalOrder stays 0).
	Round uint64

	// Votes maps each voter's AgentID to their boolean verdict (true = yes).
	Votes map[crypto.AgentID]bool

	// VerifiedValues maps each voter to the verifiedValue they submitted.
	// Used at finalization to compute a deterministic consensus-aggregated
	// verified value (stake-weighted median of approve votes).
	VerifiedValues map[crypto.AgentID]uint64

	// TotalWeight is the sum of all voters' computed weights from the most
	// recent tally. Recomputed on every TallyVotes call.
	TotalWeight uint64

	// YesWeight is the sum of weights of voters who voted yes, from the most
	// recent tally.
	YesWeight uint64

	// NoWeight is the sum of weights of voters who voted no, from the most
	// recent tally. Used for the rejection path: when NoWeight exceeds 1/3
	// of TotalActiveWeight, approval is mathematically impossible.
	NoWeight uint64

	// Finalized is true once a BFT threshold is met (approval or rejection).
	// Once true it never reverts.
	Finalized bool

	// FinalVerdict is the consensus outcome. Only meaningful when Finalized
	// is true. True means the event was approved; false means rejected.
	FinalVerdict bool

	// FinalVerifiedValue is the deterministic consensus-aggregated verified
	// value computed from all approve votes' verified values at finalization
	// time (stake-weighted median). Only meaningful when Finalized is true.
	FinalVerifiedValue uint64

	// FinalOrder is the global ordering sequence number assigned at the moment
	// of finalization. Reflects the order in which events were finalized within
	// this VotingRound. Zero means the event is not yet finalized.
	FinalOrder uint64

	// CallbackFired is true once the finalization callback has been invoked.
	// Prevents duplicate callback invocations from concurrent vote paths.
	// Protected by VotingRound's mutex.
	CallbackFired bool

	// CreatedAt is the wall-clock time the VoteRecord was first created.
	CreatedAt time.Time
}

// ConsensusConfig holds the tunable parameters for the virtual voting algorithm.
type ConsensusConfig struct {
	// SupermajorityThreshold is the minimum ratio of YesWeight to TotalWeight
	// required for finalization. The default 0.667 (≈2/3) provides Byzantine
	// Fault Tolerance against adversaries controlling up to 1/3 of weight.
	SupermajorityThreshold float64

	// MaxRounds is the maximum number of tally iterations an event may undergo
	// before it is permanently failed. Bounds the time an event can remain
	// unresolved under stalled or adversarial voting conditions.
	MaxRounds uint64

	// RoundTimeout is the wall-clock budget per round, available for use by
	// higher-level orchestrators that drive the round lifecycle via timeouts.
	// VotingRound itself does not enforce this; it is exposed for callers.
	RoundTimeout time.Duration

	// MinParticipants is the minimum number of distinct voters whose votes must
	// be recorded before a supermajority can be declared. Prevents premature
	// finalization with a single high-weight voter.
	MinParticipants int
}

// DefaultConsensusConfig returns a production-ready ConsensusConfig with
// 2/3 supermajority, 10 maximum rounds, 5-second round timeout, and a
// minimum of 3 participants.
func DefaultConsensusConfig() *ConsensusConfig {
	return &ConsensusConfig{
		SupermajorityThreshold: 0.667,
		MaxRounds:              10,
		RoundTimeout:           5 * time.Second,
		MinParticipants:        3,
	}
}

// VotingRound manages the virtual voting state for a set of events. It is safe
// for concurrent use by multiple goroutines.
//
// Each VotingRound maintains a monotonic orderSeq counter. Events that reach
// supermajority are assigned the next sequence number, establishing a total
// order over finalized events within this round instance.
type VotingRound struct {
	config          *ConsensusConfig
	registry        *identity.Registry
	validatorSet    ValidatorSetSource  // optional; when set, replaces registry for weight
	committeeSource CommitteeSource     // optional; when set, only committee members may vote
	persistence     VotePersistence     // optional; when set, votes are written through

	records  map[event.EventID]*VoteRecord
	orderSeq uint64 // monotonically increasing; assigned to each finalized event

	mu sync.RWMutex
}

// NewVotingRound constructs a VotingRound backed by the given identity registry.
// If config is nil, DefaultConsensusConfig is used.
func NewVotingRound(config *ConsensusConfig, registry *identity.Registry) *VotingRound {
	if config == nil {
		config = DefaultConsensusConfig()
	}
	return &VotingRound{
		config:   config,
		registry: registry,
		records:  make(map[event.EventID]*VoteRecord),
	}
}

// SetPersistence wires a durable store into the VotingRound. When set, every
// RegisterVote call writes through to the store, and finalization deletes the
// corresponding records so they are not reloaded on restart (HIGH-6).
// Call before any votes are registered.
func (vr *VotingRound) SetPersistence(p VotePersistence) {
	vr.mu.Lock()
	vr.persistence = p
	vr.mu.Unlock()
}

// SetValidatorSet binds a validator-set snapshot to this VotingRound. When
// set, vote eligibility and weight are derived from the snapshot instead of
// the identity registry. The snapshot is immutable, so weights are stable
// across all votes in a round — eliminating races caused by registry updates
// between vote registration and tally.
//
// Call after the lifecycle Reducer has been seeded and before consensus
// traffic begins. Can be updated atomically when the validator set changes.
func (vr *VotingRound) SetValidatorSet(vs ValidatorSetSource) {
	vr.mu.Lock()
	vr.validatorSet = vs
	vr.mu.Unlock()
}

// SetCommitteeSource wires deterministic committee selection into the
// VotingRound. When set, each round's votes are filtered against the
// committee selected for that round's event ID. Voters not in the
// committee are rejected with ErrVoterNotInCommittee.
//
// When nil (the default), all snapshot-eligible voters may vote on any
// round — appropriate for small networks where all validators participate.
// Call before Start.
func (vr *VotingRound) SetCommitteeSource(cs CommitteeSource) {
	vr.mu.Lock()
	vr.committeeSource = cs
	vr.mu.Unlock()
}

// LoadPersistedVotes reloads pending vote state from p on node restart, so
// that in-progress consensus rounds survive restarts (HIGH-6). It iterates
// all event IDs in the persistence layer, loads their votes, and calls
// RegisterVote for each to reconstruct the in-memory VoteRecord.
//
// Call after SetPersistence and before Start — during the boot sequence,
// before the node begins accepting new votes.
func (vr *VotingRound) LoadPersistedVotes(p VotePersistence) error {
	eventIDs, err := p.AllVoteEventIDs()
	if err != nil {
		return fmt.Errorf("consensus: load persisted votes: %w", err)
	}
	for _, eid := range eventIDs {
		votes, err := p.GetVotes(eid)
		if err != nil {
			return fmt.Errorf("consensus: load votes for %s: %w", eid, err)
		}
		for _, v := range votes {
			// RegisterVote will attempt to look up the voter in the registry.
			// Voters that are no longer registered are silently skipped
			// (consistent with tallyVotesLocked's missing-voter handling).
			_ = vr.RegisterVote(event.EventID(eid), crypto.AgentID(v.VoterID), v.Verdict, 0)
		}
	}
	return nil
}

// ComputeWeight returns the voting weight for agentID derived from the current
// registry state:
//
//	weight = (ReputationScore × StakedAmount) / 10000
//
// Returns zero if either factor is zero. Returns an error if the agent is not
// registered. This function is safe to call concurrently; it acquires only the
// registry's own read lock.
func (vr *VotingRound) ComputeWeight(agentID crypto.AgentID) (uint64, error) {
	return vr.computeWeight(agentID)
}

// computeWeight is the lock-free core shared by ComputeWeight and tallyVotesLocked.
//
// When a ValidatorSetSource is bound (via SetValidatorSet), weight is read
// from the snapshot — the seat's pre-computed weight is returned directly.
// This eliminates the race where identity registry mutations between vote
// registration and tally cause different nodes to compute different weights.
//
// When no snapshot is bound, falls back to the identity registry using the
// original reputation × stake / 10000 formula. This preserves backward
// compatibility for tests and deployments that haven't adopted lifecycle
// snapshots yet.
func (vr *VotingRound) computeWeight(agentID crypto.AgentID) (uint64, error) {
	// Snapshot path: preferred when a validator set is bound.
	if vr.validatorSet != nil {
		w, eligible := vr.validatorSet.VoteWeightByKey(agentID)
		if !eligible {
			return 0, fmt.Errorf("%w: %s", ErrVoterNotInSnapshot, agentID)
		}
		return w, nil
	}

	// Fallback: identity registry path (pre-snapshot backward compatibility).
	return vr.computeWeightFromRegistry(agentID)
}

// computeWeightFromRegistry is the original weight computation that reads
// from the identity registry. Retained as fallback when no snapshot is bound.
func (vr *VotingRound) computeWeightFromRegistry(agentID crypto.AgentID) (uint64, error) {
	fp, err := vr.registry.Get(agentID)
	if err != nil {
		return 0, fmt.Errorf("consensus: agent %s not in registry: %w", agentID, err)
	}
	if fp.ReputationScore == 0 || fp.StakedAmount == 0 {
		return 0, nil
	}
	product := new(big.Int).Mul(
		new(big.Int).SetUint64(fp.ReputationScore),
		new(big.Int).SetUint64(fp.StakedAmount),
	)
	result := new(big.Int).Div(product, bigDivisor)
	if !result.IsUint64() {
		return ^uint64(0), nil
	}
	return result.Uint64(), nil
}

// RegisterVote records a vote from voterID for the given eventID and immediately
// triggers a tally. It creates a new VoteRecord on first vote for an event.
//
// The verifiedValue parameter is the voter's assessment of the event's economic
// value. At finalization, a deterministic consensus value is computed from all
// approve votes (stake-weighted median).
//
// Returns:
//   - ErrAlreadyFinalized if the event is already finalized.
//   - ErrRoundExhausted if the event has exceeded MaxRounds without supermajority.
//   - ErrDuplicateVote if voterID has already voted for this event.
//   - An error if voterID is not registered in the identity registry.
func (vr *VotingRound) RegisterVote(eventID event.EventID, voterID crypto.AgentID, vote bool, verifiedValue uint64) error {
	// Fast-path eligibility check before acquiring vr.mu — unregistered agents
	// are rejected early without contending on the write lock.
	if _, err := vr.computeWeight(voterID); err != nil {
		return fmt.Errorf("consensus: voter %s not eligible: %w", voterID, err)
	}

	// Committee membership check: when a committee source is configured,
	// only committee members may vote for a specific round.
	if vr.committeeSource != nil {
		committee := vr.committeeSource.SelectForRound(eventID)
		if committee != nil && !committee[voterID] {
			return fmt.Errorf("%w: voter %s for event %s", ErrVoterNotInCommittee, voterID, eventID)
		}
	}

	vr.mu.Lock()
	defer vr.mu.Unlock()

	record, exists := vr.records[eventID]
	if !exists {
		record = &VoteRecord{
			EventID:        eventID,
			Round:          1,
			Votes:          make(map[crypto.AgentID]bool),
			VerifiedValues: make(map[crypto.AgentID]uint64),
			CreatedAt:      time.Now(),
		}
		// Bind the current validator set snapshot at round-open time.
		// All weight computations for this round use this snapshot,
		// guaranteeing deterministic tally results across nodes.
		if vr.validatorSet != nil {
			record.BoundSnapshot = vr.validatorSet
			record.ValidatorSetVersion = vr.validatorSet.SetVersion()
			record.TotalActiveWeight = vr.validatorSet.ActiveWeight()
		}
		vr.records[eventID] = record
	}

	if record.Finalized {
		return fmt.Errorf("%w: %s", ErrAlreadyFinalized, eventID)
	}
	if record.Round > vr.config.MaxRounds {
		return fmt.Errorf("%w: %s (round %d > max %d)",
			ErrRoundExhausted, eventID, record.Round, vr.config.MaxRounds)
	}
	if _, hasVote := record.Votes[voterID]; hasVote {
		return fmt.Errorf("%w: voter %s already voted for %s", ErrDuplicateVote, voterID, eventID)
	}

	record.Votes[voterID] = vote
	record.VerifiedValues[voterID] = verifiedValue

	// Write-through persistence.
	if vr.persistence != nil {
		if err := vr.persistence.PutVote(string(eventID), string(voterID), vote); err != nil {
			_ = err
		}
	}

	return vr.tallyVotesLocked(record, false)
}

// TallyVotes recomputes weights for eventID and checks for supermajority.
// Unlike the tally triggered by RegisterVote, this explicit call increments
// the round counter when supermajority is not reached. Used by timeout-driven
// orchestrators. Once Round exceeds MaxRounds the event is permanently failed.
func (vr *VotingRound) TallyVotes(eventID event.EventID) error {
	vr.mu.Lock()
	defer vr.mu.Unlock()

	record, ok := vr.records[eventID]
	if !ok {
		return fmt.Errorf("%w: %s", ErrEventNotFound, eventID)
	}
	return vr.tallyVotesLocked(record, true)
}

// tallyVotesLocked is the internal tally implementation. It must be called
// with vr.mu held for writing. It recomputes weights from the bound snapshot,
// checks the BFT supermajority condition against total active weight, and
// either finalizes the event (approval or rejection) or advances the round.
func (vr *VotingRound) tallyVotesLocked(record *VoteRecord, advanceRound bool) error {
	if record.Finalized {
		return nil // already decided; tally is a no-op
	}
	if record.Round > vr.config.MaxRounds {
		return nil // permanently failed; further tallies are no-ops
	}

	// Recompute weights using the BOUND snapshot for determinism.
	// All correct nodes with the same bound snapshot and same votes compute
	// the same weights and reach the same conclusion independently.
	var totalWeight, yesWeight, noWeight uint64
	for voterID, yesVote := range record.Votes {
		w, err := vr.computeWeightForRecord(record, voterID)
		if err != nil {
			continue // voter not in bound snapshot — excluded
		}
		totalWeight += w
		if yesVote {
			yesWeight += w
		} else {
			noWeight += w
		}
	}

	record.TotalWeight = totalWeight
	record.YesWeight = yesWeight
	record.NoWeight = noWeight

	numVoters := len(record.Votes)

	// BFT denominator: total active weight from the bound snapshot.
	// When no snapshot is bound (backward compat), fall back to received vote weight.
	denominator := record.TotalActiveWeight
	if denominator == 0 {
		denominator = totalWeight
	}

	if numVoters >= vr.config.MinParticipants && denominator > 0 {
		// Approval: yesWeight >= 2/3 of total active weight.
		yesRatio := float64(yesWeight) / float64(denominator)
		if yesRatio >= vr.config.SupermajorityThreshold {
			record.Finalized = true
			record.FinalVerdict = true
			record.FinalVerifiedValue = computeWeightedMedian(record)
			vr.orderSeq++
			record.FinalOrder = vr.orderSeq
			if vr.persistence != nil {
				_ = vr.persistence.DeleteVotes(string(record.EventID))
			}
			return nil
		}

		// Rejection: noWeight > 1/3 of total active weight → approval is
		// mathematically impossible. Only applies when we have a bound snapshot
		// (TotalActiveWeight > 0) so we know the true denominator. Without a
		// snapshot we don't know how many validators haven't voted yet.
		if record.TotalActiveWeight > 0 {
			noRatio := float64(noWeight) / float64(denominator)
			if noRatio > (1.0 - vr.config.SupermajorityThreshold) {
				record.Finalized = true
				record.FinalVerdict = false
				record.FinalVerifiedValue = 0
				vr.orderSeq++
				record.FinalOrder = vr.orderSeq
				if vr.persistence != nil {
					_ = vr.persistence.DeleteVotes(string(record.EventID))
				}
				return nil
			}
		}
	}

	if advanceRound {
		record.Round++
	}
	return nil
}

// computeWeightForRecord returns the voting weight for voterID using the
// record's bound snapshot. Falls back to the current snapshot or registry
// when no snapshot was bound at round-open time (backward compatibility).
func (vr *VotingRound) computeWeightForRecord(record *VoteRecord, voterID crypto.AgentID) (uint64, error) {
	if record.BoundSnapshot != nil {
		w, eligible := record.BoundSnapshot.VoteWeightByKey(voterID)
		if !eligible {
			return 0, fmt.Errorf("%w: %s", ErrVoterNotInSnapshot, voterID)
		}
		return w, nil
	}
	// Fallback: no bound snapshot (backward compat for tests without lifecycle).
	return vr.computeWeight(voterID)
}

// weightedValue pairs a verified value with its voter's stake weight.
type weightedValue struct {
	value  uint64
	weight uint64
}

// computeWeightedMedian computes the stake-weighted median of all approve
// votes' verified values. Deterministic: same votes → same result regardless
// of vote arrival order. Only approve votes (Votes[id] == true) contribute.
func computeWeightedMedian(record *VoteRecord) uint64 {
	var entries []weightedValue
	for voterID, approved := range record.Votes {
		if !approved {
			continue
		}
		val := record.VerifiedValues[voterID]
		// Use bound snapshot for weight if available, otherwise skip.
		var w uint64
		if record.BoundSnapshot != nil {
			wt, ok := record.BoundSnapshot.VoteWeightByKey(voterID)
			if ok {
				w = wt
			}
		}
		if w == 0 {
			w = 1 // equal weight fallback when no snapshot
		}
		entries = append(entries, weightedValue{value: val, weight: w})
	}
	if len(entries) == 0 {
		return 0
	}

	// Sort by value for determinism.
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].value != entries[j].value {
			return entries[i].value < entries[j].value
		}
		return entries[i].weight < entries[j].weight
	})

	// Accumulate weight until > 50% of total approve weight.
	var totalW uint64
	for _, e := range entries {
		totalW += e.weight
	}
	threshold := totalW / 2
	var cumW uint64
	for _, e := range entries {
		cumW += e.weight
		if cumW > threshold {
			return e.value
		}
	}
	return entries[len(entries)-1].value
}

// IsFinalized reports whether eventID has reached a finalized supermajority.
// Returns ErrEventNotFound if no votes have been registered for the event.
func (vr *VotingRound) IsFinalized(eventID event.EventID) (bool, error) {
	vr.mu.RLock()
	defer vr.mu.RUnlock()

	record, ok := vr.records[eventID]
	if !ok {
		return false, fmt.Errorf("%w: %s", ErrEventNotFound, eventID)
	}
	return record.Finalized, nil
}

// FinalOrder returns the finalization sequence number assigned to eventID.
// Returns ErrEventNotFound if the event is unknown and ErrNotFinalized if it
// is known but not yet finalized.
func (vr *VotingRound) FinalOrder(eventID event.EventID) (uint64, error) {
	vr.mu.RLock()
	defer vr.mu.RUnlock()

	record, ok := vr.records[eventID]
	if !ok {
		return 0, fmt.Errorf("%w: %s", ErrEventNotFound, eventID)
	}
	if !record.Finalized {
		return 0, fmt.Errorf("%w: %s", ErrNotFinalized, eventID)
	}
	return record.FinalOrder, nil
}

// GetRecord returns a deep copy of the VoteRecord for eventID, allowing callers
// to inspect the full voting state without holding the mutex. The Votes map in
// the returned record is an independent copy safe to read and iterate freely.
// Returns ErrEventNotFound if no votes have been registered for the event.
func (vr *VotingRound) GetRecord(eventID event.EventID) (*VoteRecord, error) {
	vr.mu.RLock()
	defer vr.mu.RUnlock()

	record, ok := vr.records[eventID]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrEventNotFound, eventID)
	}

	// Deep copy: maps must be duplicated so the caller cannot race with
	// concurrent RegisterVote calls on the original.
	copied := *record
	copied.Votes = make(map[crypto.AgentID]bool, len(record.Votes))
	for k, v := range record.Votes {
		copied.Votes[k] = v
	}
	copied.VerifiedValues = make(map[crypto.AgentID]uint64, len(record.VerifiedValues))
	for k, v := range record.VerifiedValues {
		copied.VerifiedValues[k] = v
	}
	return &copied, nil
}

// MarkCallbackFired atomically marks the finalization callback as fired for
// the given event. Returns true if this is the first call (the caller should
// fire the callback). Returns false if the callback was already fired or the
// event is not finalized. This guarantees exactly-once callback invocation
// regardless of how many concurrent vote paths detect finalization.
func (vr *VotingRound) MarkCallbackFired(eventID event.EventID) bool {
	vr.mu.Lock()
	defer vr.mu.Unlock()

	record, ok := vr.records[eventID]
	if !ok || !record.Finalized || record.CallbackFired {
		return false
	}
	record.CallbackFired = true
	return true
}

// FinalizedCount returns the number of events that have reached a finalized
// supermajority within this VotingRound.
func (vr *VotingRound) FinalizedCount() int {
	vr.mu.RLock()
	defer vr.mu.RUnlock()

	n := 0
	for _, r := range vr.records {
		if r.Finalized {
			n++
		}
	}
	return n
}

// PendingCount returns the number of events that have at least one vote recorded
// but have not yet been finalized (including permanently failed events).
func (vr *VotingRound) PendingCount() int {
	vr.mu.RLock()
	defer vr.mu.RUnlock()

	n := 0
	for _, r := range vr.records {
		if !r.Finalized {
			n++
		}
	}
	return n
}
