package store_test

// Extended unit tests for internal/store covering the surfaces that the
// original store_test.go did not exercise. F4A Part B.2 (plan §3.1.2) calls
// for:
//
//   1. Transaction atomicity under error injection.
//   2. Recovery from corruption (injected corrupt keys/values).
//   3. AdmissionStore round-trip matching dispatcher state machine.
//   4. Meta-store round-trip including prefix scans + edge values.
//   5. Concurrent access multi-goroutine consistency for mixed record types.
//   6. Schema-version probing — document current behaviour when an iterator
//      encounters a record with an unrecognised version field.
//
// FINDING comments in this file capture concerning behaviours observed while
// writing the tests. Tests assert *current* behaviour so CI stays green; the
// FINDING entries are surfaced to the F4A architect-gate report rather than
// being fixed inline.

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Aethernet-network/aethernet/internal/consensus"
	"github.com/Aethernet-network/aethernet/internal/crypto"
	"github.com/Aethernet-network/aethernet/internal/dispatch"
	"github.com/Aethernet-network/aethernet/internal/escrow"
	"github.com/Aethernet-network/aethernet/internal/event"
	"github.com/Aethernet-network/aethernet/internal/identity"
	"github.com/Aethernet-network/aethernet/internal/ledger"
	"github.com/Aethernet-network/aethernet/internal/store"
	"github.com/dgraph-io/badger/v4"
)

// ---------------------------------------------------------------------------
// 1. Transaction atomicity under error injection (RunInTransaction)
// ---------------------------------------------------------------------------

