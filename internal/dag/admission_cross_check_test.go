package dag_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/Aethernet-network/aethernet/internal/dag"
	"github.com/Aethernet-network/aethernet/internal/event"
)

// TestAdmissionCrossCheck_AcceptsValidEvent verifies an admission cross-check
// returning nil admits the event normally.
func TestAdmissionCrossCheck_AcceptsValidEvent(t *testing.T) {
	d := dag.New()

	called := false
	if err := d.RegisterAdmissionCrossCheck(event.EventTypeTransfer, func(_ *event.Event, _ dag.WhileLockedReader) error {
		called = true
		return nil
	}); err != nil {
		t.Fatalf("RegisterAdmissionCrossCheck: %v", err)
	}

	g := makeGenesis(t, "g")
	mustAdd(t, d, g)

	if !called {
		t.Fatal("validator was not called")
	}
	if _, err := d.Get(g.ID); err != nil {
		t.Fatalf("event should be in DAG after accepting cross-check: %v", err)
	}
}

// TestAdmissionCrossCheck_RejectsInvalidEvent verifies a returning-error
// cross-check causes Add to return ErrCrossCheckRejected and the event is
// not stored.
func TestAdmissionCrossCheck_RejectsInvalidEvent(t *testing.T) {
	d := dag.New()

	rejection := errors.New("payload claim contradicts canonical state")
	if err := d.RegisterAdmissionCrossCheck(event.EventTypeTransfer, func(_ *event.Event, _ dag.WhileLockedReader) error {
		return rejection
	}); err != nil {
		t.Fatalf("RegisterAdmissionCrossCheck: %v", err)
	}

	g := makeGenesis(t, "g")
	err := d.Add(g)
	if !errors.Is(err, dag.ErrCrossCheckRejected) {
		t.Fatalf("expected ErrCrossCheckRejected, got %v", err)
	}
	if !strings.Contains(err.Error(), rejection.Error()) {
		t.Fatalf("expected wrapped rejection reason in error, got %v", err)
	}

	// Event must NOT be in DAG after rejection.
	if _, getErr := d.Get(g.ID); !errors.Is(getErr, dag.ErrEventNotFound) {
		t.Fatalf("rejected event should not be in DAG; Get returned %v", getErr)
	}
}

// TestAdmissionCrossCheck_NoValidatorAdmitsAll verifies events of types
// without a registered validator are admitted unchanged (backward
// compatibility with all existing event types).
func TestAdmissionCrossCheck_NoValidatorAdmitsAll(t *testing.T) {
	d := dag.New()
	// Register a validator for an unrelated type.
	if err := d.RegisterAdmissionCrossCheck(event.EventTypeEpochBoundary, func(_ *event.Event, _ dag.WhileLockedReader) error {
		t.Error("validator for EpochBoundary fired on Transfer event")
		return errors.New("unexpected fire")
	}); err != nil {
		t.Fatalf("RegisterAdmissionCrossCheck: %v", err)
	}

	g := makeGenesis(t, "g") // EventTypeTransfer
	mustAdd(t, d, g)
}

// TestAdmissionCrossCheck_DuplicateRegistrationRejected verifies one
// validator per type is the policy.
func TestAdmissionCrossCheck_DuplicateRegistrationRejected(t *testing.T) {
	d := dag.New()

	noop := func(_ *event.Event, _ dag.WhileLockedReader) error { return nil }
	if err := d.RegisterAdmissionCrossCheck(event.EventTypeTransfer, noop); err != nil {
		t.Fatalf("first registration: %v", err)
	}
	err := d.RegisterAdmissionCrossCheck(event.EventTypeTransfer, noop)
	if !errors.Is(err, dag.ErrCrossCheckAlreadyRegistered) {
		t.Fatalf("expected ErrCrossCheckAlreadyRegistered on duplicate, got %v", err)
	}
}

// TestAdmissionCrossCheck_NilValidatorRejected verifies the registration
// guards against nil validators.
func TestAdmissionCrossCheck_NilValidatorRejected(t *testing.T) {
	d := dag.New()
	err := d.RegisterAdmissionCrossCheck(event.EventTypeTransfer, nil)
	if err == nil {
		t.Fatal("expected error registering nil validator")
	}
}

