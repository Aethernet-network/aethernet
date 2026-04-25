package escrow

import (
	"testing"

	"github.com/Aethernet-network/aethernet/internal/crypto"
	"github.com/Aethernet-network/aethernet/internal/event"
	"github.com/Aethernet-network/aethernet/internal/ledger"
	"github.com/Aethernet-network/aethernet/internal/settlement/derivation"
)

// balanceOrZero returns the agent's balance via TransferLedger.Balance,
// failing the test on error. Used to keep test bodies terse.
func balanceOrZero(t *testing.T, tl *ledger.TransferLedger, agentID crypto.AgentID) uint64 {
	t.Helper()
	bal, err := tl.Balance(agentID)
	if err != nil {
		t.Fatalf("Balance(%s): %v", agentID, err)
	}
	return bal
}

// makeRecord returns a synthetic PayoutRecord with a unique CanonicalID
// derived from role + recipient. CanonicalID matters because it's the
// ledger EventID — distinct CanonicalIDs avoid spurious dedup.
func makeRecord(role derivation.RecipientRole, tag derivation.PurposeTag, recipient crypto.AgentID, amount uint64) derivation.PayoutRecord {
	return derivation.PayoutRecord{
		CanonicalID:       string(role) + ":" + string(recipient),
		DerivationVersion: derivation.DerivationVersion,
		SettlementKey: derivation.SettlementKey{
			RoundID:          "round-1",
			TaskID:           "task-1",
			FundingReference: "funding-1",
		},
		Recipient: derivation.Recipient{ID: recipient, Role: role},
		Amount:    derivation.Amount{Value: amount, Currency: derivation.CurrencyAET},
		Purpose:   derivation.Purpose{Tag: tag},
	}
}

// setupEscrowWithBudget initializes an escrow + transfer ledger with
// the given budget pre-funded into the per-task bucket. Returns the
// ready-to-use escrow + the bucket AgentID.
func setupEscrowWithBudget(t *testing.T, taskID string, budget uint64) (*Escrow, crypto.AgentID) {
	t.Helper()
	tl := ledger.NewTransferLedger()
	bucket := bucketID(taskID)
	if err := tl.FundAgent(bucket, budget); err != nil {
		t.Fatalf("FundAgent(bucket): %v", err)
	}

	e := New(tl)
	if err := e.RegisterEscrowForTest(taskID, "poster-agent", budget, "funding-event-id"); err != nil {
		t.Fatalf("RegisterEscrow: %v", err)
	}
	return e, bucket
}

// TestApplySettlementRecords_ConservesBudget verifies the canonical
// invariant: sum of record amounts == budget; nothing leaks.
func TestApplySettlementRecords_ConservesBudget(t *testing.T) {
	const budget uint64 = 100_000
	taskID := "task-1"
	e, _ := setupEscrowWithBudget(t, taskID, budget)

	records := []derivation.PayoutRecord{
		makeRecord(derivation.RoleWorker, derivation.TagWorkerPayout, "worker", 73_000),
		makeRecord(derivation.RoleValidator, derivation.TagValidatorDistribution, "v-a", 11_500),
		makeRecord(derivation.RoleValidator, derivation.TagValidatorDistribution, "v-b", 11_500),
		makeRecord(derivation.RoleTreasury, derivation.TagTreasuryRemainder, "treasury", 4_000),
	}

	if err := e.ApplySettlementRecords(taskID, records); err != nil {
		t.Fatalf("ApplySettlementRecords: %v", err)
	}

	// Conservation check: bucket fully drained.
	if balanceOrZero(t, e.ledger, bucketID(taskID)) != 0 {
		t.Fatalf("budget conservation violated: bucket has %d remaining", balanceOrZero(t, e.ledger, bucketID(taskID)))
	}
	// Recipients credited.
	if got := balanceOrZero(t, e.ledger, "worker"); got != 73_000 {
		t.Errorf("worker balance = %d, want 73000", got)
	}
	if got := balanceOrZero(t, e.ledger, "v-a"); got != 11_500 {
		t.Errorf("v-a balance = %d, want 11500", got)
	}
	if got := balanceOrZero(t, e.ledger, "treasury"); got != 4_000 {
		t.Errorf("treasury balance = %d, want 4000", got)
	}
}

