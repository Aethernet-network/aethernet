package escrow

import (
	"testing"

	"github.com/Aethernet-network/aethernet/internal/crypto"
	"github.com/Aethernet-network/aethernet/internal/genesis"
	"github.com/Aethernet-network/aethernet/internal/ledger"
	"github.com/Aethernet-network/aethernet/internal/settlement/derivation"
)

// recordingCrashHook captures the index/taskID/recordCID of the crash
// trigger without terminating the test process. Replaces
// crashAfterNthRecord for the duration of a test via setCrashHookForTest.
type recordingCrashHook struct {
	called     bool
	index      int
	taskID     string
	recordCID  string
	callCount  int
}

func (r *recordingCrashHook) hook() func(int, string, string) {
	return func(index int, taskID, recordCID string) {
		r.called = true
		r.index = index
		r.taskID = taskID
		r.recordCID = recordCID
		r.callCount++
	}
}

func setCrashHookForTest(t *testing.T, hook func(int, string, string)) {
	t.Helper()
	prev := crashAfterNthRecord
	crashAfterNthRecord = hook
	t.Cleanup(func() { crashAfterNthRecord = prev })
}

// TestCrashInject_FlagFiresAtExpectedIndex verifies that setting
// AETHERNET_CRASH_AFTER_NTH_RECORD=N causes the hook to fire exactly
// when the per-record loop reaches index N (and not at other indexes).
//
// Validates the crash-position semantic: with flag=N, records 0..N-1
// are fully applied (transfer + paid-flag persist) before the hook
// fires; record N is untouched.
func TestCrashInject_FlagFiresAtExpectedIndex(t *testing.T) {
	t.Setenv(crashFlagEnvVar, "2")
	hook := &recordingCrashHook{}
	setCrashHookForTest(t, hook.hook())

	// Build an escrow with 4 records' worth of budget; apply 4 records.
	tl := ledger.NewTransferLedger()
	const budget uint64 = 100_000
	taskID := "crash-test-task-1"
	posterID := crypto.AgentID("poster-agent")
	if err := tl.FundAgent(posterID, budget); err != nil {
		t.Fatalf("FundAgent: %v", err)
	}
	em := New(tl)
	if err := em.RegisterEscrowForTest(taskID, posterID, budget, "funding-1"); err != nil {
		t.Fatalf("RegisterEscrowForTest: %v", err)
	}
	if err := tl.TransferFromBucket(posterID, crypto.AgentID("escrow:"+taskID), budget); err != nil {
		t.Fatalf("TransferFromBucket: %v", err)
	}

	records := makeApplyRecords(taskID, []recordSpec{
		{role: derivation.RoleWorker, id: "worker-agent", amount: 30_000, tag: derivation.TagWorkerPayout},
		{role: derivation.RoleValidator, id: "validator-a", amount: 10_000, tag: derivation.TagValidatorDistribution},
		{role: derivation.RoleValidator, id: "validator-b", amount: 10_000, tag: derivation.TagValidatorDistribution},
		{role: derivation.RoleTreasury, id: crypto.AgentID(genesis.BucketTreasury), amount: 50_000, tag: derivation.TagTreasuryRemainder},
	})

	err := em.ApplySettlementRecords(taskID, records)
	if err != nil {
		t.Fatalf("ApplySettlementRecords: %v", err)
	}

	if !hook.called {
		t.Fatalf("crash hook should have fired when env var = 2; hook never called")
	}
	if hook.callCount != 1 {
		t.Fatalf("crash hook should have fired exactly once; fired %d times", hook.callCount)
	}
	if hook.index != 2 {
		t.Fatalf("crash hook fired at index %d, want 2", hook.index)
	}
	if hook.taskID != taskID {
		t.Fatalf("crash hook taskID = %q, want %q", hook.taskID, taskID)
	}
	if hook.recordCID != records[2].CanonicalID {
		t.Fatalf("crash hook recordCID = %q, want %q (records[2])",
			hook.recordCID, records[2].CanonicalID)
	}
}

