// generation.go — Generation Ledger for the AetherNet dual-ledger system.
//
// The Generation Ledger records net-new value created by AI computation. Unlike
// the Transfer Ledger (which tracks existing value moving between agents), every
// entry here represents value that did not previously exist in the system.
//
// OCS lifecycle for generation events:
//
//	Optimistic → Settled  (via Verify — verifiedValue is set, may differ from claim)
//	Optimistic → Adjusted (via Reject — verifiedValue stays zero, claim invalidated)
//	Settled    → Adjusted (supermajority late challenge, consistent with OCS rules)
//
// ContributionScore measures how accurately an agent calibrates its generation
// claims: agents whose verified output consistently matches their claims earn a
// high score; agents who persistently overclaim score proportionally lower.
package ledger

import (
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/Aethernet-network/aethernet/internal/crypto"
	"github.com/Aethernet-network/aethernet/internal/event"
)

// generationPersistence is the subset of store.Store used by GenerationLedger.
// *store.Store from the store package satisfies this interface.
type generationPersistence interface {
	PutGeneration(e *GenerationEntry) error
	AllGenerations() ([]*GenerationEntry, error)
	GetGeneration(id event.EventID) (*GenerationEntry, error)
}

// ErrNotGeneration is returned when Record receives an event whose Type is not
// EventTypeGeneration.
var ErrNotGeneration = errors.New("ledger: event is not a Generation")

// GenerationEntry is a single record in the Generation Ledger. It extends the
// raw GenerationPayload with OCS state tracking and a post-verification field
// that may differ from the original claim.
type GenerationEntry struct {
	// EventID is the content-addressed ID of the originating Generation event.
	EventID event.EventID

	// GeneratingAgent is the agent that performed the AI work.
	GeneratingAgent crypto.AgentID

	// BeneficiaryAgent receives the newly generated value.
	BeneficiaryAgent crypto.AgentID

	// ClaimedValue is the amount of value the generating agent asserted was
	// created, in micro-AET. Set at Record time and never modified.
	ClaimedValue uint64

	// VerifiedValue is the amount of value confirmed by a validator via Verify.
	// Zero until the entry is Settled; may be less than, equal to, or (rarely)
	// greater than ClaimedValue depending on the validator's assessment.
	VerifiedValue uint64

	// EvidenceHash is the content hash of the work artifact from the payload.
	EvidenceHash string

	// TaskDescription is the human-readable task summary from the payload.
	TaskDescription string

	// Timestamp is the Lamport causal clock value of the originating event.
	Timestamp uint64

	// Settlement mirrors the OCS state and is advanced by Verify and Reject.
	Settlement event.SettlementState

	// RecordedAt is the wall-clock time at which Record was called. Used by
	// TotalVerifiedValue to bound the contribution window.
	RecordedAt time.Time
}

// GenerationLedger is a concurrent, in-memory record of all Generation events
// recorded on this node. It is safe for simultaneous use by multiple goroutines.
type GenerationLedger struct {
	mu          sync.RWMutex
	entries     map[event.EventID]*GenerationEntry
	archiveDone chan struct{} // closed by Stop() to terminate the archival goroutine
	store       generationPersistence
}

// SetStore attaches a persistence backend to the GenerationLedger. After this call
// Record, Verify, and Reject write through to the store on every mutation.
// s must satisfy generationPersistence; *store.Store from the store package does so.
func (l *GenerationLedger) SetStore(s generationPersistence) {
	l.store = s
}

// NewGenerationLedger returns an empty, ready-to-use GenerationLedger.
func NewGenerationLedger() *GenerationLedger {
	return &GenerationLedger{
		entries: make(map[event.EventID]*GenerationEntry),
	}
}

