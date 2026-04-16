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
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"

	"github.com/Aethernet-network/aethernet/internal/crypto"
	"github.com/Aethernet-network/aethernet/internal/event"
	"github.com/Aethernet-network/aethernet/internal/ledger"
)

// EscrowEntry records the poster and amount held for a task.
// WorkerPaid, ValidatorPaid, and TreasuryPaid track which ReleaseNet
// disbursements have succeeded, enabling idempotent retry: if ReleaseNet
// fails mid-way, a second call skips already-completed transfers (CRITICAL-3).
type EscrowEntry struct {
	TaskID   string         `json:"task_id"`
	PosterID crypto.AgentID `json:"poster_id"`
	Amount   uint64         `json:"amount"`

	// FundingTransferRef is the EventID of the canonical Transfer (Reason=escrow-lock)
	// that funded this escrow. Recorded by RegisterEscrow after DAG validation.
	// Empty for pre-Part-E legacy entries loaded from persistent storage; LoadFromStore
	// tolerates the zero value.
	FundingTransferRef event.EventID `json:"funding_transfer_ref,omitempty"`

	WorkerPaid    bool `json:"worker_paid"`    // true once the worker's net share has been transferred
	ValidatorPaid bool `json:"validator_paid"` // true once the validator's share has been transferred
	TreasuryPaid  bool `json:"treasury_paid"`  // true once the treasury's share has been transferred

	// Extended per-recipient paid tracking for ReleaseSettlement (commit 9).
	// Per-validator and per-gen-ledger-recipient idempotency guards.
	ValidatorsPaid   map[string]bool `json:"validators_paid,omitempty"`
	GenLedgerPaid    map[string]bool `json:"gen_ledger_paid,omitempty"`
	PosterRefundPaid bool            `json:"poster_refund_paid,omitempty"`
}

// escrowPersistence is the subset of store.Store used by Escrow for durable writes.
// *store.Store from the store package satisfies this interface.
type escrowPersistence interface {
	PutEscrow(entry *EscrowEntry) error
	GetEscrow(taskID string) (*EscrowEntry, error)
	AllEscrowEntries() ([]*EscrowEntry, error)
	DeleteEscrow(taskID string) error
}

// ErrEscrowNotFound is returned when an operation references an unknown taskID.
var ErrEscrowNotFound = errors.New("escrow: entry not found")

// Error sentinels for RegisterEscrow funding-transfer validation.
//
// ErrFundingTransferNotProjected indicates the fundingTransferRef does not
// yet resolve in the DAG. Callers classify this as a prerequisite failure
// handled by Part D deferral.
//
// The other three are hard failures (malformed/malicious event, misconfigured
// registration).
var (
	ErrDAGReaderNotConfigured      = errors.New("escrow: DAG reader not configured")
	ErrFundingTransferNotProjected = errors.New("escrow: funding transfer not projected in DAG")
	ErrFundingTransferWrongType    = errors.New("escrow: funding transfer event is not a Transfer")
	ErrFundingTransferMismatch     = errors.New("escrow: funding transfer does not match registration")
)

// Escrow manages task budget escrow using virtual transfer ledger buckets.
// It is safe for concurrent use by multiple goroutines.
type Escrow struct {
	mu        sync.RWMutex
	entries   map[string]*EscrowEntry // keyed by taskID
	ledger    *ledger.TransferLedger
	store     escrowPersistence // optional; nil = in-memory only
	dagReader DAGReader         // optional until RegisterEscrow is called; then required
}

// New creates a new Escrow backed by tl.
func New(tl *ledger.TransferLedger) *Escrow {
	return &Escrow{
		entries: make(map[string]*EscrowEntry),
		ledger:  tl,
	}
}

// SetStore attaches a persistence backend. After this call Hold, ReleaseNet,
// and Refund write through to the store so escrow entries survive restarts.
// s must satisfy escrowPersistence; *store.Store from the store package does so.
func (e *Escrow) SetStore(s escrowPersistence) {
	e.store = s
}

// SetDAGReader attaches the DAG reader used by RegisterEscrow for funding-transfer
// validation. Must be called before RegisterEscrow is invoked; RegisterEscrow
// returns ErrDAGReaderNotConfigured if called with no reader attached.
//
// Idempotent: calling twice replaces the reader. Safe to call at startup before
// any event-processing goroutine is started.
func (e *Escrow) SetDAGReader(r DAGReader) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.dagReader = r
}

