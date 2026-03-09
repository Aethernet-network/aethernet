// Package ledger implements the AetherNet dual-ledger system.
//
// The Transfer Ledger records value moving between agents. Each entry corresponds
// to a single EventTypeTransfer event in the causal DAG and mirrors its OCS
// settlement lifecycle: Optimistic → Settled | Adjusted.
//
// Balance semantics follow OCS conservatism: only Settled inflows are treated as
// spendable, while both Settled and Optimistic outflows are reserved immediately
// to prevent double-spend under the optimistic execution model.
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

// transferPersistence is the subset of store.Store used by TransferLedger.
// Defining a local interface breaks the import cycle: store imports ledger for
// the TransferEntry type, and ledger uses only this interface — not store directly.
// *store.Store satisfies this interface.
type transferPersistence interface {
	PutTransfer(e *TransferEntry) error
	AllTransfers() ([]*TransferEntry, error)
	DeleteTransfer(id event.EventID) error
}

// Sentinel errors returned by TransferLedger methods.
var (
	// ErrNotTransfer is returned when Record receives an event whose Type is not
	// EventTypeTransfer.
	ErrNotTransfer = errors.New("ledger: event is not a Transfer")

	// ErrDuplicateEntry is returned when Record receives an event whose ID is
	// already present in the ledger.
	ErrDuplicateEntry = errors.New("ledger: event already recorded")

	// ErrEntryNotFound is returned when Settle references an EventID not present
	// in the ledger.
	ErrEntryNotFound = errors.New("ledger: entry not found")

	// ErrInvalidTransition is returned when Settle requests a settlement state
	// progression that violates the OCS lifecycle rules.
	ErrInvalidTransition = errors.New("ledger: invalid settlement state transition")

	// ErrInsufficientBalance is returned when a transfer's FromAgent does not
	// have enough spendable balance to cover the transfer amount.
	ErrInsufficientBalance = errors.New("ledger: insufficient balance")
)

// TransferEntry is a single record in the Transfer Ledger. It captures the
// economic fields of a TransferPayload together with the event's causal position
// and OCS state at the time of recording.
type TransferEntry struct {
	// EventID is the content-addressed ID of the originating Transfer event.
	EventID event.EventID

	// FromAgent is the sending agent's identity.
	FromAgent crypto.AgentID

	// ToAgent is the receiving agent's identity.
	ToAgent crypto.AgentID

	// Amount is the quantity transferred, in micro-AET (or the smallest unit of
	// Currency if non-AET). Copied directly from TransferPayload.Amount.
	Amount uint64

	// Currency identifies the token transferred (e.g., "AET", "USDC").
	Currency string

	// Memo is the optional human-readable annotation from the payload.
	Memo string

	// Timestamp is the Lamport causal clock value of the originating event.
	// Used to establish a deterministic causal ordering for History queries.
	Timestamp uint64

	// Settlement mirrors the OCS state of the originating event and is updated
	// by Settle as the event progresses through the lifecycle.
	Settlement event.SettlementState

	// RecordedAt is the wall-clock time at which Record was called. Used only
	// for operational observability; not used in any balance or ordering logic.
	RecordedAt time.Time

	// IsGenesis marks entries created by FundAgent. Genesis credits are
	// internal funding mechanics and are excluded from History results so
	// that agent-facing history reflects only real transfers.
	IsGenesis bool
}

// TransferLedger is a concurrent, in-memory record of all Transfer events recorded
// on this node. It is safe for simultaneous use by multiple goroutines.
type TransferLedger struct {
	mu      sync.RWMutex
	entries map[event.EventID]*TransferEntry
	store   transferPersistence
}

// SetStore attaches a persistence backend to the TransferLedger. After this call
// Record and Settle write through to the store on every mutation. The argument
// must satisfy transferPersistence; *store.Store from the store package does so.
func (l *TransferLedger) SetStore(s transferPersistence) {
	l.store = s
}

// NewTransferLedger returns an empty, ready-to-use TransferLedger.
func NewTransferLedger() *TransferLedger {
	return &TransferLedger{
		entries: make(map[event.EventID]*TransferEntry),
	}
}