// Record adds a Generation event to the ledger.
//
// Returns ErrNotGeneration if the event type is wrong, an error if the payload
// cannot be decoded as GenerationPayload, and ErrDuplicateEntry if the EventID
// is already present. On success the entry is stored with Settlement Optimistic
// and VerifiedValue zero pending a Verify or Reject call.
func (l *GenerationLedger) Record(e *event.Event) error {
	if e.Type != event.EventTypeGeneration {
		return fmt.Errorf("%w: got %q", ErrNotGeneration, e.Type)
	}

	p, err := event.GetPayload[event.GenerationPayload](e)
	if err != nil {
		return fmt.Errorf("ledger: Generation event %s: %w", e.ID, err)
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	if _, exists := l.entries[e.ID]; exists {
		return fmt.Errorf("%w: %s", ErrDuplicateEntry, e.ID)
	}

	l.entries[e.ID] = &GenerationEntry{
		EventID:          e.ID,
		GeneratingAgent:  crypto.AgentID(p.GeneratingAgent),
		BeneficiaryAgent: crypto.AgentID(p.BeneficiaryAgent),
		ClaimedValue:     p.ClaimedValue,
		VerifiedValue:    0,
		EvidenceHash:     p.EvidenceHash,
		TaskDescription:  p.TaskDescription,
		Timestamp:        e.CausalTimestamp,
		Settlement:       event.SettlementOptimistic,
		RecordedAt:       time.Now(),
	}
	if l.store != nil {
		_ = l.store.PutGeneration(l.entries[e.ID])
	}
	return nil
}

// Verify confirms a generation claim, setting the authoritative VerifiedValue
// and advancing the entry to Settled.
//
// verifiedValue may differ from ClaimedValue: validators re-execute or inspect
// the work artifact and assign the value they consider accurate. An agent that
// consistently overclaims will see its ContributionScore fall accordingly.
//
// Verify is only valid from Optimistic state. Returns ErrEntryNotFound if the
// EventID is unknown and ErrInvalidTransition if the entry is not Optimistic.
func (l *GenerationLedger) Verify(eventID event.EventID, verifiedValue uint64) error {
	l.mu.Lock()
	defer l.mu.Unlock()

	entry, ok := l.entries[eventID]
	if !ok {
		return fmt.Errorf("%w: %s", ErrEntryNotFound, eventID)
	}
	if entry.Settlement != event.SettlementOptimistic {
		return fmt.Errorf("%w: Verify requires Optimistic state, entry is %q", ErrInvalidTransition, entry.Settlement)
	}

	entry.VerifiedValue = verifiedValue
	entry.Settlement = event.SettlementSettled
	if l.store != nil {
		_ = l.store.PutGeneration(entry)
	}
	return nil
}

// Reject invalidates a generation claim, advancing the entry to Adjusted.
// VerifiedValue remains zero. The entry persists in the ledger as an immutable
// audit record consistent with the append-only DAG.
//
// Reject is valid from Optimistic or Settled state (the latter covering
// supermajority late challenges consistent with the broader OCS lifecycle).
// Returns ErrEntryNotFound if the EventID is unknown and ErrInvalidTransition
// if the entry is already Adjusted.
func (l *GenerationLedger) Reject(eventID event.EventID) error {
	l.mu.Lock()
	defer l.mu.Unlock()

	entry, ok := l.entries[eventID]
	if !ok {
		return fmt.Errorf("%w: %s", ErrEntryNotFound, eventID)
	}
	if entry.Settlement == event.SettlementAdjusted {
		return fmt.Errorf("%w: %q is terminal", ErrInvalidTransition, event.SettlementAdjusted)
	}

	entry.Settlement = event.SettlementAdjusted
	if l.store != nil {
		_ = l.store.PutGeneration(entry)
	}
	return nil
}

// TotalVerifiedValue returns the sum of VerifiedValue across all Settled entries
// whose RecordedAt timestamp falls within the trailing window ending at time.Now().
//
// A window of zero returns only entries recorded at exactly the current instant,
// which in practice means zero. Pass a sufficiently large duration (e.g., 24*time.Hour)
// to cover the relevant contribution period.
func (l *GenerationLedger) TotalVerifiedValue(window time.Duration) (uint64, error) {
	l.mu.RLock()
	defer l.mu.RUnlock()

	cutoff := time.Now().Add(-window)
	var total uint64
	for _, e := range l.entries {
		if e.Settlement == event.SettlementSettled && !e.RecordedAt.Before(cutoff) {
			total += e.VerifiedValue
		}
	}
	return total, nil
}

// GenerationHistory returns a paginated slice of GenerationEntry records in which
// agentID appears as either GeneratingAgent or BeneficiaryAgent, ordered by causal
// timestamp descending (most recent first). Entries with equal timestamps are
// further ordered by EventID descending for deterministic output.
//
// offset skips the first N matching entries; limit caps the number returned.
// A limit of 0 (or negative) returns all remaining entries after the offset.
// Returns a non-nil empty slice when no entries match the page parameters.
func (l *GenerationLedger) GenerationHistory(agentID crypto.AgentID, limit int, offset int) ([]*GenerationEntry, error) {
	l.mu.RLock()
	defer l.mu.RUnlock()

	var matched []*GenerationEntry
	for _, e := range l.entries {
		if e.GeneratingAgent == agentID || e.BeneficiaryAgent == agentID {
			matched = append(matched, e)
		}
	}

	sort.Slice(matched, func(i, j int) bool {
		if matched[i].Timestamp != matched[j].Timestamp {
			return matched[i].Timestamp > matched[j].Timestamp
		}
		return matched[i].EventID > matched[j].EventID
	})

	total := len(matched)

	if offset < 0 {
		offset = 0
	}
	if offset >= total {
		return []*GenerationEntry{}, nil
	}
	matched = matched[offset:]

	if limit > 0 && limit < len(matched) {
		matched = matched[:limit]
	}

	return matched, nil
}

// ContributionScore measures how accurately agentID calibrates its generation
// claims, expressed as basis points in the range [0, 10000].
//
// The score is computed over all Settled entries where agentID is the
// GeneratingAgent:
//
//	score = (sumVerifiedValue / sumClaimedValue) × 10000
//
// A perfect score of 10000 means the agent's verified output exactly matched or
// exceeded every claim. Persistent overclaiming (ClaimedValue >> VerifiedValue)
// drives the score toward zero. The result is capped at 10000 to handle the
// uncommon case where a validator awards more than the claimed amount.
//
// Agents with no Settled entries as GeneratingAgent receive 5000 — a neutral
// midpoint signalling no track record rather than a good or bad one.
func (l *GenerationLedger) ContributionScore(agentID crypto.AgentID) (uint64, error) {
	l.mu.RLock()
	defer l.mu.RUnlock()

	var sumClaimed, sumVerified uint64
	for _, e := range l.entries {
		if e.GeneratingAgent != agentID || e.Settlement != event.SettlementSettled {
			continue
		}
		sumClaimed += e.ClaimedValue
		sumVerified += e.VerifiedValue
	}

	// No settled track record → neutral default.
	if sumClaimed == 0 {
		return 5000, nil
	}

	// Integer arithmetic: multiply first to preserve precision before dividing.
	// Cap at 10000 to handle validators who award above the claimed amount.
	score := (sumVerified * 10000) / sumClaimed
	if score > 10000 {
		score = 10000
	}
	return score, nil
}

// RecordTaskGeneration records a verified task completion directly in the ledger
// without requiring an OCS event. This is the primary path by which the
// Generation Ledger accumulates verified productive AI computation — every
// settled marketplace task creates one entry here.
//
// The entry is created in Settled state immediately because task completion has
// already been verified by the auto-validator's evidence scoring (score ≥ 0.60).
// verifiedValue = uint64(task.Budget × score.Overall) captures how much of the
// budget was economically validated by the evidence.
//
// Idempotent: returns nil without creating a duplicate if taskID was already
// recorded (safe to call from a retrying ticker).
func (l *GenerationLedger) RecordTaskGeneration(agentID crypto.AgentID, evidenceHash, taskDescription string, verifiedValue uint64, taskID string) error {
	entryID := event.EventID("task-completion:" + taskID)

	l.mu.Lock()
	defer l.mu.Unlock()

	if _, exists := l.entries[entryID]; exists {
		return nil // idempotent
	}

	entry := &GenerationEntry{
		EventID:          entryID,
		GeneratingAgent:  agentID,
		BeneficiaryAgent: agentID,
		ClaimedValue:     verifiedValue,
		VerifiedValue:    verifiedValue, // Already verified by evidence scoring
		EvidenceHash:     evidenceHash,
		TaskDescription:  taskDescription,
		Timestamp:        uint64(time.Now().UnixNano()),
		Settlement:       event.SettlementSettled,
		RecordedAt:       time.Now(),
	}
	l.entries[entryID] = entry
	if l.store != nil {
		_ = l.store.PutGeneration(entry)
	}
	return nil
}

// LoadGenerationLedgerFromStore reconstructs a GenerationLedger from a persisted
// store, bypassing event validation (all entries were already validated on first
// Record). The returned ledger has s attached so subsequent mutations write through.
// s must satisfy generationPersistence; *store.Store from the store package does so.
func LoadGenerationLedgerFromStore(s generationPersistence) (*GenerationLedger, error) {
	entries, err := s.AllGenerations()
	if err != nil {
		return nil, fmt.Errorf("ledger: load generations: %w", err)
	}
	gl := NewGenerationLedger()
	gl.store = s
	for _, e := range entries {
		gl.entries[e.EventID] = e
	}
	return gl, nil
}
