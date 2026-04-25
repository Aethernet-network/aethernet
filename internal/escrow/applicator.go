package escrow

import (
	"errors"
	"fmt"
	"sync"

	"github.com/Aethernet-network/aethernet/internal/event"
	"github.com/Aethernet-network/aethernet/internal/ledger"
	"github.com/Aethernet-network/aethernet/internal/settlement/derivation"
)

// recordLocks holds per-canonical_id sync.Mutex entries for intra-node
// defense-in-depth serialization within ApplySettlementRecords. Per F5
// 5B Plan v3 §3.3 sentence: this lock is INTRA-NODE defense-in-depth
// only. Cross-node idempotency and ordering are guaranteed by property
// D-1 (DeriveSettlement produces byte-identical records on every node)
// and by the LEDGER's canonical_id duplicate detection
// (TransferFromBucketLabeled returns ErrDuplicateEntry on duplicate
// EventID under l.mu.Lock() at internal/ledger/transfer.go:531). The
// lock here prevents wasted intra-node ledger calls when two settler
// goroutines race on the same record set; the LEDGER is what makes the
// cross-node correctness load-bearing.
//
// Removing this lock would not affect canonical correctness — the
// ledger's atomic dedup is sufficient. The lock exists to skip
// redundant work, not to enforce uniqueness.
//
// **Cross-layer framing reinforcement (multi-AI Grok review, 2026-04-25)**:
// recordLocks is defense-in-depth (intra-node only). Cross-node
// ordering and idempotency are guaranteed by DeriveSettlement
// producing byte-identical records (property D-1).
var recordLocks sync.Map // canonical_id (string) → *sync.Mutex

func recordLock(canonicalID string) *sync.Mutex {
	v, _ := recordLocks.LoadOrStore(canonicalID, &sync.Mutex{})
	return v.(*sync.Mutex)
}