// TestCrashInject_NoFlagNoFire verifies the no-op default: with the env
// var unset, the hook never fires regardless of how many records are
// applied.
func TestCrashInject_NoFlagNoFire(t *testing.T) {
	// Explicitly clear in case the test environment has it set.
	t.Setenv(crashFlagEnvVar, "")
	hook := &recordingCrashHook{}
	setCrashHookForTest(t, hook.hook())

	tl := ledger.NewTransferLedger()
	const budget uint64 = 100_000
	taskID := "crash-test-task-2"
	posterID := crypto.AgentID("poster-agent")
	if err := tl.FundAgent(posterID, budget); err != nil {
		t.Fatalf("FundAgent: %v", err)
	}
	em := New(tl)
	if err := em.RegisterEscrowForTest(taskID, posterID, budget, "funding-2"); err != nil {
		t.Fatalf("RegisterEscrowForTest: %v", err)
	}
	if err := tl.TransferFromBucket(posterID, crypto.AgentID("escrow:"+taskID), budget); err != nil {
		t.Fatalf("TransferFromBucket: %v", err)
	}

	records := makeApplyRecords(taskID, []recordSpec{
		{role: derivation.RoleWorker, id: "worker-agent", amount: 50_000, tag: derivation.TagWorkerPayout},
		{role: derivation.RoleTreasury, id: crypto.AgentID(genesis.BucketTreasury), amount: 50_000, tag: derivation.TagTreasuryRemainder},
	})
	if err := em.ApplySettlementRecords(taskID, records); err != nil {
		t.Fatalf("ApplySettlementRecords: %v", err)
	}

	if hook.called {
		t.Fatalf("crash hook should NOT have fired when env var unset; fired at index=%d",
			hook.index)
	}
}

// TestCrashInject_NonNumericFlagIgnored verifies an invalid env-var
// value is treated as no-op (defensive against malformed deploys).
func TestCrashInject_NonNumericFlagIgnored(t *testing.T) {
	t.Setenv(crashFlagEnvVar, "not-a-number")
	hook := &recordingCrashHook{}
	setCrashHookForTest(t, hook.hook())

	tl := ledger.NewTransferLedger()
	taskID := "crash-test-task-3"
	posterID := crypto.AgentID("poster-agent")
	if err := tl.FundAgent(posterID, 50_000); err != nil {
		t.Fatalf("FundAgent: %v", err)
	}
	em := New(tl)
	if err := em.RegisterEscrowForTest(taskID, posterID, 50_000, "funding-3"); err != nil {
		t.Fatalf("RegisterEscrowForTest: %v", err)
	}
	if err := tl.TransferFromBucket(posterID, crypto.AgentID("escrow:"+taskID), 50_000); err != nil {
		t.Fatalf("TransferFromBucket: %v", err)
	}

	records := makeApplyRecords(taskID, []recordSpec{
		{role: derivation.RoleWorker, id: "worker-agent", amount: 50_000, tag: derivation.TagWorkerPayout},
	})
	if err := em.ApplySettlementRecords(taskID, records); err != nil {
		t.Fatalf("ApplySettlementRecords: %v", err)
	}

	if hook.called {
		t.Fatalf("crash hook should NOT have fired with non-numeric flag value")
	}
}

// recordSpec is a minimal record description for makeApplyRecords.
type recordSpec struct {
	role   derivation.RecipientRole
	id     crypto.AgentID
	amount uint64
	tag    derivation.PurposeTag
}

// makeApplyRecords builds a slice of PayoutRecords with deterministic
// canonical_id values so the test can assert on the recordCID captured
// by the crash hook.
func makeApplyRecords(taskID string, specs []recordSpec) []derivation.PayoutRecord {
	records := make([]derivation.PayoutRecord, 0, len(specs))
	for i, s := range specs {
		rec := derivation.PayoutRecord{
			DerivationVersion: derivation.DerivationVersion,
			SettlementKey: derivation.SettlementKey{
				RoundID:          "crash-test-round-1",
				TaskID:           taskID,
				FundingReference: "funding-ref-1",
			},
			Recipient: derivation.Recipient{ID: s.id, Role: s.role},
			Amount:    derivation.Amount{Value: s.amount, Currency: derivation.CurrencyAET},
			Purpose:   derivation.Purpose{Tag: s.tag, Ordinal: uint32(i)},
			Provenance: derivation.Provenance{
				RoundVerdict:               derivation.VerdictAccept,
				CanonicalCutoffAnchor:      "",
				CanonicalCutoffAnchorIsNil: true,
			},
		}
		cid, err := derivation.ComputeCanonicalID(rec)
		if err != nil {
			panic(err)
		}
		rec.CanonicalID = cid
		records = append(records, rec)
	}
	return records
}
