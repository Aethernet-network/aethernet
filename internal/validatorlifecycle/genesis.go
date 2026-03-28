package validatorlifecycle

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"

	"github.com/Aethernet-network/aethernet/internal/crypto"
	"github.com/Aethernet-network/aethernet/internal/genesis"
)

// ---------------------------------------------------------------------------
// Errors
// ---------------------------------------------------------------------------

var (
	// ErrManifestEmpty is returned when the manifest contains no entries.
	ErrManifestEmpty = errors.New("validatorlifecycle: genesis manifest contains no entries")

	// ErrManifestDuplicateID is returned when two entries share a ValidatorID.
	ErrManifestDuplicateID = errors.New("validatorlifecycle: duplicate validator_id in manifest")

	// ErrManifestDuplicateKey is returned when two entries share a consensus key.
	ErrManifestDuplicateKey = errors.New("validatorlifecycle: duplicate consensus_public_key in manifest")

	// ErrManifestMissingField is returned when a required field is empty.
	ErrManifestMissingField = errors.New("validatorlifecycle: required manifest field is empty")

	// ErrManifestInvalidStake is returned when bonded stake is zero.
	ErrManifestInvalidStake = errors.New("validatorlifecycle: bonded_stake must be > 0")

	// ErrManifestInvalidKeyEpoch is returned when key epoch is zero.
	ErrManifestInvalidKeyEpoch = errors.New("validatorlifecycle: key_epoch must be > 0")
)

// ---------------------------------------------------------------------------
// Genesis Manifest
// ---------------------------------------------------------------------------

// GenesisManifestEntry describes a single validator in the genesis set.
// All fields are explicit — no defaults are inferred.
type GenesisManifestEntry struct {
	// ValidatorID is the stable seat identifier for this genesis validator.
	ValidatorID ValidatorID `json:"validator_id"`

	// OperatorAgentID is the agent identity operating this seat.
	OperatorAgentID crypto.AgentID `json:"operator_agent_id"`

	// ConsensusPublicKey is the Ed25519 public key used for consensus
	// signing. May equal OperatorAgentID when the operator key and
	// consensus key are the same.
	ConsensusPublicKey crypto.AgentID `json:"consensus_public_key"`

	// KeyEpoch is the initial key epoch (must be 1 for genesis entries).
	KeyEpoch uint64 `json:"key_epoch"`

	// BondedStake is the initial staked amount in µAET.
	BondedStake uint64 `json:"bonded_stake"`

	// InitialStatus is the status the seat enters. Genesis validators
	// typically enter Active directly, bypassing PendingJoin and
	// Probationary.
	InitialStatus SeatStatus `json:"initial_status"`

	// ReputationBaseline is the initial reputation score in basis points
	// (0–10000). Used for cross-referencing with the identity registry.
	// Does not affect lifecycle state directly.
	ReputationBaseline uint64 `json:"reputation_baseline,omitempty"`

	// Categories is the optional list of verification domains this
	// validator specializes in. Empty means all categories.
	Categories []string `json:"categories,omitempty"`
}

// GenesisManifest is the complete genesis validator set definition. All
// nodes must load the same manifest to produce identical snapshot version 1.
type GenesisManifest struct {
	// Entries is the ordered list of genesis validator seats.
	Entries []GenesisManifestEntry `json:"entries"`
}