// persist writes entry to the store (best-effort; errors are logged not propagated).
// Must be called while NOT holding e.mu — it acquires its own view of the entry.
func (e *Escrow) persist(entry *EscrowEntry) {
	if e.store == nil {
		return
	}
	if err := e.store.PutEscrow(entry); err != nil {
		slog.Error("escrow: failed to persist entry", "task_id", entry.TaskID, "err", err)
	}
}

// LoadFromStore reconstructs in-flight escrow entries from the persistence store
// on node restart. Call before serving requests. s must satisfy escrowPersistence.
func LoadFromStore(tl *ledger.TransferLedger, s escrowPersistence) (*Escrow, error) {
	entries, err := s.AllEscrowEntries()
	if err != nil {
		return nil, fmt.Errorf("escrow: load from store: %w", err)
	}
	escrow := New(tl)
	escrow.store = s
	for _, entry := range entries {
		escrow.entries[entry.TaskID] = entry
	}
	return escrow, nil
}

// bucketID returns the virtual escrow agent ID for a task.
func bucketID(taskID string) crypto.AgentID {
	return crypto.AgentID("escrow:" + taskID)
}

// IsLocked reports whether an escrow entry exists for taskID.
func (e *Escrow) IsLocked(taskID string) bool {
	e.mu.RLock()
	defer e.mu.RUnlock()
	_, exists := e.entries[taskID]
	return exists
}

// Empty reports whether the escrow holds any in-flight entries. Used by
// the projection registry StateProbe — see
// docs/plans/2026-04-12-reputation-step-2-retrofit-projections.md §D7.
func (e *Escrow) Empty(_ context.Context) (bool, error) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return len(e.entries) == 0, nil
}

// Hold is the legacy combined fund-and-register primitive retained for the
// marketplace binary's application-layer escrow only.
//
// Production protocol code (applicator, verification-consensus settler,
// task-settler closure) uses RegisterEscrow instead — the canonical Transfer
// has already moved funds on those paths, so Hold's TransferFromBucket call
// would be a duplicate debit (the F1 bug). See the 2026-04-15 audit
// (docs/audits/2026-04-15-escrow-hold-callers-audit.md) entry C3-2 for the
// marketplace-only retention rationale, and the Part E plan
// (docs/plans/2026-04-15-f3b-part-e-escrow-hardening.md §8) for the lint
// exemption at internal/marketplace/server.go that permits the one retained
// caller. When the marketplace-escrow-integration workstream lands, Hold is
// removed.
//
// Moves amount from posterID's balance into the escrow bucket for taskID.
// Returns an error wrapping ledger.ErrInsufficientBalance when the poster
// has insufficient funds.
func (e *Escrow) Hold(taskID string, posterID crypto.AgentID, amount uint64) error {
	// Record the entry before the ledger transfer so a panic between the two
	// operations cannot strand funds in the bucket with no entry to find them.
	e.mu.Lock()
	entry := &EscrowEntry{
		TaskID:   taskID,
		PosterID: posterID,
		Amount:   amount,
	}
	e.entries[taskID] = entry
	e.mu.Unlock()

	e.persist(entry)

	if err := e.ledger.TransferFromBucket(posterID, bucketID(taskID), amount); err != nil {
		e.mu.Lock()
		delete(e.entries, taskID)
		e.mu.Unlock()
		if e.store != nil {
			if delErr := e.store.DeleteEscrow(taskID); delErr != nil {
				slog.Error("escrow: failed to delete rolled-back entry", "task_id", taskID, "err", delErr)
			}
		}
		return fmt.Errorf("escrow: hold for task %s: %w", taskID, err)
	}
	return nil
}

