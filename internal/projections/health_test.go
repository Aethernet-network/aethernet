package projections

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// T3.1 — Empty registry is HealthOK.
func TestHealth_EmptyRegistry(t *testing.T) {
	r := NewProjectionRegistry(fixedEpoch(0))
	hs := r.HealthCheck(context.Background())
	if hs.Overall != HealthOK {
		t.Fatalf("empty registry Overall: want HealthOK, got %v", hs.Overall)
	}
	if len(hs.Checks) != 0 {
		t.Fatalf("empty registry Checks: want [], got %#v", hs.Checks)
	}
}

// T3.2 — Advisory entry returns HealthAdvisory regardless of state/probe.
func TestHealth_Advisory_AlwaysAdvisory(t *testing.T) {
	getEpoch, setEpoch := atomicEpoch()
	r := NewProjectionRegistry(getEpoch)
	entry := newAdvisoryEntry("Adv")
	entry.StateProbe = emptyProbe // would flip canonical to HealthEmpty; advisory ignores
	mustRegister(t, r, entry)

	setEpoch(10) // well past window
	hs := r.HealthCheck(context.Background())
	if hs.Overall != HealthOK {
		t.Fatalf("Overall with only Advisory entry: want HealthOK, got %v", hs.Overall)
	}
	if len(hs.Checks) != 1 {
		t.Fatalf("Checks: want 1, got %d", len(hs.Checks))
	}
	if hs.Checks[0].Health != HealthAdvisory {
		t.Fatalf("entry health: want HealthAdvisory, got %v", hs.Checks[0].Health)
	}
}

// T3.3 — Canonical within window returns HealthNotYetEligible.
func TestHealth_Canonical_WithinWindow(t *testing.T) {
	getEpoch, setEpoch := atomicEpoch()
	r := NewProjectionRegistry(getEpoch)
	setEpoch(0)
	entry := newCanonicalEntry("EvidenceStore")
	entry.StateProbe = emptyProbe
	mustRegister(t, r, entry)

	setEpoch(1) // ageEpochs = 1 < EligibilityWindow (3)
	hs := r.HealthCheck(context.Background())
	if hs.Overall != HealthOK {
		t.Fatalf("Overall: want HealthOK (within window), got %v", hs.Overall)
	}
	if hs.Checks[0].Health != HealthNotYetEligible {
		t.Fatalf("entry health: want HealthNotYetEligible, got %v", hs.Checks[0].Health)
	}
}

// T3.4 — Canonical exactly at window boundary is still not yet eligible.
// The health rule is strict "more than EligibilityWindow epochs" per plan §9.5.
func TestHealth_Canonical_AtBoundary_NotYetEligible(t *testing.T) {
	getEpoch, setEpoch := atomicEpoch()
	r := NewProjectionRegistry(getEpoch)
	setEpoch(0)
	entry := newCanonicalEntry("EvidenceStore")
	entry.StateProbe = emptyProbe
	mustRegister(t, r, entry)

	setEpoch(2) // ageEpochs = 2, EligibilityWindow = 3; 2 < 3 -> still not yet eligible
	hs := r.HealthCheck(context.Background())
	if hs.Checks[0].Health != HealthNotYetEligible {
		t.Fatalf("at ageEpochs=2 (< 3): want HealthNotYetEligible, got %v", hs.Checks[0].Health)
	}

	setEpoch(3) // ageEpochs = 3; 3 is NOT "more than 3", still not yet eligible
	hs = r.HealthCheck(context.Background())
	if hs.Checks[0].Health != HealthNotYetEligible {
		t.Fatalf("at ageEpochs=3 (strict > semantic): want HealthNotYetEligible, got %v", hs.Checks[0].Health)
	}
}

// T3.5 — Canonical past window with populated state returns HealthOK.
func TestHealth_Canonical_PastWindow_Populated(t *testing.T) {
	getEpoch, setEpoch := atomicEpoch()
	r := NewProjectionRegistry(getEpoch)
	setEpoch(0)
	entry := newCanonicalEntry("EvidenceStore")
	entry.StateProbe = okProbe // not empty
	mustRegister(t, r, entry)

	setEpoch(4) // ageEpochs = 4 > EligibilityWindow (3)
	hs := r.HealthCheck(context.Background())
	if hs.Overall != HealthOK {
		t.Fatalf("Overall: want HealthOK, got %v", hs.Overall)
	}
	if hs.Checks[0].Health != HealthOK {
		t.Fatalf("entry: want HealthOK, got %v (reason: %s)", hs.Checks[0].Health, hs.Checks[0].Reason)
	}
}