// ApplySettlementRecords applies a derivation-function-produced slice
// of PayoutRecords atomically-with-respect-to-canonical_id. Replaces
// the pre-5B `ReleaseSettlement` 5-paid-flag check-then-transfer-then-set
// pattern (the concurrent-Apply race site).
//
// Per F5 5B Plan v3 §3.2-§3.5:
//
//   - Records are processed in canonical order (the slice is already
//     sorted by Purpose.Ordinal per the schema 4-step rule applied in
//     DeriveSettlement).
//
//   - Each record's transfer is keyed on record.CanonicalID as the
//     ledger EventID. Concurrent invocations producing the same
//     derivation produce records with the same CanonicalID values
//     (DeriveSettlement is pure per property D-1); the ledger's
//     duplicate-entry detection (ErrDuplicateEntry at
//     internal/ledger/transfer.go:531) makes the second call's transfer
//     attempt a no-op atomically with the first call's commit.
//
//   - The per-canonical_id sync.Mutex is INTRA-NODE defense-in-depth
//     only (architect-confirmed per recordLocks doc above). Cross-node
//     correctness rests on (a) D-1 producing identical records and (b)
//     the ledger's atomic dedup.
//
//   - Paid-flag projection (entry.WorkerPaid, entry.ValidatorsPaid[id],
//     etc.) is updated AS A PURE PROJECTION of records that have been
//     applied (Plan v3 §3.4 obligation b). Paid-flag READS are used
//     ONLY for skip-optimization (avoiding redundant ledger calls when
//     we already know the record was applied); they are NEVER a
//     correctness gate (Plan v3 §3.4 obligation c). Even if a flag is
//     unset due to crash mid-persist, calling
//     TransferFromBucketLabeled is safe — the ledger returns
//     ErrDuplicateEntry which we treat as benign no-op.
//
// Crash-position analysis (Plan v3 §3.3 table):
//
//   - Crash before ledger write: paid-flag says "not applied"; retry
//     succeeds; flag updated.
//   - Crash after ledger write, before paid-flag persist: paid-flag
//     says "not applied"; retry's TransferFromBucketLabeled returns
//     ErrDuplicateEntry; treated as benign no-op AND flag updated.
//   - Crash after both: paid-flag says "applied"; retry's
//     skip-optimization fast-path skips entirely.
//
// All three positions self-heal to byte-identical end-state with
// non-crashed nodes. The ledger's existing duplicate-detection IS the
// canonical idempotency primitive; atomic-persist coordination at this
// layer is unnecessary.
//
// Returns ErrEscrowNotFound if no escrow exists for taskID.
//
// On success, the escrow entry is deleted iff every record was applied
// (matches pre-5B convention: the entry's lifetime ends with the
// canonical settlement).
func (e *Escrow) ApplySettlementRecords(taskID string, records []derivation.PayoutRecord) error {
	if len(records) == 0 {
		return nil
	}

	e.mu.Lock()
	entry, ok := e.entries[taskID]
	e.mu.Unlock()
	if !ok {
		return fmt.Errorf("%w: task %s", ErrEscrowNotFound, taskID)
	}

	bucket := bucketID(taskID)

	for _, rec := range records {
		// Skip-optimization (fast path, never a correctness gate per
		// Plan v3 §3.4 obligation c). Paid-flag READ here decides ONLY
		// whether to skip a redundant ledger call we've already made;
		// the ledger's ErrDuplicateEntry would catch it anyway.
		e.mu.RLock()
		alreadyApplied := isRecordApplied(entry, rec)
		e.mu.RUnlock()
		if alreadyApplied {
			continue
		}

		// Per-canonical_id intra-node lock (defense-in-depth only;
		// canonical correctness is at the ledger layer).
		lock := recordLock(rec.CanonicalID)
		lock.Lock()

		// Re-check skip-optimization under lock (handles concurrent-
		// goroutine race where two settler goroutines both pass the
		// pre-lock check).
		e.mu.RLock()
		alreadyApplied = isRecordApplied(entry, rec)
		e.mu.RUnlock()
		if alreadyApplied {
			lock.Unlock()
			continue
		}

		eventID := event.EventID(rec.CanonicalID)
		memo := memoForTag(rec.Purpose.Tag)

		// Canonical correctness gate: the ledger's atomic
		// canonical_id duplicate detection. nil OR ErrDuplicateEntry
		// both mean "this record's transfer is in the ledger now."
		transferErr := e.ledger.TransferFromBucketLabeled(
			eventID,
			bucket,
			rec.Recipient.ID,
			rec.Amount.Value,
			memo,
			false,
		)
		switch {
		case transferErr == nil:
			// Transfer succeeded; update paid-flag projection.
		case errors.Is(transferErr, ledger.ErrDuplicateEntry):
			// Ledger already has this canonical_id (crash window
			// position 2: prior call wrote ledger but crashed before
			// paid-flag persist). Benign no-op; still update the
			// paid-flag projection so subsequent retries fast-path skip.
		default:
			// Real failure (e.g., insufficient bucket balance — should
			// not happen under correct derivation). Surface; do NOT
			// update paid-flag.
			lock.Unlock()
			return fmt.Errorf("escrow: apply record %s for task %s: %w", rec.CanonicalID, taskID, transferErr)
		}

		// Update paid-flag projection (Plan v3 §3.4 obligation b: pure
		// projection of records that have been applied).
		e.mu.Lock()
		updatePaidFlag(entry, rec)
		e.mu.Unlock()
		e.persist(entry)

		lock.Unlock()
	}

	// Delete the entry once all records are applied — matches pre-5B
	// convention. Records are derived per round; once they're all
	// applied the entry's purpose is fulfilled.
	if allRecordsApplied(entry, records) {
		e.mu.Lock()
		delete(e.entries, taskID)
		e.mu.Unlock()
		if e.store != nil {
			if err := e.store.DeleteEscrow(taskID); err != nil {
				// Non-fatal: the in-memory deletion succeeded; on next
				// restart LoadFromStore will rebuild without this entry.
				// Logged at the same level as the existing ReleaseSettlement
				// path (escrow.go:631).
				return fmt.Errorf("escrow: delete settled entry for task %s: %w", taskID, err)
			}
		}
	}
	return nil
}