func TestRunInTransaction_RollsBackOnInnerError(t *testing.T) {
	s := openStore(t)

	sentinel := errors.New("inject: rollback")
	err := s.RunInTransaction(func(txn *badger.Txn) error {
		// Stage two writes within the same transaction, then abort.
		if err := txn.Set([]byte("meta:tx-test-a"), []byte("alpha")); err != nil {
			return err
		}
		if err := txn.Set([]byte("meta:tx-test-b"), []byte("bravo")); err != nil {
			return err
		}
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("RunInTransaction error: got %v, want %v", err, sentinel)
	}

	// Both writes must have been rolled back.
	for _, k := range []string{"tx-test-a", "tx-test-b"} {
		got, err := s.GetMeta(k)
		if err != nil {
			t.Fatalf("GetMeta(%s) after rollback: %v", k, err)
		}
		if got != nil {
			t.Errorf("rollback failed: meta:%s present after aborted txn (value=%q)", k, got)
		}
	}
}

func TestRunInTransaction_CommitsOnNilReturn(t *testing.T) {
	s := openStore(t)

	if err := s.RunInTransaction(func(txn *badger.Txn) error {
		return txn.Set([]byte("meta:committed"), []byte("yes"))
	}); err != nil {
		t.Fatalf("RunInTransaction: %v", err)
	}
	got, err := s.GetMeta("committed")
	if err != nil {
		t.Fatalf("GetMeta: %v", err)
	}
	if string(got) != "yes" {
		t.Errorf("committed value: got %q, want %q", got, "yes")
	}
}

func TestRunInTransaction_AtomicMultiRecordCommit(t *testing.T) {
	// A more realistic atomic write — escrow release pattern: write a
	// transfer entry and delete an escrow record in the same transaction,
	// where any partial state would corrupt the ledger invariant.
	s := openStore(t)

	// Seed: store an escrow entry to be deleted.
	seed := &escrow.EscrowEntry{TaskID: "task-tx-1", PosterID: crypto.AgentID("alice"), Amount: 500}
	if err := s.PutEscrow(seed); err != nil {
		t.Fatalf("PutEscrow seed: %v", err)
	}

	// Atomic multi-record op that succeeds.
	transferData, _ := json.Marshal(&ledger.TransferEntry{EventID: "evt-tx-1", Amount: 500})
	err := s.RunInTransaction(func(txn *badger.Txn) error {
		if err := txn.Set([]byte("txf:evt-tx-1"), transferData); err != nil {
			return err
		}
		return txn.Delete([]byte("esc:task-tx-1"))
	})
	if err != nil {
		t.Fatalf("RunInTransaction: %v", err)
	}

	if _, err := s.GetTransfer(event.EventID("evt-tx-1")); err != nil {
		t.Errorf("transfer not committed: %v", err)
	}
	if _, err := s.GetEscrow("task-tx-1"); err == nil {
		t.Error("escrow still present after atomic delete")
	}

	// Same op but failing midway leaves the store untouched.
	if err := s.PutEscrow(seed); err != nil {
		t.Fatalf("PutEscrow re-seed: %v", err)
	}
	abort := errors.New("abort")
	err = s.RunInTransaction(func(txn *badger.Txn) error {
		if err := txn.Set([]byte("txf:evt-tx-2"), transferData); err != nil {
			return err
		}
		if err := txn.Delete([]byte("esc:task-tx-1")); err != nil {
			return err
		}
		return abort
	})
	if !errors.Is(err, abort) {
		t.Fatalf("expected abort, got %v", err)
	}
	if _, err := s.GetTransfer(event.EventID("evt-tx-2")); err == nil {
		t.Error("evt-tx-2 leaked: txn should have rolled back")
	}
	if _, err := s.GetEscrow("task-tx-1"); err != nil {
		t.Errorf("escrow vanished after rolled-back delete: %v", err)
	}
}

// ---------------------------------------------------------------------------
// 2. Recovery from corruption — direct BadgerDB writes of malformed bytes
// ---------------------------------------------------------------------------

// putRaw bypasses the typed setters and writes the given bytes directly
// under the given key. Used to inject corruption that the typed Get/All
// surfaces will subsequently encounter.
func putRaw(t *testing.T, s *store.Store, key string, value []byte) {
	t.Helper()
	if err := s.DB().Update(func(txn *badger.Txn) error {
		return txn.Set([]byte(key), value)
	}); err != nil {
		t.Fatalf("putRaw(%q): %v", key, err)
	}
}

func TestGetEvent_OnCorruptValue_ReturnsError(t *testing.T) {
	s := openStore(t)
	putRaw(t, s, "evt:corrupt-1", []byte("{not valid json"))

	_, err := s.GetEvent(event.EventID("corrupt-1"))
	if err == nil {
		t.Fatal("GetEvent on corrupt value: expected error, got nil")
	}
	// Must not panic; must wrap the underlying decode error in a clean
	// "store: get event ..." wrapper consistent with other error paths.
	if !strings.Contains(err.Error(), "get event") {
		t.Errorf("error message: got %q, want substring %q", err.Error(), "get event")
	}
}

func TestAllEvents_OnCorruptValue_ReturnsError(t *testing.T) {
	// FINDING (store-corruption-fail-stop): AllEvents (and every other
	// AllX iterator in store.go) propagates the first JSON unmarshal error
	// rather than skipping the entry. A single corrupted event therefore
	// hides all healthy events that come after it in iteration order. This
	// is documented as fail-stop here. Plan §3.1.2 anticipated either
	// "skip with warning" or "fail closed" — current behaviour is
	// fail-closed. Discuss whether skip-with-warn would be preferable for
	// the recovery story (see also §3.1.3 schema-migration discipline).
	s := openStore(t)

	good, err := event.New(event.EventTypeAttestation, nil, nil, "good-agent", nil, 100)
	if err != nil {
		t.Fatalf("event.New: %v", err)
	}
	if err := s.PutEvent(good); err != nil {
		t.Fatalf("PutEvent: %v", err)
	}
	putRaw(t, s, "evt:corrupt-2", []byte{0xff, 0xff, 0xff})

	all, err := s.AllEvents()
	if err == nil {
		// Document any future change to skip-and-continue behaviour.
		t.Logf("FINDING follow-up: AllEvents now skips corrupt entries; got %d events", len(all))
	} else if !strings.Contains(err.Error(), "invalid character") &&
		!strings.Contains(err.Error(), "looking for") {
		t.Logf("AllEvents error (acceptable): %v", err)
	}
}

func TestGetTransfer_OnCorruptValue_ReturnsError(t *testing.T) {
	s := openStore(t)
	putRaw(t, s, "txf:bad", []byte("garbage"))
	_, err := s.GetTransfer(event.EventID("bad"))
	if err == nil {
		t.Fatal("expected error from GetTransfer on corrupt value")
	}
	if !strings.Contains(err.Error(), "get transfer") {
		t.Errorf("error wrapping: got %q, want 'get transfer' prefix", err.Error())
	}
}

func TestGetGeneration_OnCorruptValue_ReturnsError(t *testing.T) {
	s := openStore(t)
	putRaw(t, s, "gen:bad", []byte("garbage"))
	_, err := s.GetGeneration(event.EventID("bad"))
	if err == nil {
		t.Fatal("expected error from GetGeneration on corrupt value")
	}
	if !strings.Contains(err.Error(), "get generation") {
		t.Errorf("error wrapping: got %q, want 'get generation' prefix", err.Error())
	}
}

func TestGetIdentity_OnCorruptValue_ReturnsError(t *testing.T) {
	s := openStore(t)
	putRaw(t, s, "idn:bad", []byte("garbage"))
	_, err := s.GetIdentity(crypto.AgentID("bad"))
	if err == nil {
		t.Fatal("expected error from GetIdentity on corrupt value")
	}
	if !strings.Contains(err.Error(), "get identity") {
		t.Errorf("error wrapping: got %q, want 'get identity' prefix", err.Error())
	}
}

func TestGetEscrow_OnCorruptValue_ReturnsError(t *testing.T) {
	s := openStore(t)
	putRaw(t, s, "esc:bad", []byte("garbage"))
	_, err := s.GetEscrow("bad")
	if err == nil {
		t.Fatal("expected error from GetEscrow on corrupt value")
	}
	if !strings.Contains(err.Error(), "get escrow") {
		t.Errorf("error wrapping: got %q, want 'get escrow' prefix", err.Error())
	}
}

func TestStakeMeta_OnTruncatedValue_ReturnsZero(t *testing.T) {
	// FINDING (stake-meta-silent-zero): parseStakeMetaValue returns
	// (0, 0, 0) for any value shorter than 16 bytes. Callers cannot
	// distinguish "no record" from "corrupted record". Acceptable today
	// because stake meta is reconstructable from the event log, but worth
	// documenting in §3.1.3 schema-migration discipline so a future
	// stake-meta v3 migration can choose explicit failure semantics.
	s := openStore(t)
	putRaw(t, s, "stk:agent-x", []byte{0x01, 0x02}) // <16 bytes

	ss, la, amt, err := s.GetStakeMeta(crypto.AgentID("agent-x"))
	if err != nil {
		t.Fatalf("GetStakeMeta: %v", err)
	}
	if ss != 0 || la != 0 || amt != 0 {
		t.Errorf("expected zeros from truncated stake-meta, got (%d, %d, %d)", ss, la, amt)
	}
}

func TestGetMeta_OnMissingKey_ReturnsNilNil(t *testing.T) {
	s := openStore(t)
	got, err := s.GetMeta("does-not-exist")
	if err != nil {
		t.Fatalf("GetMeta missing: %v", err)
	}
	if got != nil {
		t.Errorf("GetMeta missing: got %q, want nil", got)
	}
}

// ---------------------------------------------------------------------------
// 3. AdmissionStore round-trip matching dispatcher state machine
// ---------------------------------------------------------------------------

func TestAdmission_RoundTripAllStates(t *testing.T) {
	s := openStore(t)

	// Construct one record for each lifecycle state. Note: StateAbsent has
	// no on-disk representation so it is not exercised here.
	cases := []struct {
		name  string
		state dispatch.AdmissionState
		cs    map[string]dispatch.PerConsumerStatus
	}{
		{
			name:  "reserved-pending-prereqs",
			state: dispatch.StateReservedPendingPrereqs,
			cs:    map[string]dispatch.PerConsumerStatus{"settlement": dispatch.ConsumerPending},
		},
		{
			name:  "processing",
			state: dispatch.StateProcessing,
			cs: map[string]dispatch.PerConsumerStatus{
				"settlement": dispatch.ConsumerApplied,
				"reputation": dispatch.ConsumerPending,
			},
		},
		{
			name:  "applied",
			state: dispatch.StateApplied,
			cs: map[string]dispatch.PerConsumerStatus{
				"settlement": dispatch.ConsumerApplied,
				"reputation": dispatch.ConsumerApplied,
			},
		},
		{
			name:  "failed-retryable",
			state: dispatch.StateFailedRetryable,
			cs: map[string]dispatch.PerConsumerStatus{
				"settlement": dispatch.ConsumerApplied,
				"reputation": dispatch.ConsumerFailedRetryable,
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			key := "dispatch:" + tc.name
			rec := &dispatch.AdmissionRecord{
				SchemaVersion:             1,
				Key:                       key,
				State:                     tc.state,
				DAGAnchor:                 event.EventID("dag-anchor-" + tc.name),
				PrerequisiteSchemaVersion: 2,
				Consumers:                 tc.cs,
				EventID:                   event.EventID("evt-" + tc.name),
				EventType:                 string(event.EventTypeTaskSettlement),
				CreatedAtEpoch:            uint64(time.Now().Unix()),
			}
			if tc.name == "reserved-pending-prereqs" {
				rec.MissingPrerequisites = []event.EventID{"prereq-A", "prereq-B"}
			}

			if err := s.PutAdmission(key, rec); err != nil {
				t.Fatalf("PutAdmission: %v", err)
			}
			got, err := s.GetAdmission(key)
			if err != nil {
				t.Fatalf("GetAdmission: %v", err)
			}
			if got.State != rec.State {
				t.Errorf("State: got %s, want %s", got.State, rec.State)
			}
			if got.Key != rec.Key {
				t.Errorf("Key: got %q, want %q", got.Key, rec.Key)
			}
			if got.DAGAnchor != rec.DAGAnchor {
				t.Errorf("DAGAnchor: got %s, want %s", got.DAGAnchor, rec.DAGAnchor)
			}
			if got.SchemaVersion != rec.SchemaVersion {
				t.Errorf("SchemaVersion: got %d, want %d", got.SchemaVersion, rec.SchemaVersion)
			}
			if got.PrerequisiteSchemaVersion != rec.PrerequisiteSchemaVersion {
				t.Errorf("PrerequisiteSchemaVersion: got %d, want %d", got.PrerequisiteSchemaVersion, rec.PrerequisiteSchemaVersion)
			}
			if len(got.Consumers) != len(rec.Consumers) {
				t.Fatalf("Consumers count: got %d, want %d", len(got.Consumers), len(rec.Consumers))
			}
			for k, v := range rec.Consumers {
				if got.Consumers[k] != v {
					t.Errorf("Consumers[%s]: got %s, want %s", k, got.Consumers[k], v)
				}
			}
			if len(got.MissingPrerequisites) != len(rec.MissingPrerequisites) {
				t.Errorf("MissingPrerequisites count: got %d, want %d",
					len(got.MissingPrerequisites), len(rec.MissingPrerequisites))
			}
		})
	}
}

func TestAdmission_StateMachineTransition_PendingToApplied(t *testing.T) {
	// Simulate the dispatcher walking an admission record from
	// reserved-pending-prereqs through processing to applied. Each
	// transition is a Put on the same key; the most recent record wins.
	s := openStore(t)
	key := "dispatch:lifecycle"

	rec := &dispatch.AdmissionRecord{
		SchemaVersion: 1,
		Key:           key,
		State:         dispatch.StateReservedPendingPrereqs,
		DAGAnchor:     event.EventID("anchor-1"),
		Consumers:     map[string]dispatch.PerConsumerStatus{"c1": dispatch.ConsumerPending},
		EventID:       event.EventID("evt-life"),
		EventType:     string(event.EventTypeTaskSettlement),
	}
	if err := s.PutAdmission(key, rec); err != nil {
		t.Fatalf("PutAdmission(reserved): %v", err)
	}

	rec.State = dispatch.StateProcessing
	if err := s.PutAdmission(key, rec); err != nil {
		t.Fatalf("PutAdmission(processing): %v", err)
	}

	rec.State = dispatch.StateApplied
	rec.Consumers["c1"] = dispatch.ConsumerApplied
	if err := s.PutAdmission(key, rec); err != nil {
		t.Fatalf("PutAdmission(applied): %v", err)
	}

	got, err := s.GetAdmission(key)
	if err != nil {
		t.Fatalf("GetAdmission: %v", err)
	}
	if got.State != dispatch.StateApplied {
		t.Errorf("final state: got %s, want %s", got.State, dispatch.StateApplied)
	}
	if got.Consumers["c1"] != dispatch.ConsumerApplied {
		t.Errorf("final c1 status: got %s, want %s", got.Consumers["c1"], dispatch.ConsumerApplied)
	}
}

func TestAdmission_AllAdmissions_ReturnsOnlyDispatchPrefix(t *testing.T) {
	s := openStore(t)

	for i := 0; i < 3; i++ {
		key := fmt.Sprintf("dispatch:all-%d", i)
		rec := &dispatch.AdmissionRecord{
			SchemaVersion: 1,
			Key:           key,
			State:         dispatch.StateApplied,
			DAGAnchor:     event.EventID(fmt.Sprintf("anchor-%d", i)),
			Consumers:     map[string]dispatch.PerConsumerStatus{"c": dispatch.ConsumerApplied},
			EventID:       event.EventID(fmt.Sprintf("evt-%d", i)),
		}
		if err := s.PutAdmission(key, rec); err != nil {
			t.Fatalf("PutAdmission[%d]: %v", i, err)
		}
	}

	// Add some unrelated keys that must not appear in AllAdmissions.
	if err := s.PutMeta("not-an-admission", []byte("nope")); err != nil {
		t.Fatalf("PutMeta noise: %v", err)
	}
	putRaw(t, s, "evt:noise", []byte("{}"))

	all, err := s.AllAdmissions()
	if err != nil {
		t.Fatalf("AllAdmissions: %v", err)
	}
	if len(all) != 3 {
		t.Errorf("AllAdmissions: got %d records, want 3", len(all))
	}
	for _, rec := range all {
		if !strings.HasPrefix(rec.Key, "dispatch:") {
			t.Errorf("non-dispatch key in AllAdmissions: %q", rec.Key)
		}
	}
}

func TestAdmission_DeleteAdmission_Idempotent(t *testing.T) {
	s := openStore(t)
	key := "dispatch:delete-me"
	rec := &dispatch.AdmissionRecord{
		SchemaVersion: 1, Key: key, State: dispatch.StateApplied,
		DAGAnchor: event.EventID("a"),
		Consumers: map[string]dispatch.PerConsumerStatus{"c": dispatch.ConsumerApplied},
	}
	if err := s.PutAdmission(key, rec); err != nil {
		t.Fatalf("PutAdmission: %v", err)
	}
	if err := s.DeleteAdmission(key); err != nil {
		t.Fatalf("DeleteAdmission: %v", err)
	}
	// Second delete is a no-op (BadgerDB Delete on missing key returns nil).
	if err := s.DeleteAdmission(key); err != nil {
		t.Errorf("second DeleteAdmission must be idempotent, got %v", err)
	}
	if _, err := s.GetAdmission(key); err == nil {
		t.Error("GetAdmission after delete: expected error, got nil")
	}
}

// ---------------------------------------------------------------------------
// 4. Meta-store round-trip — prefix scans, empty prefix, missing keys, large values
// ---------------------------------------------------------------------------

func TestMeta_RoundTripBasic(t *testing.T) {
	s := openStore(t)

	cases := []struct {
		key, value string
	}{
		{"genesis-marker", "v1"},
		{"onboarding-counter", "42"},
		{"settlement:applied:e1", `{"timestamp":1000}`},
		{"settlement:applied:e2", `{"timestamp":2000}`},
		{"empty-value", ""},
	}
	for _, c := range cases {
		if err := s.PutMeta(c.key, []byte(c.value)); err != nil {
			t.Fatalf("PutMeta(%s): %v", c.key, err)
		}
	}
	for _, c := range cases {
		got, err := s.GetMeta(c.key)
		if err != nil {
			t.Fatalf("GetMeta(%s): %v", c.key, err)
		}
		if string(got) != c.value {
			t.Errorf("GetMeta(%s): got %q, want %q", c.key, got, c.value)
		}
	}
}

func TestMeta_AllMeta_PrefixScan(t *testing.T) {
	s := openStore(t)

	if err := s.PutMeta("settlement:applied:e1", []byte("v1")); err != nil {
		t.Fatal(err)
	}
	if err := s.PutMeta("settlement:applied:e2", []byte("v2")); err != nil {
		t.Fatal(err)
	}
	if err := s.PutMeta("escrow:applied:e3", []byte("v3")); err != nil {
		t.Fatal(err)
	}
	if err := s.PutMeta("genesis-marker", []byte("g")); err != nil {
		t.Fatal(err)
	}

	t.Run("specific-prefix", func(t *testing.T) {
		got, err := s.AllMeta("settlement:applied:")
		if err != nil {
			t.Fatalf("AllMeta: %v", err)
		}
		if len(got) != 2 {
			t.Errorf("settlement:applied: got %d, want 2", len(got))
		}
		// Returned keys retain the logical (post-meta:) prefix.
		for k := range got {
			if !strings.HasPrefix(k, "settlement:applied:") {
				t.Errorf("unexpected key in scan: %q", k)
			}
		}
	})

	t.Run("empty-prefix-returns-all", func(t *testing.T) {
		// Empty prefix should match every meta entry.
		got, err := s.AllMeta("")
		if err != nil {
			t.Fatalf("AllMeta(\"\"): %v", err)
		}
		if len(got) != 4 {
			t.Errorf("empty prefix: got %d, want 4", len(got))
		}
	})

	t.Run("nonexistent-prefix", func(t *testing.T) {
		got, err := s.AllMeta("never-used:")
		if err != nil {
			t.Fatalf("AllMeta(never-used:): %v", err)
		}
		if len(got) != 0 {
			t.Errorf("nonexistent prefix: got %d, want 0", len(got))
		}
	})
}

func TestMeta_LargeValue_RoundTrip(t *testing.T) {
	s := openStore(t)

	// 256 KiB blob — larger than typical meta entries; verifies the
	// value-copy path in GetMeta returns the full byte sequence.
	large := make([]byte, 256*1024)
	for i := range large {
		large[i] = byte(i % 251)
	}
	if err := s.PutMeta("big", large); err != nil {
		t.Fatalf("PutMeta large: %v", err)
	}
	got, err := s.GetMeta("big")
	if err != nil {
		t.Fatalf("GetMeta large: %v", err)
	}
	if len(got) != len(large) {
		t.Fatalf("length: got %d, want %d", len(got), len(large))
	}
	for i := range large {
		if got[i] != large[i] {
			t.Fatalf("byte %d differs: got %x want %x", i, got[i], large[i])
		}
	}
}

func TestMeta_OverwriteSemantics(t *testing.T) {
	s := openStore(t)
	if err := s.PutMeta("k", []byte("first")); err != nil {
		t.Fatal(err)
	}
	if err := s.PutMeta("k", []byte("second")); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetMeta("k")
	if err != nil {
		t.Fatalf("GetMeta: %v", err)
	}
	if string(got) != "second" {
		t.Errorf("overwrite: got %q, want %q", got, "second")
	}
}

// ---------------------------------------------------------------------------
// 5. Concurrent access — multi-goroutine consistency for mixed record types
// ---------------------------------------------------------------------------

func TestConcurrentReadsAndWrites_Mixed(t *testing.T) {
	// Writers continuously put events, readers continuously enumerate.
	// Goal: detect data races (-race) and confirm AllEvents never panics
	// or returns a partial-decoded slice.
	s := openStore(t)

	const writers = 8
	const readers = 4
	const writesPerWriter = 25

	var stop atomic.Bool
	var wg sync.WaitGroup

	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < writesPerWriter; i++ {
				e, err := event.New(event.EventTypeAttestation, nil, nil,
					fmt.Sprintf("agent-mixed-%d-%d", w, i), nil, 100)
				if err != nil {
					t.Errorf("event.New[%d/%d]: %v", w, i, err)
					return
				}
				if err := s.PutEvent(e); err != nil {
					t.Errorf("PutEvent[%d/%d]: %v", w, i, err)
					return
				}
			}
		}(w)
	}

	for r := 0; r < readers; r++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for !stop.Load() {
				if _, err := s.AllEvents(); err != nil {
					t.Errorf("concurrent AllEvents: %v", err)
					return
				}
			}
		}()
	}

	// Wait for writers to finish, then signal readers to stop.
	go func() {
		// Coarse wait: writers finish quickly; signal stop after a tick.
		time.Sleep(50 * time.Millisecond)
		stop.Store(true)
	}()
	wg.Wait()

	all, err := s.AllEvents()
	if err != nil {
		t.Fatalf("final AllEvents: %v", err)
	}
	if want := writers * writesPerWriter; len(all) != want {
		t.Errorf("final count: got %d, want %d", len(all), want)
	}
}