// TestAdmissionCrossCheck_WhileLockedReader_GetWorks confirms the reader
// passed to the validator can fetch already-admitted events without
// deadlock.
func TestAdmissionCrossCheck_WhileLockedReader_GetWorks(t *testing.T) {
	d := dag.New()
	parent := makeGenesis(t, "parent")
	mustAdd(t, d, parent)

	if err := d.RegisterAdmissionCrossCheck(event.EventTypeTransfer, func(_ *event.Event, reader dag.WhileLockedReader) error {
		// Skip the validator's own event by checking a known prior ID.
		fetched, err := reader.GetWhileLocked(parent.ID)
		if err != nil {
			return err
		}
		if fetched.ID != parent.ID {
			return errors.New("fetched wrong event")
		}
		return nil
	}); err != nil {
		t.Fatalf("RegisterAdmissionCrossCheck: %v", err)
	}

	child := makeChild(t, "child", parent)
	mustAdd(t, d, child)
}

// TestAdmissionCrossCheck_WhileLockedReader_CountAncestorsByTypeWorks
// confirms the lock-free count primitive returns the correct value when
// called from inside the validator.
func TestAdmissionCrossCheck_WhileLockedReader_CountAncestorsByTypeWorks(t *testing.T) {
	d := dag.New()
	g := makeGenesis(t, "g")
	mid := makeTypedChild(t, event.EventTypeEpochBoundary, g)
	mustAdd(t, d, g)
	mustAdd(t, d, mid)

	if err := d.RegisterAdmissionCrossCheck(event.EventTypeTransfer, func(ev *event.Event, reader dag.WhileLockedReader) error {
		// The candidate event being validated has CausalRefs to mid.
		// Count EpochBoundary ancestors of one of its parents (mid) — we
		// expect 0 because mid itself is the only EpochBoundary in this
		// DAG and CountAncestorsByType is irreflexive.
		count, err := reader.CountAncestorsByTypeWhileLocked(mid.ID, event.EventTypeEpochBoundary)
		if err != nil {
			return err
		}
		if count != 0 {
			return errors.New("expected zero EpochBoundary ancestors of mid (mid itself doesn't count)")
		}
		return nil
	}); err != nil {
		t.Fatalf("RegisterAdmissionCrossCheck: %v", err)
	}

	tip := makeChild(t, "tip", mid)
	mustAdd(t, d, tip)
}

// TestAdmissionCrossCheck_RestrictedAPIDocumented is a documentation
// assertion. The deadlock-on-reentrant-lock property is structural to
// the implementation: dag.Add holds d.mu.Lock for the duration of the
// validator call, so any *DAG method that acquires d.mu.RLock or
// d.mu.Lock from within the validator deadlocks (Go sync.Mutex is
// non-reentrant).
//
// We do not exercise the deadlock at runtime — doing so would leak a
// goroutine permanently for every test run, polluting other tests in
// the package. The defense relies on:
//
//  1. Documentation in RegisterAdmissionCrossCheck and WhileLockedReader
//     comments naming the discipline explicitly.
//  2. The WhileLockedReader interface providing the lock-free read
//     methods validators need; using d.IsAncestor / d.CountAncestorsByType
//     instead is a code-review red flag.
//  3. Sub-spec §9 halt trigger "Admission-cross-check validator
//     mis-implemented (reentrant lock, non-pure, I/O, runtime state)".
//
// This test exists to surface the discipline in the test file itself,
// so a future maintainer adding a new validator sees the constraint.
func TestAdmissionCrossCheck_RestrictedAPIDocumented(t *testing.T) {
	t.Log("RegisterAdmissionCrossCheck validators MUST use WhileLockedReader.")
	t.Log("Calling d.IsAncestor / d.CountAncestorsByType from within a")
	t.Log("validator will deadlock (sync.Mutex is non-reentrant).")
	t.Log("Per F5 5B canonical-epoch sub-spec v2.2 §1.4.1 + §9 halt trigger.")
}