// isRecordApplied returns true iff the paid-flag projection on entry
// already reflects this record. SKIP-OPTIMIZATION ONLY: a false return
// is NOT an authorization to apply (the ledger's canonical_id dedup is
// the correctness gate); a true return is a hint that the ledger call
// would be redundant and can be skipped.
//
// Per Plan v3 §3.4 obligation c: this function is READ-only on
// paid-flags; the result is used to short-circuit the per-record loop
// body, NEVER to gate whether application logic runs at all.
func isRecordApplied(entry *EscrowEntry, rec derivation.PayoutRecord) bool {
	switch rec.Recipient.Role {
	case derivation.RoleWorker:
		return entry.WorkerPaid
	case derivation.RolePosterRefund:
		return entry.PosterRefundPaid
	case derivation.RoleValidator:
		if entry.ValidatorsPaid == nil {
			return false
		}
		return entry.ValidatorsPaid[string(rec.Recipient.ID)]
	case derivation.RoleGenLedgerAncestor:
		if entry.GenLedgerPaid == nil {
			return false
		}
		return entry.GenLedgerPaid[string(rec.Recipient.ID)]
	case derivation.RoleTreasury:
		return entry.TreasuryPaid
	}
	return false
}

// updatePaidFlag writes the paid-flag projection for a record.
// Per Plan v3 §3.4 obligation b: paid-flag is a PURE PROJECTION of
// records that have been applied. The function is the WRITER of that
// projection; isRecordApplied is the READER (skip-optimization only).
//
// Callers MUST hold e.mu.Lock() when invoking this function.
func updatePaidFlag(entry *EscrowEntry, rec derivation.PayoutRecord) {
	switch rec.Recipient.Role {
	case derivation.RoleWorker:
		entry.WorkerPaid = true
	case derivation.RolePosterRefund:
		entry.PosterRefundPaid = true
	case derivation.RoleValidator:
		if entry.ValidatorsPaid == nil {
			entry.ValidatorsPaid = make(map[string]bool)
		}
		entry.ValidatorsPaid[string(rec.Recipient.ID)] = true
	case derivation.RoleGenLedgerAncestor:
		if entry.GenLedgerPaid == nil {
			entry.GenLedgerPaid = make(map[string]bool)
		}
		entry.GenLedgerPaid[string(rec.Recipient.ID)] = true
	case derivation.RoleTreasury:
		entry.TreasuryPaid = true
	}
}

// allRecordsApplied returns true iff every record's paid-flag is set.
// Drives the entry-deletion fast path at the end of
// ApplySettlementRecords. Like isRecordApplied, this function is
// READ-only on paid-flags and is used for skip / cleanup decisions, not
// correctness gating.
func allRecordsApplied(entry *EscrowEntry, records []derivation.PayoutRecord) bool {
	for _, rec := range records {
		if !isRecordApplied(entry, rec) {
			return false
		}
	}
	return true
}

// memoForTag returns the canonical memo string for a record's purpose
// tag. Memos match the pre-5B ReleaseSettlement values exactly so the
// ledger's per-entry memo field is byte-identical to the pre-5B output
// for the same canonical inputs (preserves the existing audit log
// shape).
func memoForTag(tag derivation.PurposeTag) string {
	switch tag {
	case derivation.TagWorkerPayout:
		return "escrow-release:worker"
	case derivation.TagPosterRefund:
		return "escrow-release:poster-refund"
	case derivation.TagValidatorDistribution:
		return "escrow-release:validator-distribution"
	case derivation.TagGenLedgerRoyalty:
		return "escrow-release:gen-ledger-royalty"
	case derivation.TagTreasuryRemainder:
		return "escrow-release:treasury-fee"
	}
	// Defensive: tags outside the locked vocabulary are a schema-
	// contract violation upstream. Use a recognizable memo so the ledger
	// entry surfaces the issue at audit time.
	return "escrow-release:unknown-tag"
}

// Compile-time guard that ApplySettlementRecords stays in the package's
// public surface. If the method signature drifts, the compiler catches
// it here before downstream callers do.
var _ = (*Escrow)(nil).ApplySettlementRecords