// RegisterEscrow records an EscrowEntry for taskID without moving funds.
//
// The canonical Transfer event identified by fundingTransferRef must already
// be projected in the DAG and must match the registration's amount, poster,
// bucket (ToAgent="escrow:"+taskID), taskID, and reason ("escrow-lock"). If the
// Transfer is not yet projected, RegisterEscrow returns ErrFundingTransferNotProjected
// — callers treat this as a prerequisite failure handled by Part D deferral.
// Any other validation failure returns one of the hard sentinels
// (ErrFundingTransferWrongType, ErrFundingTransferMismatch) with a diagnostic.
//
// Idempotent: if an entry already exists for taskID, RegisterEscrow is a no-op
// and returns nil. This matches the peer-node catch-up contract.
//
// Does not call ledger.TransferFromBucket under any path. That distinction is
// the point of this method — the canonical Transfer has already moved funds
// before RegisterEscrow is reached.
func (e *Escrow) RegisterEscrow(
	taskID string,
	poster crypto.AgentID,
	amount uint64,
	fundingTransferRef event.EventID,
) error {
	e.mu.RLock()
	reader := e.dagReader
	_, alreadyExists := e.entries[taskID]
	e.mu.RUnlock()

	if alreadyExists {
		return nil
	}
	if reader == nil {
		return ErrDAGReaderNotConfigured
	}

	fundingEvt, err := reader.Get(fundingTransferRef)
	if err != nil {
		return fmt.Errorf("%w: %s: %v", ErrFundingTransferNotProjected, fundingTransferRef, err)
	}
	if fundingEvt.Type != event.EventTypeTransfer {
		return fmt.Errorf("%w: %s is %s", ErrFundingTransferWrongType, fundingTransferRef, fundingEvt.Type)
	}
	tp, err := event.GetPayload[event.TransferPayload](fundingEvt)
	if err != nil {
		return fmt.Errorf("%w: decode payload: %v", ErrFundingTransferWrongType, err)
	}

	switch {
	case tp.Reason != "escrow-lock":
		return fmt.Errorf("%w: reason=%q want escrow-lock", ErrFundingTransferMismatch, tp.Reason)
	case tp.FromAgent != string(poster):
		return fmt.Errorf("%w: from_agent=%s want %s", ErrFundingTransferMismatch, tp.FromAgent, poster)
	case tp.ToAgent != "escrow:"+taskID:
		return fmt.Errorf("%w: to_agent=%s want escrow:%s", ErrFundingTransferMismatch, tp.ToAgent, taskID)
	case tp.Amount != amount:
		return fmt.Errorf("%w: amount=%d want %d", ErrFundingTransferMismatch, tp.Amount, amount)
	case tp.TaskID != taskID:
		return fmt.Errorf("%w: task_id=%s want %s", ErrFundingTransferMismatch, tp.TaskID, taskID)
	}

	e.mu.Lock()
	if _, alreadyExists := e.entries[taskID]; alreadyExists {
		e.mu.Unlock()
		return nil
	}
	entry := &EscrowEntry{
		TaskID:             taskID,
		PosterID:           poster,
		Amount:             amount,
		FundingTransferRef: fundingTransferRef,
	}
	e.entries[taskID] = entry
	e.mu.Unlock()

	e.persist(entry)
	return nil
}

// RegisterEscrowForTest records an EscrowEntry without DAG validation.
//
// SCOPE: This method exists for the test-helpers module and same-package tests.
// Production code must not call this method. The no-bypass CI lint (built in
// Part C of the F3-B workstream) will fail the build if any production package
// imports or invokes this method. Until that lint lands, the constraint is
// enforced by code review.
//
// See docs/plans/2026-04-15-f3b-part-e-escrow-hardening.md §3.1 and
// docs/plans/2026-04-15-settlement-consensus-integrity-fix.md §6.1.
//
// Idempotent under duplicate call: second invocation with an existing taskID
// is a no-op returning nil.
func (e *Escrow) RegisterEscrowForTest(
	taskID string,
	poster crypto.AgentID,
	amount uint64,
	fundingTransferRef event.EventID,
) error {
	e.mu.Lock()
	if _, exists := e.entries[taskID]; exists {
		e.mu.Unlock()
		return nil
	}
	entry := &EscrowEntry{
		TaskID:             taskID,
		PosterID:           poster,
		Amount:             amount,
		FundingTransferRef: fundingTransferRef,
	}
	e.entries[taskID] = entry
	e.mu.Unlock()

	e.persist(entry)
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

	if err := e.ledger.TransferFromBucketLabeled(bucketID(taskID), claimerID, entry.Amount, "escrow-release:full-release", false); err != nil {
		return fmt.Errorf("escrow: release for task %s: %w", taskID, err)
	}

	e.mu.Lock()
	delete(e.entries, taskID)
	e.mu.Unlock()
	return nil
}

