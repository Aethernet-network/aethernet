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

	// Round is the number of completed tally iterations for this event.
	// It starts at 1 and increments after each TallyVotes call that does not
	// reach a supermajority. When Round exceeds MaxRounds the event is
	// permanently failed (Finalized stays false, FinalOrder stays 0).
	Round uint64

	// Votes maps each voter's AgentID to their boolean verdict (true = yes).
	Votes map[crypto.AgentID]bool

	// TotalWeight is the sum of all voters' computed weights from the most
	// recent tally. Recomputed on every TallyVotes call from the live registry.
	TotalWeight uint64

	// YesWeight is the sum of weights of voters who voted yes, from the most
	// recent tally.
	YesWeight uint64

	// Finalized is true once YesWeight/TotalWeight >= SupermajorityThreshold
	// and at least MinParticipants voters have participated. Once true it
	// never reverts.
	Finalized bool

	// FinalOrder is the global ordering sequence number assigned at the moment
	// of finalization. Reflects the order in which events were finalized within
	// this VotingRound. Zero means the event is not yet finalized.
	FinalOrder uint64

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
	config      *ConsensusConfig
	registry    *identity.Registry
	persistence VotePersistence // optional; when set, votes are written through

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
			_ = vr.RegisterVote(event.EventID(eid), crypto.AgentID(v.VoterID), v.Verdict)
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
// It acquires only the registry's read lock, making it safe to call while vr.mu
// is held (locking order: vr.mu → registry.mu, no reverse dependency).
func (vr *VotingRound) computeWeight(agentID crypto.AgentID) (uint64, error) {
	fp, err := vr.registry.Get(agentID)
	if err != nil {
		return 0, fmt.Errorf("consensus: agent %s not in registry: %w", agentID, err)
	}
	// Overflow-safe arithmetic using math/big: the intermediate product of
	// ReputationScore × StakedAmount can exceed uint64 for large stakes.
	// Using big.Int for the multiplication avoids silent wraparound.
	// If either factor is zero, return zero immediately (short-circuit).
	if fp.ReputationScore == 0 || fp.StakedAmount == 0 {
		return 0, nil
	}
	product := new(big.Int).Mul(
		new(big.Int).SetUint64(fp.ReputationScore),
		new(big.Int).SetUint64(fp.StakedAmount),
	)
	result := new(big.Int).Div(product, bigDivisor)
	// If the result exceeds uint64 range, saturate at MaxUint64.
	if !result.IsUint64() {
		return ^uint64(0), nil
	}
	return result.Uint64(), nil
}

// RegisterVote records a vote from voterID for the given eventID and immediately
// triggers a tally. It creates a new VoteRecord on first vote for an event.
//
// Returns:
//   - ErrAlreadyFinalized if the event is already finalized.
//   - ErrRoundExhausted if the event has exceeded MaxRounds without supermajority.
//   - ErrDuplicateVote if voterID has already voted for this event.
//   - An error if voterID is not registered in the identity registry.
func (vr *VotingRound) RegisterVote(eventID event.EventID, voterID crypto.AgentID, vote bool) error {
	// Validate registry membership before acquiring vr.mu — unregistered agents
	// are rejected early without contending on the write lock.
	if _, err := vr.computeWeight(voterID); err != nil {
		return fmt.Errorf("consensus: voter %s not eligible: %w", voterID, err)
	}

	vr.mu.Lock()
	defer vr.mu.Unlock()

	record, exists := vr.records[eventID]
	if !exists {
		record = &VoteRecord{
			EventID:   eventID,
			Round:     1,
			Votes:     make(map[crypto.AgentID]bool),
			CreatedAt: time.Now(),
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

	// Write-through persistence: persist this vote before tallying so a crash
	// after write but before finalization can be recovered on restart (HIGH-6).
	if vr.persistence != nil {
		if err := vr.persistence.PutVote(string(eventID), string(voterID), vote); err != nil {
			// Non-fatal: consensus continues in-memory; log at warn level.
			// A restarted node may miss this vote but will still converge
			// once the other peers re-broadcast their votes.
			_ = err // caller can check vr.tallyVotesLocked outcome
		}
	}

	return vr.tallyVotesLocked(record)
}

// TallyVotes recomputes YesWeight and TotalWeight for eventID from the current
// registry state and checks whether a supermajority has been reached.
//
// If the supermajority condition is satisfied (YesWeight/TotalWeight >=
// SupermajorityThreshold AND at least MinParticipants have voted), the event is
// marked Finalized and assigned the next FinalOrder sequence number.
//
// If the condition is not met, Round increments. Once Round exceeds MaxRounds
// the event is permanently failed: subsequent RegisterVote calls return
// ErrRoundExhausted and Finalized remains false.
func (vr *VotingRound) TallyVotes(eventID event.EventID) error {
	vr.mu.Lock()
	defer vr.mu.Unlock()

	record, ok := vr.records[eventID]
	if !ok {
		return fmt.Errorf("%w: %s", ErrEventNotFound, eventID)
	}
	return vr.tallyVotesLocked(record)
}

// tallyVotesLocked is the internal tally implementation. It must be called
// with vr.mu held for writing. It recomputes weights from the registry,
// checks the supermajority condition, and either finalizes the event or
// advances the round counter.
func (vr *VotingRound) tallyVotesLocked(record *VoteRecord) error {
	if record.Finalized {
		return nil // already decided; tally is a no-op
	}
	if record.Round > vr.config.MaxRounds {
		return nil // permanently failed; further tallies are no-ops
	}

	// Recompute weights from registry state. The virtual voting invariant:
	// any correct node with the same registry state will compute the same
	// weights and therefore reach the same conclusion independently.
	var totalWeight, yesWeight uint64
	for voterID, yesVote := range record.Votes {
		w, err := vr.computeWeight(voterID)
		if err != nil {
			// Voter disappeared from registry between vote registration and tally.
			// Their vote is silently excluded from weight computation. This is
			// safe: all correct nodes would skip the same missing voter.
			continue
		}
		totalWeight += w
		if yesVote {
			yesWeight += w
		}
	}

	record.TotalWeight = totalWeight
	record.YesWeight = yesWeight

	numVoters := len(record.Votes)

	// Supermajority condition: enough participants AND enough weighted support.
	if numVoters >= vr.config.MinParticipants && totalWeight > 0 {
		ratio := float64(yesWeight) / float64(totalWeight)
		if ratio >= vr.config.SupermajorityThreshold {
			record.Finalized = true
			vr.orderSeq++
			record.FinalOrder = vr.orderSeq
			// Delete persisted votes now that the event is finalized — they
			// must not be reloaded on restart since the event is already settled.
			if vr.persistence != nil {
				_ = vr.persistence.DeleteVotes(string(record.EventID))
			}
			return nil
		}
	}

	// No supermajority yet. Advance the round counter. When Round exceeds
	// MaxRounds the event is permanently failed on the next RegisterVote or
	// TallyVotes call.
	record.Round++
	return nil
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

	// Deep copy: the Votes map must be duplicated so the caller cannot race
	// with concurrent RegisterVote calls on the original.
	copied := *record
	copied.Votes = make(map[crypto.AgentID]bool, len(record.Votes))
	for k, v := range record.Votes {
		copied.Votes[k] = v
	}
	return &copied, nil
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