// Validate checks structural invariants of the manifest:
//   - at least one entry
//   - no duplicate ValidatorIDs
//   - no duplicate consensus keys
//   - all required fields present
//   - valid stake and key epoch
func (m *GenesisManifest) Validate() error {
	if len(m.Entries) == 0 {
		return ErrManifestEmpty
	}
	seenIDs := make(map[ValidatorID]bool, len(m.Entries))
	seenKeys := make(map[crypto.AgentID]bool, len(m.Entries))

	for i, e := range m.Entries {
		if e.ValidatorID == "" {
			return fmt.Errorf("%w: validator_id at index %d", ErrManifestMissingField, i)
		}
		if seenIDs[e.ValidatorID] {
			return fmt.Errorf("%w: %s", ErrManifestDuplicateID, e.ValidatorID)
		}
		seenIDs[e.ValidatorID] = true

		if e.OperatorAgentID == "" {
			return fmt.Errorf("%w: operator_agent_id for %s", ErrManifestMissingField, e.ValidatorID)
		}
		if e.ConsensusPublicKey == "" {
			return fmt.Errorf("%w: consensus_public_key for %s", ErrManifestMissingField, e.ValidatorID)
		}
		if seenKeys[e.ConsensusPublicKey] {
			return fmt.Errorf("%w: %s", ErrManifestDuplicateKey, e.ConsensusPublicKey)
		}
		seenKeys[e.ConsensusPublicKey] = true

		if e.BondedStake == 0 {
			return fmt.Errorf("%w: validator %s", ErrManifestInvalidStake, e.ValidatorID)
		}
		if e.KeyEpoch == 0 {
			return fmt.Errorf("%w: validator %s", ErrManifestInvalidKeyEpoch, e.ValidatorID)
		}
		if e.InitialStatus == "" {
			return fmt.Errorf("%w: initial_status for %s", ErrManifestMissingField, e.ValidatorID)
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// Default Testnet Manifest
// ---------------------------------------------------------------------------

// DefaultTestnetManifest returns the standard genesis validator set for the
// AetherNet testnet. This is deterministic and identical on every node.
//
// The testnet genesis set contains one validator: the shared
// "testnet-validator" identity funded and staked during seedGenesis.
func DefaultTestnetManifest() *GenesisManifest {
	return &GenesisManifest{
		Entries: []GenesisManifestEntry{
			{
				ValidatorID:        ValidatorID("genesis-seat-testnet-validator"),
				OperatorAgentID:    crypto.AgentID(genesis.GenesisValidatorID),
				ConsensusPublicKey: crypto.AgentID(genesis.GenesisValidatorID),
				KeyEpoch:           1,
				BondedStake:        genesis.GenesisValidatorStake,
				InitialStatus:      SeatActive,
				ReputationBaseline: 5000,
			},
		},
	}
}

// SingleNodeManifest creates a genesis manifest with a single Active seat
// using the provided agent ID (hex-encoded Ed25519 public key) as both
// operator and consensus key. This ensures the auto-validator can vote on
// its own events in single-node dev mode without a shared manifest file.
func SingleNodeManifest(agentID crypto.AgentID) *GenesisManifest {
	return &GenesisManifest{
		Entries: []GenesisManifestEntry{
			{
				ValidatorID:        ValidatorID("seat-" + string(agentID)[:16]),
				OperatorAgentID:    agentID,
				ConsensusPublicKey: agentID,
				KeyEpoch:           1,
				BondedStake:        genesis.GenesisValidatorStake,
				InitialStatus:      SeatActive,
				ReputationBaseline: 5000,
			},
		},
	}
}

// ---------------------------------------------------------------------------
// Manifest Loading
// ---------------------------------------------------------------------------

// LoadManifestFromFile reads a GenesisManifest from a JSON file. Returns an
// error if the file cannot be read or the manifest fails validation.
func LoadManifestFromFile(path string) (*GenesisManifest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("validatorlifecycle: read manifest: %w", err)
	}
	var m GenesisManifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("validatorlifecycle: parse manifest: %w", err)
	}
	if err := m.Validate(); err != nil {
		return nil, err
	}
	return &m, nil
}

// ---------------------------------------------------------------------------
// Reducer Seeding
// ---------------------------------------------------------------------------

// SeedReducerFromManifest creates a new Reducer and applies the genesis
// manifest to produce snapshot version 1. Genesis validators enter their
// declared InitialStatus directly — the normal PendingJoin → Probationary →
// Active progression is bypassed.
//
// The returned Reducer's Version() will equal the number of genesis entries
// (one Apply per seat). The caller should take a Snapshot() and log its
// digest to verify cross-node agreement.
func SeedReducerFromManifest(manifest *GenesisManifest) (*Reducer, error) {
	if err := manifest.Validate(); err != nil {
		return nil, err
	}

	r := NewReducer()

	for _, entry := range manifest.Entries {
		// Step 1: Create the seat via a Join event.
		joinEv := LifecycleEvent{
			Kind:        EventJoin,
			EventID:     "genesis:" + string(entry.ValidatorID),
			CausalTS:    0,
			SeatID:      entry.ValidatorID,
			OperatorKey: entry.ConsensusPublicKey,
			StakeAmount: entry.BondedStake,
			IsGenesis:   true,
		}
		if err := r.Apply(joinEv); err != nil {
			return nil, fmt.Errorf("validatorlifecycle: seed join for %s: %w", entry.ValidatorID, err)
		}

		// Step 2: Advance the seat to the declared initial status.
		// Genesis validators skip PendingJoin → Probationary → Active
		// progression by applying synthetic lifecycle events.
		if err := advanceSeatToStatus(r, entry); err != nil {
			return nil, err
		}
	}

	return r, nil
}

// advanceSeatToStatus applies synthetic lifecycle events to move a genesis
// seat from PendingJoin to its declared InitialStatus.
func advanceSeatToStatus(r *Reducer, entry GenesisManifestEntry) error {
	seatID := entry.ValidatorID

	switch entry.InitialStatus {
	case SeatPendingJoin:
		// Already in PendingJoin after Join — nothing to do.
		return nil

	case SeatProbationary:
		seat := r.seats[seatID]
		if !seat.Status.CanTransitionTo(SeatProbationary) {
			return fmt.Errorf("validatorlifecycle: cannot advance genesis seat %s from %s to %s",
				seatID, seat.Status, SeatProbationary)
		}
		r.version++
		seat.Status = SeatProbationary
		seat.Weight = entry.BondedStake / 2
		seat.EffectiveFromVersion = r.version
		return nil

	case SeatActive:
		seat := r.seats[seatID]
		if !seat.Status.CanTransitionTo(SeatProbationary) {
			return fmt.Errorf("validatorlifecycle: cannot advance genesis seat %s from %s to %s",
				seatID, seat.Status, SeatProbationary)
		}
		seat.Status = SeatProbationary
		if !seat.Status.CanTransitionTo(SeatActive) {
			return fmt.Errorf("validatorlifecycle: cannot advance genesis seat %s from %s to %s",
				seatID, seat.Status, SeatActive)
		}
		r.version++
		seat.Status = SeatActive
		seat.Weight = entry.BondedStake
		seat.ActivatedCausalTS = 0
		seat.EffectiveFromVersion = r.version
		return nil

	default:
		return fmt.Errorf("validatorlifecycle: unsupported initial_status %q for genesis seat %s",
			entry.InitialStatus, seatID)
	}
}

// ---------------------------------------------------------------------------
// Snapshot Verification Helpers
// ---------------------------------------------------------------------------

// LogSnapshotDigest computes and logs the snapshot digest at slog.Info level.
// Operators compare this across nodes to verify identical genesis state.
func LogSnapshotDigest(r *Reducer) string {
	snap := r.Snapshot()
	digest := snap.ComputeDigest()
	slog.Info("validator lifecycle: genesis snapshot seeded",
		"version", snap.Version,
		"total_seats", len(snap.Seats),
		"active_seats", snap.ActiveSeatCount,
		"total_active_weight", snap.TotalActiveWeight,
		"digest", digest,
	)
	return digest
}