// TestApplySettlementRecords_LedgerErrDuplicateEntryIsBenign verifies
// the load-bearing crash-position-2 case from Plan v3 §3.3: ledger
// already has the canonical_id (e.g., a prior call wrote the ledger
// but crashed before the paid-flag persist) → the applicator's
// TransferFromBucketLabeled call returns ErrDuplicateEntry → applicator
// treats as benign no-op AND updates the paid-flag. No double-pay; no
// error returned to caller.
//
// This is the architect's watch-point #3: crash atomicity via
// ledger-level idempotency. The ledger's atomic dedup IS the
// canonical-correctness mechanism; the applicator-layer per-canonical_id
// lock is intra-node defense-in-depth only.
//
// Setup note: the ledger's TransferFromBucketLabeled checks bucket
// balance BEFORE the dedup check (transfer.go:527 vs :531). To
// isolate the dedup path from the balance path, the bucket is funded
// to 2x budget (covers both the pre-written transfer and the
// applicator's retry), then verified at the end that the dedup-
// admitted record did NOT double-pay.
func TestApplySettlementRecords_LedgerErrDuplicateEntryIsBenign(t *testing.T) {
	const budget uint64 = 100_000
	taskID := "task-1"
	tl := ledger.NewTransferLedger()
	bucket := bucketID(taskID)
	if err := tl.FundAgent(bucket, budget*2); err != nil { // 2x budget per setup-note above
		t.Fatalf("FundAgent(bucket): %v", err)
	}
	e := New(tl)
	if err := e.RegisterEscrowForTest(taskID, "poster-agent", budget, "funding-event-id"); err != nil {
		t.Fatalf("RegisterEscrow: %v", err)
	}

	workerRecord := makeRecord(derivation.RoleWorker, derivation.TagWorkerPayout, "worker", 73_000)
	treasuryRecord := makeRecord(derivation.RoleTreasury, derivation.TagTreasuryRemainder, "treasury", 27_000)

	// Pre-write the worker transfer to the ledger DIRECTLY (simulates a
	// crash window where the ledger entry persisted but the
	// applicator's paid-flag did not). The applicator's call below
	// should observe ErrDuplicateEntry and treat as benign no-op.
	preWriteErr := tl.TransferFromBucketLabeled(
		event.EventID(workerRecord.CanonicalID),
		bucket,
		"worker",
		73_000,
		"escrow-release:worker",
		false,
	)
	if preWriteErr != nil {
		t.Fatalf("pre-write worker transfer: %v", preWriteErr)
	}
	if got := balanceOrZero(t, tl, "worker"); got != 73_000 {
		t.Fatalf("pre-write check: worker balance = %d, want 73000", got)
	}

	records := []derivation.PayoutRecord{workerRecord, treasuryRecord}
	if err := e.ApplySettlementRecords(taskID, records); err != nil {
		t.Fatalf("ApplySettlementRecords: %v", err)
	}

	// Worker balance unchanged (no double-pay) — the ErrDuplicateEntry
	// path was hit and treated as benign no-op.
	if got := balanceOrZero(t, tl, "worker"); got != 73_000 {
		t.Errorf("worker balance changed (double-pay leak): was 73000, now %d", got)
	}
	// Treasury credited.
	if got := balanceOrZero(t, tl, "treasury"); got != 27_000 {
		t.Errorf("treasury balance = %d, want 27000", got)
	}
}

// TestApplySettlementRecords_PaidFlagIsProjectionNotGate verifies the
// architect's watch-point #1: paid-flag READS skip the redundant ledger
// call but the LEDGER's atomic dedup is the correctness gate. We drive
// this by setting the paid-flag manually (simulating a crash window
// where the flag is wrongly true) and asserting the second call is a
// safe no-op (NOT a misapplication).
func TestApplySettlementRecords_PaidFlagIsProjectionNotGate(t *testing.T) {
	const budget uint64 = 100_000
	taskID := "task-1"
	e, _ := setupEscrowWithBudget(t, taskID, budget)

	records := []derivation.PayoutRecord{
		makeRecord(derivation.RoleWorker, derivation.TagWorkerPayout, "worker", 73_000),
	}

	// Apply once.
	if err := e.ApplySettlementRecords(taskID, records); err != nil {
		t.Fatalf("ApplySettlementRecords: %v", err)
	}
	balance := balanceOrZero(t, e.ledger, "worker")
	if balance != 73_000 {
		t.Fatalf("worker not paid: balance = %d, want 73000", balance)
	}

	// The entry was deleted (all records applied → cleanup). Re-register
	// + manually set WorkerPaid=true (simulates "paid-flag wrongly true"
	// crash recovery scenario).
	if err := e.RegisterEscrowForTest(taskID, "poster-agent", budget, "funding-event-id"); err != nil {
		t.Fatalf("re-RegisterEscrow: %v", err)
	}
	e.mu.Lock()
	e.entries[taskID].WorkerPaid = true
	e.mu.Unlock()

	// Apply again — paid-flag fast-path skip fires; ledger is NOT called.
	// Even if skipped, no double-pay occurs.
	if err := e.ApplySettlementRecords(taskID, records); err != nil {
		t.Fatalf("second ApplySettlementRecords: %v", err)
	}
	if got := balanceOrZero(t, e.ledger, "worker"); got != balance {
		t.Errorf("worker balance changed: was %d, now %d (skip-optimization leaked)", balance, got)
	}
}

// TestApplySettlementRecords_EmptyRecordsNoop verifies the empty-input
// edge case (no records to apply → nil return, no entry mutation).
func TestApplySettlementRecords_EmptyRecordsNoop(t *testing.T) {
	const budget uint64 = 100_000
	taskID := "task-1"
	e, _ := setupEscrowWithBudget(t, taskID, budget)

	if err := e.ApplySettlementRecords(taskID, nil); err != nil {
		t.Fatalf("ApplySettlementRecords(nil): %v", err)
	}
	if got := balanceOrZero(t, e.ledger, bucketID(taskID)); got != budget {
		t.Errorf("bucket balance changed on empty-records call: was %d, now %d", budget, got)
	}
}

// TestApplySettlementRecords_ErrEscrowNotFoundOnUnknownTask verifies
// the precondition: missing entry returns the sentinel.
func TestApplySettlementRecords_ErrEscrowNotFoundOnUnknownTask(t *testing.T) {
	tl := ledger.NewTransferLedger()
	e := New(tl)

	records := []derivation.PayoutRecord{makeRecord(derivation.RoleWorker, derivation.TagWorkerPayout, "worker", 1)}
	err := e.ApplySettlementRecords("nonexistent-task", records)
	if err == nil {
		t.Fatal("expected ErrEscrowNotFound, got nil")
	}
}