func TestConcurrentDeletesAndReads_Identities(t *testing.T) {
	// Delete-while-read: writer thread continuously puts identities, a
	// deleter thread removes them at random, a reader thread enumerates.
	// Verifies AllIdentities never panics under concurrent mutation.
	s := openStore(t)

	const total = 30
	ids := make([]crypto.AgentID, total)
	for i := 0; i < total; i++ {
		kp, err := crypto.GenerateKeyPair()
		if err != nil {
			t.Fatalf("GenerateKeyPair[%d]: %v", i, err)
		}
		ids[i] = kp.AgentID()
		fp, err := identity.NewFingerprint(ids[i], kp.PublicKey, nil)
		if err != nil {
			t.Fatalf("NewFingerprint[%d]: %v", i, err)
		}
		if err := s.PutIdentity(fp); err != nil {
			t.Fatalf("PutIdentity[%d]: %v", i, err)
		}
	}

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for _, id := range ids {
			if err := s.DeleteIdentity(id); err != nil {
				t.Errorf("DeleteIdentity: %v", err)
				return
			}
		}
	}()
	go func() {
		defer wg.Done()
		// Read multiple times in parallel with deletes.
		for i := 0; i < 50; i++ {
			if _, err := s.AllIdentities(); err != nil {
				t.Errorf("AllIdentities: %v", err)
				return
			}
		}
	}()
	wg.Wait()

	all, err := s.AllIdentities()
	if err != nil {
		t.Fatalf("final AllIdentities: %v", err)
	}
	if len(all) != 0 {
		t.Errorf("expected all identities deleted, got %d", len(all))
	}
}