// ReleaseNet releases the escrowed budget across three recipients — worker,
// validator, and treasury — all from the task's escrow bucket. After the
// transfers the escrow bucket balance is zero and the entry is deleted.
//
// Parameters:
//   - claimerID / netAmount:    worker's share (budget minus fee)
//   - validatorID / validatorAmount: validator's share of the fee (80%)
//   - treasuryID / treasuryAmount:   treasury's share of the fee (20%)
//
// The caller is responsible for computing the split using fees.CalculateFee,
// fees.ValidatorShare, and fees.TreasuryShare before calling. Any remainder
// (burned amount) is intentionally left in the bucket if non-zero.
//
// Returns ErrEscrowNotFound if no escrow exists for taskID.
func (e *Escrow) ReleaseNet(
	taskID string,
	claimerID crypto.AgentID, netAmount uint64,
	validatorID crypto.AgentID, validatorAmount uint64,
	treasuryID crypto.AgentID, treasuryAmount uint64,
) error {
	e.mu.Lock()
	entry, ok := e.entries[taskID]
	e.mu.Unlock()
	if !ok {
		return fmt.Errorf("%w: task %s", ErrEscrowNotFound, taskID)
	}

	bucket := bucketID(taskID)

	// Each disbursement is guarded by a paid flag. If a previous ReleaseNet call
	// partially succeeded and returned an error, a retry will skip the already-
	// completed transfers, preventing double payment (CRITICAL-3: idempotency).

	// Transfer the worker's net share (skip if already paid on a prior attempt).
	if !entry.WorkerPaid {
		if err := e.ledger.TransferFromBucketLabeled(bucket, claimerID, netAmount, "escrow-release-net:worker", false); err != nil {
			return fmt.Errorf("escrow: release-net worker for task %s: %w", taskID, err)
		}
		e.mu.Lock()
		entry.WorkerPaid = true
		e.mu.Unlock()
		e.persist(entry)
	}

	// Distribute validator share from the remaining escrow balance.
	if validatorAmount > 0 && !entry.ValidatorPaid {
		if err := e.ledger.TransferFromBucketLabeled(bucket, validatorID, validatorAmount, "escrow-release-net:validator", false); err != nil {
			return fmt.Errorf("escrow: release-net validator for task %s: %w", taskID, err)
		}
		e.mu.Lock()
		entry.ValidatorPaid = true
		e.mu.Unlock()
		e.persist(entry)
	}

	// Distribute treasury share from the remaining escrow balance.
	if treasuryAmount > 0 && !entry.TreasuryPaid {
		if err := e.ledger.TransferFromBucketLabeled(bucket, treasuryID, treasuryAmount, "escrow-release-net:treasury", false); err != nil {
			return fmt.Errorf("escrow: release-net treasury for task %s: %w", taskID, err)
		}
		e.mu.Lock()
		entry.TreasuryPaid = true
		e.mu.Unlock()
		e.persist(entry)
	}

	// All three disbursements complete — remove the entry.
	e.mu.Lock()
	delete(e.entries, taskID)
	e.mu.Unlock()
	if e.store != nil {
		if err := e.store.DeleteEscrow(taskID); err != nil {
			slog.Error("escrow: failed to delete completed entry", "task_id", taskID, "err", err)
		}
	}
	return nil
}