// T3.6 — Canonical past window with empty state returns HealthEmpty, flips Overall.
func TestHealth_Canonical_PastWindow_Empty(t *testing.T) {
	getEpoch, setEpoch := atomicEpoch()
	r := NewProjectionRegistry(getEpoch)
	setEpoch(0)
	entry := newCanonicalEntry("EvidenceStore")
	entry.StateProbe = emptyProbe
	mustRegister(t, r, entry)

	setEpoch(4)
	hs := r.HealthCheck(context.Background())
	if hs.Overall != HealthEmpty {
		t.Fatalf("Overall: want HealthEmpty, got %v", hs.Overall)
	}
	if hs.Checks[0].Health != HealthEmpty {
		t.Fatalf("entry: want HealthEmpty, got %v", hs.Checks[0].Health)
	}
	if !strings.Contains(hs.Checks[0].Reason, "(PR-5)") {
		t.Fatalf("reason must cite PR-5, got %q", hs.Checks[0].Reason)
	}
}

// T3.7 — Canonical past window with probe error returns HealthProbeFailed.
func TestHealth_Canonical_PastWindow_ProbeError(t *testing.T) {
	getEpoch, setEpoch := atomicEpoch()
	r := NewProjectionRegistry(getEpoch)
	setEpoch(0)
	entry := newCanonicalEntry("EvidenceStore")
	boom := errors.New("boom")
	entry.StateProbe = errProbe(boom)
	mustRegister(t, r, entry)

	setEpoch(4)
	hs := r.HealthCheck(context.Background())
	if hs.Overall != HealthProbeFailed {
		t.Fatalf("Overall: want HealthProbeFailed, got %v", hs.Overall)
	}
	if hs.Checks[0].Health != HealthProbeFailed {
		t.Fatalf("entry: want HealthProbeFailed, got %v", hs.Checks[0].Health)
	}
	if !strings.Contains(hs.Checks[0].Reason, "boom") {
		t.Fatalf("reason must contain probe error: %q", hs.Checks[0].Reason)
	}
}

// T3.8 — AllowIdleWithJustification past window returns HealthAllowedIdle.
func TestHealth_Canonical_AllowedIdle(t *testing.T) {
	getEpoch, setEpoch := atomicEpoch()
	r := NewProjectionRegistry(getEpoch)
	setEpoch(0)
	entry := newCanonicalEntry("ChallengeResolutionStore")
	entry.AllowIdleWithJustification = true
	entry.IdleJustification = "CR-9: producer deferred to challenge-path workstream"
	entry.StateProbe = nil // V11 exception
	mustRegister(t, r, entry)

	setEpoch(10) // well past window
	hs := r.HealthCheck(context.Background())
	if hs.Overall != HealthOK {
		t.Fatalf("Overall with AllowedIdle: want HealthOK, got %v", hs.Overall)
	}
	if hs.Checks[0].Health != HealthAllowedIdle {
		t.Fatalf("entry: want HealthAllowedIdle, got %v", hs.Checks[0].Health)
	}
	if !strings.Contains(hs.Checks[0].Reason, "CR-9") {
		t.Fatalf("reason must surface the justification (CR-9): %q", hs.Checks[0].Reason)
	}
}

// T3.9 — HealthProbeFailed ranks above HealthEmpty in Overall.
func TestHealth_Overall_ProbeFailedRanksAboveEmpty(t *testing.T) {
	getEpoch, setEpoch := atomicEpoch()
	r := NewProjectionRegistry(getEpoch)
	setEpoch(0)

	a := newCanonicalEntry("A")
	a.StateProbe = emptyProbe
	mustRegister(t, r, a)

	b := newCanonicalEntry("B")
	b.StateProbe = errProbe(errors.New("B-broken"))
	mustRegister(t, r, b)

	setEpoch(4)
	hs := r.HealthCheck(context.Background())
	if hs.Overall != HealthProbeFailed {
		t.Fatalf("Overall: want HealthProbeFailed (ranks above HealthEmpty), got %v", hs.Overall)
	}
}

// T3.10 — Mixed registry: all statuses classified correctly; Overall healthy.
func TestHealth_Mixed(t *testing.T) {
	getEpoch, setEpoch := atomicEpoch()
	r := NewProjectionRegistry(getEpoch)
	setEpoch(0)

	// Advisory (should classify as HealthAdvisory).
	adv := newAdvisoryEntry("Adv")
	mustRegister(t, r, adv)

	// Canonical past-window populated (will be HealthOK after window).
	healthy := newCanonicalEntry("Healthy")
	healthy.StateProbe = okProbe
	mustRegister(t, r, healthy)

	// AllowedIdle canonical (will be HealthAllowedIdle after window).
	idle := newCanonicalEntry("Idle")
	idle.AllowIdleWithJustification = true
	idle.IdleJustification = "CR-9: challenge-path workstream"
	idle.StateProbe = nil
	mustRegister(t, r, idle)

	// Register Within LAST at epoch=4 so it stays within its window after the
	// final setEpoch(5) below (ageEpochs=1, EligibilityWindow=3).
	setEpoch(4)
	within := newCanonicalEntry("Within")
	within.StateProbe = emptyProbe
	mustRegister(t, r, within)

	// At epoch=5: Adv age=5 (Advisory), Healthy age=5 past-window (HealthOK),
	// Idle age=5 past-window (AllowedIdle), Within age=1 still in-window
	// (NotYetEligible). Overall HealthOK.
	setEpoch(5)
	hs := r.HealthCheck(context.Background())
	if hs.Overall != HealthOK {
		t.Fatalf("Overall: want HealthOK, got %v (%#v)", hs.Overall, hs.Checks)
	}
	if len(hs.Checks) != 4 {
		t.Fatalf("Checks: want 4, got %d", len(hs.Checks))
	}
	// List is sorted by Name: Adv, Healthy, Idle, Within.
	want := map[string]ProjectionHealth{
		"Adv":     HealthAdvisory,
		"Within":  HealthNotYetEligible,
		"Healthy": HealthOK,
		"Idle":    HealthAllowedIdle,
	}
	for _, c := range hs.Checks {
		exp, ok := want[c.Name]
		if !ok {
			t.Fatalf("unexpected check: %s", c.Name)
		}
		if c.Health != exp {
			t.Fatalf("%s: want %v, got %v (reason %q)", c.Name, exp, c.Health, c.Reason)
		}
	}
}