// Record adds a Transfer event to the ledger.
//
// It returns ErrNotTransfer if the event type is not EventTypeTransfer, an error
// if the payload cannot be decoded as TransferPayload, and ErrDuplicateEntry if
// the event ID has already been recorded. On success the entry is stored with the
// event's current SettlementState (typically Optimistic).
func (l *TransferLedger) Record(e *event.Event) error {
	if e.Type != event.EventTypeTransfer {
		return fmt.Errorf("%w: got %q", ErrNotTransfer, e.Type)
	}

	p, err := event.GetPayload[event.TransferPayload](e)
	if err != nil {
		return fmt.Errorf("ledger: Transfer event %s: %w", e.ID, err)
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	if _, exists := l.entries[e.ID]; exists {
		return fmt.Errorf("%w: %s", ErrDuplicateEntry, e.ID)
	}

	// Balance validation: the sender must have enough spendable balance to
	// cover this transfer. Skip the check for the "system" agent which is
	// the source of genesis credits (FundAgent).
	if string(p.FromAgent) != "system" && p.Amount > 0 {
		available := l.balanceLocked(crypto.AgentID(p.FromAgent))
		if available < p.Amount {
			return fmt.Errorf("%w: agent %s balance %d < transfer amount %d",
				ErrInsufficientBalance, p.FromAgent, available, p.Amount)
		}
	}

	l.entries[e.ID] = &TransferEntry{
		EventID:    e.ID,
		FromAgent:  crypto.AgentID(p.FromAgent),
		ToAgent:    crypto.AgentID(p.ToAgent),
		Amount:     p.Amount,
		Currency:   p.Currency,
		Memo:       p.Memo,
		Timestamp:  e.CausalTimestamp,
		Settlement: e.SettlementState,
		RecordedAt: time.Now(),
	}
	if l.store != nil {
		_ = l.store.PutTransfer(l.entries[e.ID])
	}
	return nil
}

// validLedgerTransitions mirrors the OCS lifecycle defined in event.Transition.
// Keeping a local copy avoids mutating the live event just to validate a transition.
var validLedgerTransitions = map[event.SettlementState]map[event.SettlementState]bool{
	event.SettlementOptimistic: {
		event.SettlementSettled:  true,
		event.SettlementAdjusted: true,
	},
	event.SettlementSettled: {
		event.SettlementAdjusted: true,
	},
	event.SettlementAdjusted: {},
}

// Settle updates the settlement state of a previously recorded entry.
//
// The requested state must be a valid forward transition in the OCS lifecycle:
// Optimistic → {Settled, Adjusted} and Settled → Adjusted. Adjusted is terminal.
// Returns ErrEntryNotFound if the EventID is unknown and ErrInvalidTransition if
// the state change is not permitted.
func (l *TransferLedger) Settle(eventID event.EventID, state event.SettlementState) error {
	l.mu.Lock()
	defer l.mu.Unlock()

	entry, ok := l.entries[eventID]
	if !ok {
		return fmt.Errorf("%w: %s", ErrEntryNotFound, eventID)
	}

	allowed, known := validLedgerTransitions[entry.Settlement]
	if !known {
		return fmt.Errorf("%w: unrecognized current state %q", ErrInvalidTransition, entry.Settlement)
	}
	if !allowed[state] {
		return fmt.Errorf("%w: cannot transition from %q to %q", ErrInvalidTransition, entry.Settlement, state)
	}

	entry.Settlement = state
	if l.store != nil {
		_ = l.store.PutTransfer(entry)
	}
	return nil
}

// GetSettlement returns the OCS settlement state recorded for eventID.
// Returns (SettlementOptimistic, false) when the event is not in the ledger.
func (l *TransferLedger) GetSettlement(eventID event.EventID) (event.SettlementState, bool) {
	l.mu.RLock()
	defer l.mu.RUnlock()
	entry, ok := l.entries[eventID]
	if !ok {
		return event.SettlementOptimistic, false
	}
	return entry.Settlement, true
}

// Balance returns the spendable balance for agentID in all currencies combined
// as a single micro-AET equivalent.
//
// The formula is: sum(incoming Settled) − sum(outgoing Settled + Optimistic).
//
// Only Settled inflows are counted as spendable because Optimistic inflows may
// still be challenged and adjusted. Both Settled and Optimistic outflows are
// reserved immediately to prevent double-spend under the optimistic model.
// Adjusted entries are excluded from both sides. If outgoing reservations exceed
// Settled inflows the result is clamped to zero (uint64 cannot represent negative
// values, and the caller can use PendingOutgoing to inspect the pending position).
func (l *TransferLedger) Balance(agentID crypto.AgentID) (uint64, error) {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.balanceLocked(agentID), nil
}

// balanceLocked computes the spendable balance while the caller already holds
// at least a read lock on l.mu. Extracted so Record can call it under the write
// lock without double-locking.
func (l *TransferLedger) balanceLocked(agentID crypto.AgentID) uint64 {
	var inSettled, outReserved uint64
	for _, e := range l.entries {
		if e.ToAgent == agentID && e.Settlement == event.SettlementSettled {
			inSettled += e.Amount
		}
		if e.FromAgent == agentID &&
			(e.Settlement == event.SettlementSettled || e.Settlement == event.SettlementOptimistic) {
			outReserved += e.Amount
		}
	}

	if outReserved >= inSettled {
		return 0
	}
	return inSettled - outReserved
}

// BalanceCheck returns an error if agentID does not have enough spendable
// balance to cover amount. It is a convenience wrapper used by the OCS engine
// before recording a transfer.
func (l *TransferLedger) BalanceCheck(agentID crypto.AgentID, amount uint64) error {
	l.mu.RLock()
	defer l.mu.RUnlock()
	if l.balanceLocked(agentID) < amount {
		return fmt.Errorf("%w: agent %s has insufficient funds for %d", ErrInsufficientBalance, agentID, amount)
	}
	return nil
}

// FundAgent creates a genesis credit entry that grants initial funds to an agent.
// This is the only path through which new balance enters the Transfer Ledger
// outside of normal settled transfers. Used for initial staking and test setup.
// The entry is created in Settled state so it is immediately spendable.
func (l *TransferLedger) FundAgent(agentID crypto.AgentID, amount uint64) error {
	l.mu.Lock()
	defer l.mu.Unlock()

	// Create a synthetic settled entry with a deterministic ID based on the
	// agent and amount, using a "genesis:" prefix to avoid collisions with
	// content-addressed event IDs.
	eid := event.EventID(fmt.Sprintf("genesis:%s:%d:%d", agentID, amount, len(l.entries)))
	if _, exists := l.entries[eid]; exists {
		return fmt.Errorf("%w: %s", ErrDuplicateEntry, eid)
	}

	l.entries[eid] = &TransferEntry{
		EventID:    eid,
		FromAgent:  "system",
		ToAgent:    agentID,
		Amount:     amount,
		Currency:   "AET",
		Memo:       "genesis credit",
		Timestamp:  0,
		Settlement: event.SettlementSettled,
		RecordedAt: time.Now(),
		IsGenesis:  true,
	}
	return nil
}

// TransferFromBucket creates a settled transfer entry from a genesis bucket to
// an agent, properly debiting the source bucket. Unlike FundAgent (which mints
// from "system"), this method decrements the bucket's spendable balance so the
// genesis pool cannot exceed its seeded amount.
//
// Returns ErrInsufficientBalance if fromID does not have enough settled balance
// to cover the allocation.
func (l *TransferLedger) TransferFromBucket(fromID crypto.AgentID, toID crypto.AgentID, amount uint64) error {
	l.mu.Lock()
	defer l.mu.Unlock()

	available := l.balanceLocked(fromID)
	if available < amount {
		return fmt.Errorf("%w: bucket %s balance %d < allocation %d",
			ErrInsufficientBalance, fromID, available, amount)
	}

	eid := event.EventID(fmt.Sprintf("onboarding:%s:%s:%d:%d", fromID, toID, amount, len(l.entries)))
	if _, exists := l.entries[eid]; exists {
		return fmt.Errorf("%w: %s", ErrDuplicateEntry, eid)
	}

	l.entries[eid] = &TransferEntry{
		EventID:    eid,
		FromAgent:  fromID,
		ToAgent:    toID,
		Amount:     amount,
		Currency:   "AET",
		Memo:       "onboarding allocation",
		Timestamp:  0,
		Settlement: event.SettlementSettled,
		RecordedAt: time.Now(),
		IsGenesis:  true,
	}
	if l.store != nil {
		_ = l.store.PutTransfer(l.entries[eid])
	}
	return nil
}

// PendingOutgoing returns the total outgoing amount across all Optimistic
// (not yet settled) transfers initiated by agentID.
//
// This represents value that has left the agent's control under the optimistic
// model but whose final settlement status has not yet been determined.
func (l *TransferLedger) PendingOutgoing(agentID crypto.AgentID) (uint64, error) {
	l.mu.RLock()
	defer l.mu.RUnlock()

	var total uint64
	for _, e := range l.entries {
		if e.FromAgent == agentID && e.Settlement == event.SettlementOptimistic {
			total += e.Amount
		}
	}
	return total, nil
}

// ResetOptimisticOutflows removes all Optimistic-state outflow entries for
// agentID from both the in-memory map and the backing store. It returns the
// number of entries removed.
//
// Optimistic outflows that survive a node restart are stale: the OCS session
// that created them ended before they could be settled or adjusted, but the
// ledger persisted them. Because the balance formula reserves both Settled and
// Optimistic outflows, these stale entries permanently reduce the agent's
// apparent spendable balance even though no real economic obligation remains.
//
// Removing them restores the agent's correct liquid balance (settled inflows
// minus settled outflows only). Any OCS PendingItems referencing the same
// events will expire via checkExpired and produce harmless "entry not found"
// errors in the engine's settle path — those are one-time and non-critical.
//
// This method must only be called at node startup during seed or recovery
// operations, never during normal runtime.
func (l *TransferLedger) ResetOptimisticOutflows(agentID crypto.AgentID) int {
	l.mu.Lock()
	defer l.mu.Unlock()

	var removed int
	for id, e := range l.entries {
		if e.FromAgent == agentID && e.Settlement == event.SettlementOptimistic {
			delete(l.entries, id)
			if l.store != nil {
				_ = l.store.DeleteTransfer(id)
			}
			removed++
		}
	}
	return removed
}

// History returns a page of TransferEntry records in which agentID appears as
// either sender or receiver, ordered by causal timestamp descending (most recent
// first). Entries with identical timestamps are further ordered by EventID
// descending to ensure a deterministic, stable ordering regardless of map
// iteration order.
//
// offset skips the first N matching entries; limit caps the number returned.
// A limit of 0 (or negative) returns all remaining entries after the offset.
// Returns an empty (non-nil) slice when no entries match the page parameters.
func (l *TransferLedger) History(agentID crypto.AgentID, limit int, offset int) ([]*TransferEntry, error) {
	l.mu.RLock()
	defer l.mu.RUnlock()

	var matched []*TransferEntry
	for _, e := range l.entries {
		if e.IsGenesis {
			continue // genesis credits are internal funding mechanics, not transfer history
		}
		if e.FromAgent == agentID || e.ToAgent == agentID {
			matched = append(matched, e)
		}
	}

	// Stable deterministic order: causal timestamp descending, EventID descending
	// as tiebreaker. EventID is a hex content hash so lexicographic ordering is
	// arbitrary but stable — the important property is reproducibility.
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
		return []*TransferEntry{}, nil
	}
	matched = matched[offset:]

	if limit > 0 && limit < len(matched) {
		matched = matched[:limit]
	}

	return matched, nil
}

// LoadTransferLedgerFromStore reconstructs a TransferLedger from a persisted
// store, bypassing event validation (all entries were already validated on
// first Record). The returned ledger has s attached so subsequent mutations
// continue to write through. s must satisfy transferPersistence; *store.Store
// from the store package does so.
func LoadTransferLedgerFromStore(s transferPersistence) (*TransferLedger, error) {
	entries, err := s.AllTransfers()
	if err != nil {
		return nil, fmt.Errorf("ledger: load transfers: %w", err)
	}
	tl := NewTransferLedger()
	tl.store = s
	for _, e := range entries {
		tl.entries[e.EventID] = e
	}
	return tl, nil
}