// ReleaseSettlement distributes the escrowed budget across the full v4.1
// economic model's recipient set: worker, N validators (Q-weighted),
// gen-ledger recipients, poster refund (dispute path), and treasury.
//
// Each individual transfer is guarded by a per-recipient paid flag persisted
// to BadgerDB after each successful transfer. On retry after a crash,
// already-paid recipients are skipped. This provides per-transfer idempotency
// equivalent to a single BadgerDB transaction, satisfying C-11.
//
// Integer arithmetic remainders from the share calculations are routed to
// treasury so the sum across all recipients equals the budget exactly.
// Cross-node conservation is verified by §10 success criterion 6.
//
// Returns ErrEscrowNotFound if no escrow exists for taskID.
func (e *Escrow) ReleaseSettlement(
	taskID string,
	worker crypto.AgentID, workerAmount uint64,
	posterRefund crypto.AgentID, posterRefundAmount uint64,
	validators map[crypto.AgentID]uint64,
	genRecipients map[crypto.AgentID]uint64,
	treasury crypto.AgentID, treasuryAmount uint64,
) error {
	e.mu.Lock()
	entry, ok := e.entries[taskID]
	e.mu.Unlock()
	if !ok {
		return fmt.Errorf("%w: task %s", ErrEscrowNotFound, taskID)
	}

	bucket := bucketID(taskID)

	// 1. Worker payout.
	if workerAmount > 0 && !entry.WorkerPaid {
		if err := e.ledger.TransferFromBucketLabeled(bucket, worker, workerAmount, "escrow-release:worker", false); err != nil {
			return fmt.Errorf("escrow: settlement worker for task %s: %w", taskID, err)
		}
		e.mu.Lock()
		entry.WorkerPaid = true
		e.mu.Unlock()
		e.persist(entry)
	}

	// 2. Poster refund (dispute path; zero on accept/reject).
	if posterRefundAmount > 0 && !entry.PosterRefundPaid {
		if err := e.ledger.TransferFromBucketLabeled(bucket, posterRefund, posterRefundAmount, "escrow-release:poster-refund", false); err != nil {
			return fmt.Errorf("escrow: settlement poster refund for task %s: %w", taskID, err)
		}
		e.mu.Lock()
		entry.PosterRefundPaid = true
		e.mu.Unlock()
		e.persist(entry)
	}

	// 3. Per-validator payouts (Q-weighted).
	for vid, amount := range validators {
		if amount == 0 {
			continue
		}
		e.mu.Lock()
		if entry.ValidatorsPaid == nil {
			entry.ValidatorsPaid = make(map[string]bool)
		}
		alreadyPaid := entry.ValidatorsPaid[string(vid)]
		e.mu.Unlock()
		if alreadyPaid {
			continue
		}
		if err := e.ledger.TransferFromBucketLabeled(bucket, vid, amount, "escrow-release:validator-distribution", false); err != nil {
			return fmt.Errorf("escrow: settlement validator %s for task %s: %w", vid, taskID, err)
		}
		e.mu.Lock()
		entry.ValidatorsPaid[string(vid)] = true
		e.mu.Unlock()
		e.persist(entry)
	}

	// 4. Gen-ledger royalty payouts.
	for rid, amount := range genRecipients {
		if amount == 0 {
			continue
		}
		e.mu.Lock()
		if entry.GenLedgerPaid == nil {
			entry.GenLedgerPaid = make(map[string]bool)
		}
		alreadyPaid := entry.GenLedgerPaid[string(rid)]
		e.mu.Unlock()
		if alreadyPaid {
			continue
		}
		if err := e.ledger.TransferFromBucketLabeled(bucket, rid, amount, "escrow-release:gen-ledger-royalty", false); err != nil {
			return fmt.Errorf("escrow: settlement gen-ledger %s for task %s: %w", rid, taskID, err)
		}
		e.mu.Lock()
		entry.GenLedgerPaid[string(rid)] = true
		e.mu.Unlock()
		e.persist(entry)
	}

	// 5. Treasury.
	if treasuryAmount > 0 && !entry.TreasuryPaid {
		if err := e.ledger.TransferFromBucketLabeled(bucket, treasury, treasuryAmount, "escrow-release:treasury-fee", false); err != nil {
			return fmt.Errorf("escrow: settlement treasury for task %s: %w", taskID, err)
		}
		e.mu.Lock()
		entry.TreasuryPaid = true
		e.mu.Unlock()
		e.persist(entry)
	}

	// All disbursements complete — remove the entry.
	e.mu.Lock()
	delete(e.entries, taskID)
	e.mu.Unlock()
	if e.store != nil {
		if err := e.store.DeleteEscrow(taskID); err != nil {
			slog.Error("escrow: failed to delete settled entry", "task_id", taskID, "err", err)
		}
	}
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

	if err := e.ledger.TransferFromBucketLabeled(bucketID(taskID), entry.PosterID, entry.Amount, "escrow-refund:poster-cancel", false); err != nil {
		return fmt.Errorf("escrow: refund for task %s: %w", taskID, err)
	}

	e.mu.Lock()
	delete(e.entries, taskID)
	e.mu.Unlock()
	if e.store != nil {
		if err := e.store.DeleteEscrow(taskID); err != nil {
			slog.Error("escrow: failed to delete refunded entry", "task_id", taskID, "err", err)
		}
	}
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
