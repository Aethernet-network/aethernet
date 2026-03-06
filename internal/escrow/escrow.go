// Package escrow implements fund holding for the AetherNet task marketplace.
//
// When a task is posted, the poster's budget is moved from their balance
// into a virtual escrow bucket ("escrow:<taskID>") via the transfer ledger's
// TransferFromBucket method. On approval the funds flow to the worker; on
// cancellation they are refunded to the poster.
//
// The escrow bucket is a synthetic agent ID — the transfer ledger does not
// restrict AgentID format — so funds sit in an inaccessible virtual account
// until explicitly released or refunded.
package escrow

import (
	"errors"
	"fmt"
	"sync"

	"github.com/Aethernet-network/aethernet/internal/crypto"
	"github.com/Aethernet-network/aethernet/internal/ledger"
)

// EscrowEntry records the poster and amount held for a task.
type EscrowEntry struct {
	TaskID   string
	PosterID crypto.AgentID
	Amount   uint64
}

// ErrEscrowNotFound is returned when an operation references an unknown taskID.
var ErrEscrowNotFound = errors.New("escrow: entry not found")

// Escrow manages task budget escrow using virtual transfer ledger buckets.
// It is safe for concurrent use by multiple goroutines.
type Escrow struct {
	mu      sync.RWMutex
	entries map[string]*EscrowEntry // keyed by taskID
	ledger  *ledger.TransferLedger
}

// New creates a new Escrow backed by tl.
func New(tl *ledger.TransferLedger) *Escrow {
	return &Escrow{
		entries: make(map[string]*EscrowEntry),
		ledger:  tl,
	}
}

// bucketID returns the virtual escrow agent ID for a task.
func bucketID(taskID string) crypto.AgentID {
	return crypto.AgentID("escrow:" + taskID)
}

// Hold moves amount from posterID's balance into the escrow bucket for taskID.
// Returns an error wrapping ledger.ErrInsufficientBalance when the poster
// has insufficient funds.
func (e *Escrow) Hold(taskID string, posterID crypto.AgentID, amount uint64) error {
	if err := e.ledger.TransferFromBucket(posterID, bucketID(taskID), amount); err != nil {
		return fmt.Errorf("escrow: hold for task %s: %w", taskID, err)
	}

	e.mu.Lock()
	e.entries[taskID] = &EscrowEntry{
		TaskID:   taskID,
		PosterID: posterID,
		Amount:   amount,
	}
	e.mu.Unlock()
	return nil
}

// Release moves the escrowed amount from the task bucket to claimerID.
// Returns ErrEscrowNotFound if no escrow exists for taskID.
func (e *Escrow) Release(taskID string, claimerID crypto.AgentID) error {
	e.mu.RLock()
	entry, ok := e.entries[taskID]
	e.mu.RUnlock()
	if !ok {
		return fmt.Errorf("%w: task %s", ErrEscrowNotFound, taskID)
	}

	if err := e.ledger.TransferFromBucket(bucketID(taskID), claimerID, entry.Amount); err != nil {
		return fmt.Errorf("escrow: release for task %s: %w", taskID, err)
	}

	e.mu.Lock()
	delete(e.entries, taskID)
	e.mu.Unlock()
	return nil
}

// Refund returns the escrowed amount from the task bucket back to the poster.
// Returns ErrEscrowNotFound if no escrow exists for taskID.
func (e *Escrow) Refund(taskID string) error {
	e.mu.RLock()
	entry, ok := e.entries[taskID]
	e.mu.RUnlock()
	if !ok {
		return fmt.Errorf("%w: task %s", ErrEscrowNotFound, taskID)
	}

	if err := e.ledger.TransferFromBucket(bucketID(taskID), entry.PosterID, entry.Amount); err != nil {
		return fmt.Errorf("escrow: refund for task %s: %w", taskID, err)
	}

	e.mu.Lock()
	delete(e.entries, taskID)
	e.mu.Unlock()
	return nil
}

// Get returns a copy of the escrow entry for taskID.
// Returns ErrEscrowNotFound if absent.
func (e *Escrow) Get(taskID string) (*EscrowEntry, error) {
	e.mu.RLock()
	defer e.mu.RUnlock()

	entry, ok := e.entries[taskID]
	if !ok {
		return nil, fmt.Errorf("%w: task %s", ErrEscrowNotFound, taskID)
	}
	cp := *entry
	return &cp, nil
}

// TotalEscrowed returns the total amount currently held in escrow across all tasks.
func (e *Escrow) TotalEscrowed() uint64 {
	e.mu.RLock()
	defer e.mu.RUnlock()

	var total uint64
	for _, entry := range e.entries {
		total += entry.Amount
	}
	return total
}
