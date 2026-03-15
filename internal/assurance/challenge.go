// Package assurance — challenge.go
//
// ChallengeManager tracks challenge bonds posted when a worker disputes a
// validator's verdict. Challenges are identified by a unique ChallengeID and
// transition through Open → (Succeeded | Failed | Partial).
//
// Bond computation:
//
//	bond = max(ChallengeBondFloor, ChallengeBondRate × taskBudget)
//
// Resolution outcomes:
//
//	Succeeded — challenger was right; bond returned + fraud bounty awarded.
//	Failed    — challenger was wrong; bond forfeited; 50/50 split between the
//	            accused validator (compensation) and the protocol dispute reserve.
//	Partial   — evidence inconclusive; bond returned, no bounty.
package assurance

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"sync"
	"time"

	"github.com/Aethernet-network/aethernet/internal/config"
)

// ---------------------------------------------------------------------------
// Challenge status
// ---------------------------------------------------------------------------

// ChallengeStatus is the lifecycle state of a challenge.
type ChallengeStatus string

const (
	// ChallengeOpen is the initial state after the challenge bond is posted.
	ChallengeOpen ChallengeStatus = "open"
	// ChallengeSucceeded means the challenge was upheld: the validator was
	// found to have acted dishonestly.
	ChallengeSucceeded ChallengeStatus = "succeeded"
	// ChallengeFailed means the challenge was rejected: the validator's
	// original verdict was correct.
	ChallengeFailed ChallengeStatus = "failed"
	// ChallengePartial means the evidence was inconclusive.
	ChallengePartial ChallengeStatus = "partial"
)

// ---------------------------------------------------------------------------
// Challenge record
// ---------------------------------------------------------------------------

// Challenge is the persisted record for a single challenge bond.
type Challenge struct {
	ID           string          `json:"id"`
	TaskID       string          `json:"task_id"`
	ChallengerID string          `json:"challenger_id"`
	TargetID     string          `json:"target_id"` // the validator under challenge
	Bond         uint64          `json:"bond"`
	Status       ChallengeStatus `json:"status"`
	CreatedAt    time.Time       `json:"created_at"`
	ResolvedAt   time.Time       `json:"resolved_at,omitempty"`
}

// ---------------------------------------------------------------------------
// ChallengeResolution
// ---------------------------------------------------------------------------

// ChallengeResolution describes the economic outcome of resolving a challenge.
// The caller must execute the corresponding ledger transfers.
type ChallengeResolution struct {
	// ChallengeID identifies the resolved challenge.
	ChallengeID string
	// Outcome is the final status (Succeeded, Failed, or Partial).
	Outcome ChallengeStatus
	// FraudBounty is the µAET bounty awarded to the challenger on success.
	// Zero for Failed and Partial outcomes.
	FraudBounty uint64
	// RefundedBond is the bond returned to the challenger (Succeeded or Partial).
	// Zero for Failed outcomes.
	RefundedBond uint64
	// ForfeitAmount is the total bond forfeited (Failed outcome only).
	ForfeitAmount uint64
	// AccusedShare is the half of the forfeited bond awarded to the falsely
	// accused validator's party (Failed outcome only).
	AccusedShare uint64
	// ReserveSplit is the half of the forfeited bond allocated to the
	// protocol dispute reserve (Failed outcome only).
	ReserveSplit uint64
}

// ---------------------------------------------------------------------------
// Store interface
// ---------------------------------------------------------------------------

type challengeStore interface {
	PutChallenge(id string, data []byte) error
	GetChallenge(id string) ([]byte, error)
	AllChallenges() (map[string][]byte, error)
}

// ---------------------------------------------------------------------------
// Sentinel errors
// ---------------------------------------------------------------------------

var (
	// ErrChallengeNotFound is returned when a challenge ID does not exist.
	ErrChallengeNotFound = errors.New("challenge: not found")
	// ErrChallengeAlreadyResolved is returned when trying to resolve an
	// already-resolved challenge.
	ErrChallengeAlreadyResolved = errors.New("challenge: already resolved")
	// ErrChallengeInvalidOutcome is returned for unrecognised outcome values.
	ErrChallengeInvalidOutcome = errors.New("challenge: invalid outcome status")
)

// ---------------------------------------------------------------------------
// ChallengeManager
// ---------------------------------------------------------------------------

// ChallengeManager manages challenge bond lifecycle. It is safe for concurrent
// use by multiple goroutines.
type ChallengeManager struct {
	mu         sync.RWMutex
	challenges map[string]*Challenge
	store      challengeStore
	cfg        *config.AssuranceConfig
}

// NewChallengeManager creates a ChallengeManager. store may be nil (in-memory
// only; data not persisted across restarts).
func NewChallengeManager(cfg *config.AssuranceConfig, s challengeStore) *ChallengeManager {
	return &ChallengeManager{
		challenges: make(map[string]*Challenge),
		store:      s,
		cfg:        cfg,
	}
}