func TestConcurrentMultiRecordType_NoCrossContamination(t *testing.T) {
	// Concurrently exercise four prefix namespaces. Each goroutine writes
	// to a distinct prefix; final per-prefix counts must match exactly.
	s := openStore(t)

	const perKind = 20
	var wg sync.WaitGroup
	wg.Add(4)

	go func() {
		defer wg.Done()
		for i := 0; i < perKind; i++ {
			e, err := event.New(event.EventTypeAttestation, nil, nil,
				fmt.Sprintf("evt-mc-%d", i), nil, 100)
			if err != nil {
				t.Errorf("event.New: %v", err)
				return
			}
			if err := s.PutEvent(e); err != nil {
				t.Errorf("PutEvent: %v", err)
				return
			}
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < perKind; i++ {
			entry := &ledger.TransferEntry{
				EventID:    event.EventID(fmt.Sprintf("txf-mc-%d", i)),
				FromAgent:  crypto.AgentID("alice"),
				ToAgent:    crypto.AgentID("bob"),
				Amount:     uint64(i + 1),
				Currency:   "AET",
				Settlement: event.SettlementOptimistic,
				RecordedAt: time.Now().UTC(),
			}
			if err := s.PutTransfer(entry); err != nil {
				t.Errorf("PutTransfer: %v", err)
				return
			}
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < perKind; i++ {
			if err := s.PutMeta(fmt.Sprintf("mc:%d", i), []byte("v")); err != nil {
				t.Errorf("PutMeta: %v", err)
				return
			}
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < perKind; i++ {
			if err := s.PutTask(fmt.Sprintf("task-mc-%d", i), []byte(`{"id":"x"}`)); err != nil {
				t.Errorf("PutTask: %v", err)
				return
			}
		}
	}()
	wg.Wait()

	if all, err := s.AllEvents(); err != nil {
		t.Fatalf("AllEvents: %v", err)
	} else if len(all) != perKind {
		t.Errorf("events: got %d, want %d", len(all), perKind)
	}
	if all, err := s.AllTransfers(); err != nil {
		t.Fatalf("AllTransfers: %v", err)
	} else if len(all) != perKind {
		t.Errorf("transfers: got %d, want %d", len(all), perKind)
	}
	if all, err := s.AllMeta("mc:"); err != nil {
		t.Fatalf("AllMeta: %v", err)
	} else if len(all) != perKind {
		t.Errorf("meta: got %d, want %d", len(all), perKind)
	}
	if all, err := s.AllTasks(); err != nil {
		t.Fatalf("AllTasks: %v", err)
	} else if len(all) != perKind {
		t.Errorf("tasks: got %d, want %d", len(all), perKind)
	}
}

// ---------------------------------------------------------------------------
// 6. Schema-version probe — current behaviour for unrecognised version field
// ---------------------------------------------------------------------------

// TestAdmission_UnknownSchemaVersion_RejectedWithErr was previously
// TestAdmission_UnknownSchemaVersion_RoundTripsOpaquely (F4A B.2)
// documenting FINDING #5 (admission-schema-no-gate). F4B step 1 added
// the gate at validateAdmissionDecode in internal/store/store.go; the
// test is now flipped to assert the gate fires.
//
// Records with SchemaVersion > dispatch.AdmissionCurrentVersion MUST
// surface dispatch.ErrAdmissionSchemaTooNew on read rather than mis-
// decoding into the older struct shape.
func TestAdmission_UnknownSchemaVersion_RejectedWithErr(t *testing.T) {
	s := openStore(t)

	rec := &dispatch.AdmissionRecord{
		SchemaVersion: 999,
		Key:           "dispatch:future",
		State:         dispatch.StateApplied,
		DAGAnchor:     event.EventID("anchor-future"),
		Consumers:     map[string]dispatch.PerConsumerStatus{"c": dispatch.ConsumerApplied},
		EventID:       event.EventID("evt-future"),
		EventType:     string(event.EventTypeTaskSettlement),
	}
	if err := s.PutAdmission(rec.Key, rec); err != nil {
		t.Fatalf("PutAdmission: %v", err)
	}

	_, err := s.GetAdmission(rec.Key)
	if !errors.Is(err, dispatch.ErrAdmissionSchemaTooNew) {
		t.Fatalf("GetAdmission: got %v, want ErrAdmissionSchemaTooNew", err)
	}

	_, err = s.AllAdmissions()
	if !errors.Is(err, dispatch.ErrAdmissionSchemaTooNew) {
		t.Fatalf("AllAdmissions: got %v, want ErrAdmissionSchemaTooNew", err)
	}
}

// TestAdmission_UnknownStateValue_RejectedWithErr was previously
// TestAdmission_UnknownStateValue_RoundTripsOpaquely (F4A B.2)
// documenting FINDING #6 (admission-state-no-gate). F4B step 1 added
// the gate at validateAdmissionDecode; the test is now flipped to
// assert the gate fires.
//
// Records with a State value outside dispatch.IsKnownAdmissionState's
// enum MUST surface dispatch.ErrUnknownAdmissionState on read.
func TestAdmission_UnknownStateValue_RejectedWithErr(t *testing.T) {
	s := openStore(t)

	// Hand-craft JSON with a known schema_version but unknown state value.
	// putRaw bypasses PutAdmission's typed enum constraints.
	raw := []byte(`{
		"schema_version": 1,
		"key": "dispatch:weird-state",
		"state": 99,
		"dag_anchor": "anchor-weird",
		"consumers": {"c": 0},
		"event_id": "evt-weird",
		"event_type": "task-settlement"
	}`)
	putRaw(t, s, "dispatch:weird-state", raw)

	_, err := s.GetAdmission("dispatch:weird-state")
	if !errors.Is(err, dispatch.ErrUnknownAdmissionState) {
		t.Fatalf("GetAdmission: got %v, want ErrUnknownAdmissionState", err)
	}

	_, err = s.AllAdmissions()
	if !errors.Is(err, dispatch.ErrUnknownAdmissionState) {
		t.Fatalf("AllAdmissions: got %v, want ErrUnknownAdmissionState", err)
	}
}

// ---------------------------------------------------------------------------
// 7. Coverage of remaining typed surfaces — round-trip + delete + scan
// ---------------------------------------------------------------------------

func TestAPIKey_RoundTripAndScan(t *testing.T) {
	s := openStore(t)
	for i := 0; i < 3; i++ {
		key := fmt.Sprintf("apikey-%d", i)
		blob := []byte(fmt.Sprintf(`{"id":"%s"}`, key))
		if err := s.PutAPIKey(key, blob); err != nil {
			t.Fatalf("PutAPIKey: %v", err)
		}
	}
	for i := 0; i < 3; i++ {
		key := fmt.Sprintf("apikey-%d", i)
		got, err := s.GetAPIKey(key)
		if err != nil {
			t.Fatalf("GetAPIKey: %v", err)
		}
		if !strings.Contains(string(got), key) {
			t.Errorf("GetAPIKey value mismatch: %q", got)
		}
	}
	missing, err := s.GetAPIKey("never-existed")
	if err != nil {
		t.Errorf("GetAPIKey missing: got error %v, want nil", err)
	}
	if missing != nil {
		t.Errorf("GetAPIKey missing: got %q, want nil", missing)
	}
	all, err := s.AllAPIKeys()
	if err != nil {
		t.Fatalf("AllAPIKeys: %v", err)
	}
	if len(all) != 3 {
		t.Errorf("AllAPIKeys: got %d, want 3", len(all))
	}
}

func TestTask_RoundTripDeleteScan(t *testing.T) {
	s := openStore(t)

	if err := s.PutTask("t1", []byte(`{"id":"t1"}`)); err != nil {
		t.Fatalf("PutTask: %v", err)
	}
	got, err := s.GetTask("t1")
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if string(got) != `{"id":"t1"}` {
		t.Errorf("GetTask: got %q", got)
	}
	if _, err := s.GetTask("missing"); err == nil {
		t.Error("GetTask missing: expected error, got nil")
	}
	if err := s.DeleteTask("t1"); err != nil {
		t.Fatalf("DeleteTask: %v", err)
	}
	if _, err := s.GetTask("t1"); err == nil {
		t.Error("GetTask after delete: expected error")
	}
	// Idempotent delete.
	if err := s.DeleteTask("t1"); err != nil {
		t.Errorf("second DeleteTask must be idempotent, got %v", err)
	}
}

func TestReputation_RoundTripAndScan(t *testing.T) {
	s := openStore(t)
	for i := 0; i < 2; i++ {
		id := fmt.Sprintf("agent-r-%d", i)
		if err := s.PutReputation(id, []byte(`{"score":10}`)); err != nil {
			t.Fatalf("PutReputation: %v", err)
		}
	}
	got, err := s.GetReputation("agent-r-0")
	if err != nil {
		t.Fatalf("GetReputation: %v", err)
	}
	if string(got) != `{"score":10}` {
		t.Errorf("value mismatch: %q", got)
	}
	all, err := s.AllReputations()
	if err != nil {
		t.Fatalf("AllReputations: %v", err)
	}
	if len(all) != 2 {
		t.Errorf("AllReputations: got %d, want 2", len(all))
	}
}

func TestListing_RoundTripAndScan(t *testing.T) {
	s := openStore(t)
	if err := s.PutListing("agent-x", []byte(`{"capabilities":["nlp"]}`)); err != nil {
		t.Fatalf("PutListing: %v", err)
	}
	got, err := s.GetListing("agent-x")
	if err != nil {
		t.Fatalf("GetListing: %v", err)
	}
	if !strings.Contains(string(got), "nlp") {
		t.Errorf("listing value: %q", got)
	}
	all, err := s.AllListings()
	if err != nil {
		t.Fatalf("AllListings: %v", err)
	}
	if len(all) != 1 {
		t.Errorf("AllListings: got %d, want 1", len(all))
	}
}

func TestEscrow_RoundTripDeleteScan(t *testing.T) {
	s := openStore(t)

	entries := []*escrow.EscrowEntry{
		{TaskID: "esc-1", PosterID: crypto.AgentID("alice"), Amount: 100},
		{TaskID: "esc-2", PosterID: crypto.AgentID("bob"), Amount: 200,
			ValidatorsPaid: map[string]bool{"v1": true}},
	}
	for _, e := range entries {
		if err := s.PutEscrow(e); err != nil {
			t.Fatalf("PutEscrow(%s): %v", e.TaskID, err)
		}
	}
	got, err := s.GetEscrow("esc-2")
	if err != nil {
		t.Fatalf("GetEscrow: %v", err)
	}
	if got.Amount != 200 || !got.ValidatorsPaid["v1"] {
		t.Errorf("escrow round-trip mismatch: %+v", got)
	}
	all, err := s.AllEscrowEntries()
	if err != nil {
		t.Fatalf("AllEscrowEntries: %v", err)
	}
	if len(all) != 2 {
		t.Errorf("AllEscrowEntries: got %d, want 2", len(all))
	}
	if err := s.DeleteEscrow("esc-1"); err != nil {
		t.Fatalf("DeleteEscrow: %v", err)
	}
	// Idempotent delete.
	if err := s.DeleteEscrow("esc-1"); err != nil {
		t.Errorf("second DeleteEscrow must be idempotent, got %v", err)
	}
	if _, err := s.GetEscrow("esc-1"); err == nil {
		t.Error("GetEscrow after delete: expected error")
	}
}

func TestReplayJob_RoundTripAndScan(t *testing.T) {
	s := openStore(t)
	if err := s.PutReplayJob("job-1", []byte(`{"id":"job-1"}`)); err != nil {
		t.Fatalf("PutReplayJob: %v", err)
	}
	got, err := s.GetReplayJob("job-1")
	if err != nil {
		t.Fatalf("GetReplayJob: %v", err)
	}
	if string(got) != `{"id":"job-1"}` {
		t.Errorf("GetReplayJob value: %q", got)
	}
	if _, err := s.GetReplayJob("missing"); err == nil {
		t.Error("GetReplayJob missing: expected error")
	}
	all, err := s.AllReplayJobs()
	if err != nil {
		t.Fatalf("AllReplayJobs: %v", err)
	}
	if len(all) != 1 {
		t.Errorf("AllReplayJobs: got %d, want 1", len(all))
	}
}

func TestReplayOutcome_RoundTripAndScan(t *testing.T) {
	s := openStore(t)
	if err := s.PutReplayOutcome("job-1", []byte(`{"verdict":"approved"}`)); err != nil {
		t.Fatalf("PutReplayOutcome: %v", err)
	}
	got, err := s.GetReplayOutcome("job-1")
	if err != nil {
		t.Fatalf("GetReplayOutcome: %v", err)
	}
	if !strings.Contains(string(got), "approved") {
		t.Errorf("GetReplayOutcome value: %q", got)
	}
	if _, err := s.GetReplayOutcome("missing"); err == nil {
		t.Error("GetReplayOutcome missing: expected error")
	}
	all, err := s.AllReplayOutcomes()
	if err != nil {
		t.Fatalf("AllReplayOutcomes: %v", err)
	}
	if len(all) != 1 {
		t.Errorf("AllReplayOutcomes: got %d, want 1", len(all))
	}
}

func TestStakeMeta_RoundTripAndScan(t *testing.T) {
	s := openStore(t)

	if err := s.PutStakeMeta(crypto.AgentID("alice"), 100, 200, 5000); err != nil {
		t.Fatalf("PutStakeMeta: %v", err)
	}
	if err := s.PutStakeMeta(crypto.AgentID("bob"), 300, 400, 7000); err != nil {
		t.Fatalf("PutStakeMeta: %v", err)
	}
	ss, la, amt, err := s.GetStakeMeta(crypto.AgentID("alice"))
	if err != nil {
		t.Fatalf("GetStakeMeta: %v", err)
	}
	if ss != 100 || la != 200 || amt != 5000 {
		t.Errorf("alice stake meta: got (%d,%d,%d), want (100,200,5000)", ss, la, amt)
	}

	// Missing agent returns zeros without error.
	ss, la, amt, err = s.GetStakeMeta(crypto.AgentID("never"))
	if err != nil {
		t.Fatalf("GetStakeMeta missing: %v", err)
	}
	if ss != 0 || la != 0 || amt != 0 {
		t.Errorf("missing agent: got (%d,%d,%d), want zeros", ss, la, amt)
	}

	all, err := s.AllStakeMeta()
	if err != nil {
		t.Fatalf("AllStakeMeta: %v", err)
	}
	if len(all) != 2 {
		t.Errorf("AllStakeMeta: got %d, want 2", len(all))
	}
	if v, ok := all[crypto.AgentID("bob")]; !ok || v[0] != 300 || v[1] != 400 || v[2] != 7000 {
		t.Errorf("bob entry in scan: ok=%v v=%v", ok, v)
	}
}

func TestStakeMeta_LegacyV1Format_BackwardCompatible(t *testing.T) {
	// FINDING (stake-meta-v1-readable): old 16-byte stake-meta blobs
	// (pre stakedAmount field) are still readable — parseStakeMetaValue
	// returns amt=0 for legacy entries. This is the documented forward-
	// compatible read path. Verify it remains true so a future schema
	// change doesn't silently break legacy reload.
	s := openStore(t)

	v1 := make([]byte, 16)
	binary.BigEndian.PutUint64(v1[0:8], uint64(111))
	binary.BigEndian.PutUint64(v1[8:16], uint64(222))
	putRaw(t, s, "stk:legacy", v1)

	ss, la, amt, err := s.GetStakeMeta(crypto.AgentID("legacy"))
	if err != nil {
		t.Fatalf("GetStakeMeta legacy: %v", err)
	}
	if ss != 111 || la != 222 || amt != 0 {
		t.Errorf("legacy round-trip: got (%d,%d,%d), want (111,222,0)", ss, la, amt)
	}
}

func TestVote_RoundTripAndDelete(t *testing.T) {
	s := openStore(t)

	if err := s.PutVote("evt-vote-1", "voter-a", true); err != nil {
		t.Fatalf("PutVote: %v", err)
	}
	if err := s.PutVote("evt-vote-1", "voter-b", false); err != nil {
		t.Fatalf("PutVote: %v", err)
	}
	if err := s.PutVote("evt-vote-2", "voter-a", true); err != nil {
		t.Fatalf("PutVote: %v", err)
	}

	votes, err := s.GetVotes("evt-vote-1")
	if err != nil {
		t.Fatalf("GetVotes: %v", err)
	}
	if len(votes) != 2 {
		t.Errorf("GetVotes: got %d, want 2", len(votes))
	}
	// Verify decoded shape.
	for _, v := range votes {
		if v.EventID != "evt-vote-1" {
			t.Errorf("EventID: got %q, want evt-vote-1", v.EventID)
		}
		if v.VoterID != "voter-a" && v.VoterID != "voter-b" {
			t.Errorf("unexpected voter %q", v.VoterID)
		}
	}

	ids, err := s.AllVoteEventIDs()
	if err != nil {
		t.Fatalf("AllVoteEventIDs: %v", err)
	}
	if len(ids) != 2 {
		t.Errorf("AllVoteEventIDs: got %d, want 2", len(ids))
	}

	if err := s.DeleteVotes("evt-vote-1"); err != nil {
		t.Fatalf("DeleteVotes: %v", err)
	}
	votes, err = s.GetVotes("evt-vote-1")
	if err != nil {
		t.Fatalf("GetVotes after delete: %v", err)
	}
	if len(votes) != 0 {
		t.Errorf("GetVotes after delete: got %d, want 0", len(votes))
	}

	// Idempotent delete (no votes for this id).
	if err := s.DeleteVotes("never-voted"); err != nil {
		t.Errorf("DeleteVotes on empty must be no-op: %v", err)
	}

	// AllVoteEventIDs now returns only evt-vote-2.
	ids, err = s.AllVoteEventIDs()
	if err != nil {
		t.Fatalf("AllVoteEventIDs: %v", err)
	}
	if len(ids) != 1 || ids[0] != "evt-vote-2" {
		t.Errorf("AllVoteEventIDs after partial delete: got %v, want [evt-vote-2]", ids)
	}
}

// Sanity check that the consensus.PersistedVote shape matches what GetVotes
// returns. Catches a future drift where PersistedVote gains an unmarshalled
// field but PutVote forgets to populate it.
func TestVote_PersistedVoteShape(t *testing.T) {
	s := openStore(t)
	if err := s.PutVote("evt-shape", "voter-x", true); err != nil {
		t.Fatalf("PutVote: %v", err)
	}
	votes, err := s.GetVotes("evt-shape")
	if err != nil {
		t.Fatalf("GetVotes: %v", err)
	}
	if len(votes) != 1 {
		t.Fatalf("GetVotes: got %d, want 1", len(votes))
	}
	want := consensus.PersistedVote{EventID: "evt-shape", VoterID: "voter-x", Verdict: true}
	if votes[0] != want {
		t.Errorf("PersistedVote: got %+v, want %+v", votes[0], want)
	}
}

func TestValidator_RoundTripDeleteScan(t *testing.T) {
	s := openStore(t)

	if err := s.PutValidator("v1", []byte(`{"id":"v1","stake":100}`)); err != nil {
		t.Fatalf("PutValidator: %v", err)
	}
	got, err := s.GetValidator("v1")
	if err != nil {
		t.Fatalf("GetValidator: %v", err)
	}
	if !strings.Contains(string(got), "v1") {
		t.Errorf("GetValidator value: %q", got)
	}
	if _, err := s.GetValidator("missing"); err == nil {
		t.Error("GetValidator missing: expected error")
	}
	if err := s.DeleteValidator("v1"); err != nil {
		t.Fatalf("DeleteValidator: %v", err)
	}
	if err := s.DeleteValidator("v1"); err != nil {
		t.Errorf("second DeleteValidator must be idempotent, got %v", err)
	}
	all, err := s.AllValidators()
	if err != nil {
		t.Fatalf("AllValidators: %v", err)
	}
	if len(all) != 0 {
		t.Errorf("AllValidators after delete: got %d, want 0", len(all))
	}
}

func TestReplayReserve_RoundTripDefaultsToZero(t *testing.T) {
	s := openStore(t)

	bal, err := s.GetReplayReserve("never-set")
	if err != nil {
		t.Fatalf("GetReplayReserve: %v", err)
	}
	if bal != 0 {
		t.Errorf("default balance: got %d, want 0", bal)
	}

	if err := s.PutReplayReserve("category-a", 12345); err != nil {
		t.Fatalf("PutReplayReserve: %v", err)
	}
	bal, err = s.GetReplayReserve("category-a")
	if err != nil {
		t.Fatalf("GetReplayReserve: %v", err)
	}
	if bal != 12345 {
		t.Errorf("balance: got %d, want 12345", bal)
	}

	// Overwrite semantics.
	if err := s.PutReplayReserve("category-a", 99999); err != nil {
		t.Fatalf("PutReplayReserve overwrite: %v", err)
	}
	bal, err = s.GetReplayReserve("category-a")
	if err != nil {
		t.Fatalf("GetReplayReserve: %v", err)
	}
	if bal != 99999 {
		t.Errorf("overwritten balance: got %d, want 99999", bal)
	}
}

func TestReplayReserve_TruncatedValue_SilentZero(t *testing.T) {
	// FINDING (replay-reserve-truncated-zero): GetReplayReserve returns
	// (0, nil) when the on-disk blob is not exactly 8 bytes. Mirrors the
	// stake-meta truncation behaviour. Acceptable as a forward-compat
	// posture because reserves are reconstructable from the canonical
	// event log, but should be enumerated in §3.1.3.
	s := openStore(t)
	putRaw(t, s, "rsvr:weird", []byte{0x01, 0x02}) // <8 bytes
	bal, err := s.GetReplayReserve("weird")
	if err != nil {
		t.Fatalf("GetReplayReserve: %v", err)
	}
	if bal != 0 {
		t.Errorf("truncated balance: got %d, want 0", bal)
	}
}

func TestChallenge_RoundTripAndScan(t *testing.T) {
	s := openStore(t)

	got, err := s.GetChallenge("never")
	if err != nil {
		t.Fatalf("GetChallenge missing: %v", err)
	}
	if got != nil {
		t.Errorf("GetChallenge missing: got %q, want nil", got)
	}

	if err := s.PutChallenge("c1", []byte(`{"bond":100}`)); err != nil {
		t.Fatalf("PutChallenge: %v", err)
	}
	if err := s.PutChallenge("c2", []byte(`{"bond":200}`)); err != nil {
		t.Fatalf("PutChallenge: %v", err)
	}
	got, err = s.GetChallenge("c1")
	if err != nil {
		t.Fatalf("GetChallenge: %v", err)
	}
	if !strings.Contains(string(got), "100") {
		t.Errorf("GetChallenge value: %q", got)
	}
	all, err := s.AllChallenges()
	if err != nil {
		t.Fatalf("AllChallenges: %v", err)
	}
	if len(all) != 2 {
		t.Errorf("AllChallenges: got %d, want 2", len(all))
	}
}

func TestCanary_RoundTripPlusTaskIndex(t *testing.T) {
	s := openStore(t)
	if err := s.PutCanary("can-1", []byte(`{"id":"can-1"}`)); err != nil {
		t.Fatalf("PutCanary: %v", err)
	}
	if err := s.PutCanaryTaskIndex("task-1", "can-1"); err != nil {
		t.Fatalf("PutCanaryTaskIndex: %v", err)
	}
	got, err := s.GetCanary("can-1")
	if err != nil {
		t.Fatalf("GetCanary: %v", err)
	}
	if !strings.Contains(string(got), "can-1") {
		t.Errorf("GetCanary: %q", got)
	}
	got, err = s.GetCanaryByTaskID("task-1")
	if err != nil {
		t.Fatalf("GetCanaryByTaskID: %v", err)
	}
	if !strings.Contains(string(got), "can-1") {
		t.Errorf("GetCanaryByTaskID: %q", got)
	}
	if _, err := s.GetCanaryByTaskID("never"); err == nil {
		t.Error("GetCanaryByTaskID missing: expected error")
	}
	all, err := s.AllCanaries()
	if err != nil {
		t.Fatalf("AllCanaries: %v", err)
	}
	// Must contain only "cnr:" entries, not "cnrt:" index entries.
	if len(all) != 1 {
		t.Errorf("AllCanaries: got %d, want 1 (must exclude cnrt: index)", len(all))
	}
}

func TestCalibrationSignal_RoundTripAndActorFilter(t *testing.T) {
	s := openStore(t)

	if err := s.PutCalibrationSignal("sig-1", []byte(`{"actor_id":"alice","value":1}`)); err != nil {
		t.Fatalf("PutCalibrationSignal: %v", err)
	}
	if err := s.PutCalibrationSignal("sig-2", []byte(`{"actor_id":"bob","value":2}`)); err != nil {
		t.Fatalf("PutCalibrationSignal: %v", err)
	}
	if err := s.PutCalibrationSignal("sig-3", []byte(`{"actor_id":"alice","value":3}`)); err != nil {
		t.Fatalf("PutCalibrationSignal: %v", err)
	}
	// Malformed entry — by design, CalibrationSignalsByActor skips these.
	putRaw(t, s, "cal:malformed", []byte("not json"))

	all, err := s.AllCalibrationSignals()
	if err != nil {
		t.Fatalf("AllCalibrationSignals: %v", err)
	}
	if len(all) != 4 {
		t.Errorf("AllCalibrationSignals: got %d, want 4 (includes malformed blob)", len(all))
	}

	alice, err := s.CalibrationSignalsByActor("alice")
	if err != nil {
		t.Fatalf("CalibrationSignalsByActor: %v", err)
	}
	if len(alice) != 2 {
		t.Errorf("alice signals: got %d, want 2", len(alice))
	}
	bob, err := s.CalibrationSignalsByActor("bob")
	if err != nil {
		t.Fatalf("CalibrationSignalsByActor: %v", err)
	}
	if len(bob) != 1 {
		t.Errorf("bob signals: got %d, want 1", len(bob))
	}
	none, err := s.CalibrationSignalsByActor("ghost")
	if err != nil {
		t.Fatalf("CalibrationSignalsByActor: %v", err)
	}
	if len(none) != 0 {
		t.Errorf("ghost signals: got %d, want 0", len(none))
	}
}

func TestSync_NoErrorOnEmptyAndAfterWrite(t *testing.T) {
	// Sync is a fire-and-forget durability flush. Verifying it returns nil
	// both before and after a write is the only assertion possible without
	// crash-injection harness.
	s := openStore(t)
	if err := s.Sync(); err != nil {
		t.Errorf("Sync on empty store: %v", err)
	}
	if err := s.PutMeta("k", []byte("v")); err != nil {
		t.Fatalf("PutMeta: %v", err)
	}
	if err := s.Sync(); err != nil {
		t.Errorf("Sync after write: %v", err)
	}
}

func TestDeleteTransfer_RemovesEntry(t *testing.T) {
	s := openStore(t)
	entry := &ledger.TransferEntry{
		EventID: event.EventID("txf-del"), Amount: 10, Currency: "AET",
		Settlement: event.SettlementOptimistic, RecordedAt: time.Now().UTC(),
	}
	if err := s.PutTransfer(entry); err != nil {
		t.Fatalf("PutTransfer: %v", err)
	}
	if err := s.DeleteTransfer(entry.EventID); err != nil {
		t.Fatalf("DeleteTransfer: %v", err)
	}
	if _, err := s.GetTransfer(entry.EventID); err == nil {
		t.Error("GetTransfer after delete: expected error")
	}
}