// T3.11 — Clock-backwards safety: no integer underflow when epochFn returns a
// value less than registeredAtEpoch.
func TestHealth_ClockBackwards(t *testing.T) {
	getEpoch, setEpoch := atomicEpoch()
	r := NewProjectionRegistry(getEpoch)
	setEpoch(5)
	entry := newCanonicalEntry("X")
	entry.StateProbe = emptyProbe
	mustRegister(t, r, entry)

	setEpoch(3) // "clock reset" — operator rewound time
	hs := r.HealthCheck(context.Background())
	if hs.Overall != HealthOK {
		t.Fatalf("clock-backwards Overall: want HealthOK, got %v", hs.Overall)
	}
	if hs.Checks[0].Health != HealthNotYetEligible {
		t.Fatalf("clock-backwards entry: want HealthNotYetEligible, got %v", hs.Checks[0].Health)
	}
}

// T3.12 — Probe receives caller context.
func TestHealth_Probe_ReceivesContext(t *testing.T) {
	type ctxKey struct{}
	getEpoch, setEpoch := atomicEpoch()
	r := NewProjectionRegistry(getEpoch)
	setEpoch(0)

	var seenValue interface{}
	probe := func(ctx context.Context) (bool, error) {
		seenValue = ctx.Value(ctxKey{})
		return false, nil
	}
	entry := newCanonicalEntry("X")
	entry.StateProbe = probe
	mustRegister(t, r, entry)

	setEpoch(4)
	ctx := context.WithValue(context.Background(), ctxKey{}, "hello")
	_ = r.HealthCheck(ctx)
	if seenValue != "hello" {
		t.Fatalf("probe did not receive ctx value: got %v", seenValue)
	}
}

// T3.13 — MANDATORY restart semantic (Clarification 2). Verifies
// registeredAtEpoch is captured once and a fresh registry resets the window.
func TestHealth_RestartSemantic(t *testing.T) {
	getEpoch, setEpoch := atomicEpoch()

	// Step 1-2: R1 at epoch=0, register with always-empty probe.
	r1 := NewProjectionRegistry(getEpoch)
	setEpoch(0)
	entry := newCanonicalEntry("EvidenceStore")
	entry.StateProbe = emptyProbe
	mustRegister(t, r1, entry)

	// Step 3: at epoch=2, within window — NotYetEligible, Overall HealthOK.
	setEpoch(2)
	hs1a := r1.HealthCheck(context.Background())
	if hs1a.Overall != HealthOK {
		t.Fatalf("R1 step 3 Overall: want HealthOK, got %v", hs1a.Overall)
	}
	if hs1a.Checks[0].Health != HealthNotYetEligible {
		t.Fatalf("R1 step 3 entry: want HealthNotYetEligible, got %v", hs1a.Checks[0].Health)
	}

	// Step 4: at epoch=4, past window — HealthEmpty, Overall HealthEmpty.
	setEpoch(4)
	hs1b := r1.HealthCheck(context.Background())
	if hs1b.Overall != HealthEmpty {
		t.Fatalf("R1 step 4 Overall: want HealthEmpty, got %v", hs1b.Overall)
	}
	if hs1b.Checks[0].Health != HealthEmpty {
		t.Fatalf("R1 step 4 entry: want HealthEmpty, got %v", hs1b.Checks[0].Health)
	}

	// Step 5: fresh registry R2 at epoch=10 with same projection.
	setEpoch(10)
	r2 := NewProjectionRegistry(getEpoch)
	mustRegister(t, r2, entry)

	// Step 6: at epoch=12, within R2's window — NotYetEligible, Overall HealthOK.
	// This is the key assertion: the 3-epoch window resets per registry.
	setEpoch(12)
	hs2 := r2.HealthCheck(context.Background())
	if hs2.Overall != HealthOK {
		t.Fatalf("R2 step 6 Overall: want HealthOK (fresh window), got %v", hs2.Overall)
	}
	if hs2.Checks[0].Health != HealthNotYetEligible {
		t.Fatalf("R2 step 6 entry: want HealthNotYetEligible (fresh window), got %v (reason %q)",
			hs2.Checks[0].Health, hs2.Checks[0].Reason)
	}
}