// LoadChallengesFromStore creates a ChallengeManager and restores previously
// persisted challenges from s.
func LoadChallengesFromStore(cfg *config.AssuranceConfig, s challengeStore) (*ChallengeManager, error) {
	mgr := NewChallengeManager(cfg, s)
	if s == nil {
		return mgr, nil
	}
	blobs, err := s.AllChallenges()
	if err != nil {
		return nil, fmt.Errorf("challenge: load from store: %w", err)
	}
	for id, blob := range blobs {
		var c Challenge
		if err := json.Unmarshal(blob, &c); err != nil {
			return nil, fmt.Errorf("challenge: unmarshal %s: %w", id, err)
		}
		mgr.challenges[c.ID] = &c
	}
	return mgr, nil
}

// ---------------------------------------------------------------------------
// Bond computation
// ---------------------------------------------------------------------------

// ComputeBond returns the challenge bond required for a task with the given
// budget: max(ChallengeBondFloor, ChallengeBondRate × taskBudget).
func ComputeBond(taskBudget uint64, cfg *config.AssuranceConfig) uint64 {
	computed := uint64(cfg.ChallengeBondRate * float64(taskBudget))
	if computed < cfg.ChallengeBondFloor {
		return cfg.ChallengeBondFloor
	}
	return computed
}

// ---------------------------------------------------------------------------
// OpenChallenge
// ---------------------------------------------------------------------------

// OpenChallenge records a new challenge bond for taskID. bond should be at
// least ComputeBond(taskBudget, cfg) — the caller is responsible for
// collecting the bond before calling this method.
func (m *ChallengeManager) OpenChallenge(taskID, challengerID, targetID string, bond uint64) (*Challenge, error) {
	now := time.Now()
	id := generateChallengeID(taskID, challengerID, now)

	c := &Challenge{
		ID:           id,
		TaskID:       taskID,
		ChallengerID: challengerID,
		TargetID:     targetID,
		Bond:         bond,
		Status:       ChallengeOpen,
		CreatedAt:    now,
	}

	if err := m.persist(c); err != nil {
		return nil, fmt.Errorf("challenge: open: persist failed: %w", err)
	}

	m.mu.Lock()
	m.challenges[id] = c
	m.mu.Unlock()

	return c, nil
}

// ---------------------------------------------------------------------------
// ResolveChallenge
// ---------------------------------------------------------------------------

// ResolveChallenge finalises a challenge with the given outcome. fraudBounty is
// only relevant for ChallengeSucceeded outcomes; pass 0 for other outcomes.
// Returns a ChallengeResolution describing the economic distribution.
func (m *ChallengeManager) ResolveChallenge(challengeID string, outcome ChallengeStatus, fraudBounty uint64) (*ChallengeResolution, error) {
	switch outcome {
	case ChallengeSucceeded, ChallengeFailed, ChallengePartial:
		// valid
	default:
		return nil, fmt.Errorf("%w: %q", ErrChallengeInvalidOutcome, outcome)
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	c, ok := m.challenges[challengeID]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrChallengeNotFound, challengeID)
	}
	if c.Status != ChallengeOpen {
		return nil, fmt.Errorf("%w: %s (status: %s)", ErrChallengeAlreadyResolved, challengeID, c.Status)
	}

	c.Status = outcome
	c.ResolvedAt = time.Now()

	if err := m.persistLocked(c); err != nil {
		return nil, fmt.Errorf("challenge: resolve: persist failed: %w", err)
	}

	res := &ChallengeResolution{
		ChallengeID: challengeID,
		Outcome:     outcome,
	}

	switch outcome {
	case ChallengeSucceeded:
		res.FraudBounty = fraudBounty
		res.RefundedBond = c.Bond
	case ChallengeFailed:
		res.ForfeitAmount = c.Bond
		res.AccusedShare = c.Bond / 2
		res.ReserveSplit = c.Bond - res.AccusedShare
	case ChallengePartial:
		res.RefundedBond = c.Bond
	}

	return res, nil
}

// ---------------------------------------------------------------------------
// GetChallenge
// ---------------------------------------------------------------------------

// GetChallenge returns a copy of the challenge record for the given ID.
// Returns ErrChallengeNotFound when no record exists.
func (m *ChallengeManager) GetChallenge(challengeID string) (*Challenge, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	c, ok := m.challenges[challengeID]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrChallengeNotFound, challengeID)
	}
	cp := *c
	return &cp, nil
}

// ---------------------------------------------------------------------------
// Internal helpers
// ---------------------------------------------------------------------------

// persist writes c to the backing store (no lock required; c must not be
// concurrently mutated by the caller).
func (m *ChallengeManager) persist(c *Challenge) error {
	if m.store == nil {
		return nil
	}
	data, err := json.Marshal(c)
	if err != nil {
		return fmt.Errorf("challenge: marshal %s: %w", c.ID, err)
	}
	if err := m.store.PutChallenge(c.ID, data); err != nil {
		slog.Error("challenge: failed to persist", "id", c.ID, "err", err)
		return err
	}
	return nil
}

// persistLocked writes c while m.mu is already held.
func (m *ChallengeManager) persistLocked(c *Challenge) error {
	return m.persist(c)
}

// generateChallengeID produces a unique ID from taskID + challengerID + timestamp.
func generateChallengeID(taskID, challengerID string, t time.Time) string {
	raw := taskID + "|" + challengerID + "|" + strconv.FormatInt(t.UnixNano(), 10)
	sum := sha256.Sum256([]byte(raw))
	return "chal-" + hex.EncodeToString(sum[:12])
}
