package projections

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

// ----- Test helpers -----------------------------------------------------------

// okProbe always returns (empty=false, nil). Used where we want a Canonical
// entry to pass validation but the test doesn't exercise HealthCheck.
func okProbe(_ context.Context) (bool, error) { return false, nil }

// emptyProbe always returns (empty=true, nil).
func emptyProbe(_ context.Context) (bool, error) { return true, nil }

// errProbe always returns (_, err).
func errProbe(err error) StateProbe {
	return func(_ context.Context) (bool, error) { return false, err }
}

// newCanonicalEntry builds a fully-populated, valid Canonical entry.
// Tests mutate one field to exercise each validation row.
func newCanonicalEntry(name string) CanonicalProjection {
	return CanonicalProjection{
		Name:                 name,
		Package:              "internal/reputation",
		StoreType:            "EvidenceStore",
		Classification:       Canonical,
		SourceEvents:         []EventType{"TaskVerificationConsensus"},
		LiveConsumerRef:      "internal/recognition.TaskVerificationConsensusConsumer",
		ReplayConsumerRef:    "internal/replay.ReputationReplayConsumer",
		ObservabilitySurface: Surface{Kind: SurfaceNodeLocalHTTP, EndpointPath: "/v1/reputation/self"},
		IntegrationTestRef:   "internal/integration.TestEvidenceStore_AccumulatesOnConsensus",
		Owner:                "state-and-consensus",
		CreatedAt:            "2026-04-14",
		StateProbe:           okProbe,
	}
}

// newAdvisoryEntry builds a fully-populated, valid Advisory entry.
func newAdvisoryEntry(name string) CanonicalProjection {
	return CanonicalProjection{
		Name:                 name,
		Package:              "internal/blobsync",
		StoreType:            "BlobServingReputation",
		Classification:       Advisory,
		SourceEvents:         []EventType{"BlobFetchComplete"},
		LiveConsumerRef:      "internal/blobsync.ServingReputationConsumer",
		ReplayConsumerRef:    "internal/blobsync.ServingReputationReplayConsumer",
		ObservabilitySurface: Surface{Kind: SurfaceNone, Justification: "advisory only; not tier-3 exposed"},
		IntegrationTestRef:   "internal/blobsync.TestServingReputation_TracksFetches",
		Owner:                "transport",
		CreatedAt:            "2026-04-14",
	}
}

// mustRegister panics on error — used to set up fixtures where registration
// should succeed unconditionally.
func mustRegister(t *testing.T, r *ProjectionRegistry, p CanonicalProjection) {
	t.Helper()
	if err := r.Register(p); err != nil {
		t.Fatalf("unexpected registration error: %v", err)
	}
}

// fixedEpoch returns an epochFn that always returns the given value.
func fixedEpoch(e uint64) func() uint64 { return func() uint64 { return e } }

// atomicEpoch returns a mutable epochFn backed by an atomic uint64.
func atomicEpoch() (func() uint64, func(uint64)) {
	var e uint64
	return func() uint64 { return atomic.LoadUint64(&e) },
		func(v uint64) { atomic.StoreUint64(&e, v) }
}

// ----- T2.1 — Empty registry is well-formed -----------------------------------

func TestRegistry_Empty(t *testing.T) {
	r := NewProjectionRegistry(fixedEpoch(0))
	if r.Len() != 0 {
		t.Fatalf("empty Len: want 0, got %d", r.Len())
	}
	if got := r.List(); len(got) != 0 {
		t.Fatalf("empty List: want [], got %#v", got)
	}
	if _, ok := r.Get("nonexistent"); ok {
		t.Fatalf("Get on empty must return ok=false")
	}
}

// ----- T2.2 — Happy path Canonical -------------------------------------------

func TestRegistry_RegisterCanonical_HappyPath(t *testing.T) {
	r := NewProjectionRegistry(fixedEpoch(5))
	entry := newCanonicalEntry("EvidenceStore")
	if err := r.Register(entry); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if r.Len() != 1 {
		t.Fatalf("Len after register: want 1, got %d", r.Len())
	}
	got, ok := r.Get("EvidenceStore")
	if !ok {
		t.Fatalf("Get: entry not found")
	}
	if got.Name != "EvidenceStore" || got.StoreType != "EvidenceStore" {
		t.Fatalf("Get returned wrong entry: %+v", got)
	}
}

// ----- T2.3 — Happy path Advisory with SurfaceNone+justification --------------

func TestRegistry_RegisterAdvisory_SurfaceNoneAllowed(t *testing.T) {
	r := NewProjectionRegistry(fixedEpoch(0))
	entry := newAdvisoryEntry("BlobServingReputation")
	if err := r.Register(entry); err != nil {
		t.Fatalf("Register advisory: %v", err)
	}
}

// ----- T2.4a — Idempotent exact match success --------------------------------

func TestRegistry_ReRegisterExactMatch_NoOp(t *testing.T) {
	getEpoch, setEpoch := atomicEpoch()
	r := NewProjectionRegistry(getEpoch)
	setEpoch(5)
	entry := newCanonicalEntry("EvidenceStore")
	// First registration at epoch=5 with empty-returning probe for later verification.
	entry.StateProbe = emptyProbe
	mustRegister(t, r, entry)

	// Advance clock and re-register with identical fields.
	setEpoch(7)
	if err := r.Register(entry); err != nil {
		t.Fatalf("idempotent re-register must succeed, got: %v", err)
	}
	if r.Len() != 1 {
		t.Fatalf("Len after idempotent re-register: want 1, got %d", r.Len())
	}

	// Verify registeredAtEpoch was not updated: at epoch=9 (ageEpochs=4 > 3,
	// strictly past window) HealthCheck must report HealthEmpty. If
	// registeredAtEpoch had been updated to 7, ageEpochs at 9 would be 2
	// (still within window) and the status would be HealthNotYetEligible.
	setEpoch(9)
	hs := r.HealthCheck(context.Background())
	if hs.Overall != HealthEmpty {
		t.Fatalf("Overall after past-window idempotent re-register: want HealthEmpty (proving registeredAtEpoch unchanged), got %v", hs.Overall)
	}
}

// ----- T2.4b..T2.4g, T2.4i, T2.4j — Mismatch cases ----------------------------

func TestRegistry_ReRegisterMismatch(t *testing.T) {
	cases := []struct {
		name     string
		mutate   func(*CanonicalProjection)
		wantSub  string // substring of expected error
	}{
		{
			name:    "Package",
			mutate:  func(p *CanonicalProjection) { p.Package = "internal/other" },
			wantSub: "different Package",
		},
		{
			name:    "StoreType",
			mutate:  func(p *CanonicalProjection) { p.StoreType = "OtherStore" },
			wantSub: "different StoreType",
		},
		{
			name:    "Classification",
			mutate:  func(p *CanonicalProjection) { p.Classification = Advisory },
			wantSub: "different Classification",
		},
		{
			name:    "SourceEvents",
			mutate:  func(p *CanonicalProjection) { p.SourceEvents = []EventType{"Other"} },
			wantSub: "different SourceEvents",
		},
		{
			name:    "LiveConsumerRef",
			mutate:  func(p *CanonicalProjection) { p.LiveConsumerRef = "pkg.OtherConsumer" },
			wantSub: "different LiveConsumerRef",
		},
		{
			name:    "ReplayConsumerRef",
			mutate:  func(p *CanonicalProjection) { p.ReplayConsumerRef = "pkg.OtherReplay" },
			wantSub: "different ReplayConsumerRef",
		},
		{
			name:    "ObservabilitySurface",
			mutate:  func(p *CanonicalProjection) { p.ObservabilitySurface = Surface{Kind: SurfaceCLI} },
			wantSub: "different ObservabilitySurface",
		},
		{
			name:    "IntegrationTestRef",
			mutate:  func(p *CanonicalProjection) { p.IntegrationTestRef = "pkg.TestOther" },
			wantSub: "different IntegrationTestRef",
		},
		{
			name:    "Owner",
			mutate:  func(p *CanonicalProjection) { p.Owner = "other-team" },
			wantSub: "different Owner",
		},
		{
			name:    "CreatedAt",
			mutate:  func(p *CanonicalProjection) { p.CreatedAt = "2026-04-15" },
			wantSub: "different CreatedAt",
		},
		{
			name: "AllowIdleWithJustification",
			mutate: func(p *CanonicalProjection) {
				p.AllowIdleWithJustification = true
				p.IdleJustification = "CR-9 named exception for ChallengeResolutionStore"
			},
			wantSub: "different AllowIdleWithJustification",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := NewProjectionRegistry(fixedEpoch(0))
			base := newCanonicalEntry("EvidenceStore")
			mustRegister(t, r, base)

			mutated := base
			tc.mutate(&mutated)

			err := r.Register(mutated)
			if err == nil {
				t.Fatalf("expected mismatch error, got nil")
			}
			msg := err.Error()
			if !strings.Contains(msg, tc.wantSub) {
				t.Fatalf("error message missing %q: got %q", tc.wantSub, msg)
			}
			if !strings.Contains(msg, "EvidenceStore") {
				t.Fatalf("error message missing entry name: got %q", msg)
			}
		})
	}
}

// ----- T2.4h — Different StateProbe, identical non-function fields, no-op ----

func TestRegistry_ReRegisterDifferentProbe_NoOp_FirstProbeRetained(t *testing.T) {
	getEpoch, setEpoch := atomicEpoch()
	r := NewProjectionRegistry(getEpoch)

	var probeACount, probeBCount int32
	probeA := func(_ context.Context) (bool, error) {
		atomic.AddInt32(&probeACount, 1)
		return true, nil
	}
	probeB := func(_ context.Context) (bool, error) {
		atomic.AddInt32(&probeBCount, 1)
		return true, nil
	}

	setEpoch(0)
	entry := newCanonicalEntry("EvidenceStore")
	entry.StateProbe = probeA
	mustRegister(t, r, entry)

	// Re-register with different probe; non-function fields identical.
	entry.StateProbe = probeB
	if err := r.Register(entry); err != nil {
		t.Fatalf("differing-probe re-register must succeed: %v", err)
	}

	// Drive HealthCheck past the window; first-registered probe must be called.
	setEpoch(4)
	_ = r.HealthCheck(context.Background())
	if atomic.LoadInt32(&probeACount) != 1 {
		t.Fatalf("probeA must be called exactly once, got %d", probeACount)
	}
	if atomic.LoadInt32(&probeBCount) != 0 {
		t.Fatalf("probeB must NOT be called (first-registered probe retained), got %d", probeBCount)
	}
}

// ----- V1..V16 validation rows (except V2 which is replaced by idempotency) --

func TestRegistry_Validation(t *testing.T) {
	type row struct {
		name    string
		mutate  func(*CanonicalProjection)
		wantSub string
	}
	rows := []row{
		// V1 — Name required
		{
			name:    "V1_NameEmpty",
			mutate:  func(p *CanonicalProjection) { p.Name = "" },
			wantSub: "Name is required",
		},
		// V3 — invalid Classification
		{
			name:    "V3_ClassificationZero",
			mutate:  func(p *CanonicalProjection) { p.Classification = 0 },
			wantSub: "Classification must be Canonical or Advisory",
		},
		{
			name:    "V3_ClassificationOutOfRange",
			mutate:  func(p *CanonicalProjection) { p.Classification = Classification(99) },
			wantSub: "Classification must be Canonical or Advisory",
		},
		// V4 — LiveConsumerRef required for Canonical (PR-1)
		{
			name:    "V4_LiveConsumerRefEmpty",
			mutate:  func(p *CanonicalProjection) { p.LiveConsumerRef = "" },
			wantSub: "LiveConsumerRef is required (PR-1)",
		},
		// V5 — ReplayConsumerRef required for Canonical (PR-2)
		{
			name:    "V5_ReplayConsumerRefEmpty",
			mutate:  func(p *CanonicalProjection) { p.ReplayConsumerRef = "" },
			wantSub: "ReplayConsumerRef is required (PR-2)",
		},
		// V6 — IntegrationTestRef required for Canonical (PR-3)
		{
			name:    "V6_IntegrationTestRefEmpty",
			mutate:  func(p *CanonicalProjection) { p.IntegrationTestRef = "" },
			wantSub: "IntegrationTestRef is required (PR-3)",
		},
		// V7 — Canonical cannot have SurfaceNone (PR-4)
		{
			name: "V7_CanonicalSurfaceNone",
			mutate: func(p *CanonicalProjection) {
				p.ObservabilitySurface = Surface{Kind: SurfaceNone, Justification: "irrelevant"}
			},
			wantSub: "ObservabilitySurface cannot be None (PR-4)",
		},
		// V11 — Canonical without idle-exception must have StateProbe
		{
			name:    "V11_CanonicalMissingProbe",
			mutate:  func(p *CanonicalProjection) { p.StateProbe = nil },
			wantSub: "StateProbe is required unless AllowIdleWithJustification is set (PR-5)",
		},
		// V12 — Package required
		{
			name:    "V12_PackageEmpty",
			mutate:  func(p *CanonicalProjection) { p.Package = "" },
			wantSub: "Package is required",
		},
		// V13 — StoreType required
		{
			name:    "V13_StoreTypeEmpty",
			mutate:  func(p *CanonicalProjection) { p.StoreType = "" },
			wantSub: "StoreType is required",
		},
		// V14 — Owner required
		{
			name:    "V14_OwnerEmpty",
			mutate:  func(p *CanonicalProjection) { p.Owner = "" },
			wantSub: "Owner is required",
		},
		// V15 — CreatedAt required
		{
			name:    "V15_CreatedAtEmpty",
			mutate:  func(p *CanonicalProjection) { p.CreatedAt = "" },
			wantSub: "CreatedAt is required",
		},
		// V16 — Canonical SourceEvents non-empty
		{
			name:    "V16_CanonicalSourceEventsEmpty",
			mutate:  func(p *CanonicalProjection) { p.SourceEvents = nil },
			wantSub: "SourceEvents must be non-empty",
		},
		// V9 — AllowIdleWithJustification=true without IdleJustification
		{
			name: "V9_IdleWithoutJustification",
			mutate: func(p *CanonicalProjection) {
				p.AllowIdleWithJustification = true
				p.IdleJustification = ""
			},
			wantSub: "AllowIdleWithJustification requires non-empty IdleJustification (CR-9)",
		},
		// V10 — IdleJustification non-empty without AllowIdleWithJustification
		{
			name: "V10_JustificationWithoutFlag",
			mutate: func(p *CanonicalProjection) {
				p.AllowIdleWithJustification = false
				p.IdleJustification = "stray justification"
			},
			wantSub: "IdleJustification provided without AllowIdleWithJustification (CR-9)",
		},
	}

	for _, rc := range rows {
		t.Run(rc.name, func(t *testing.T) {
			r := NewProjectionRegistry(fixedEpoch(0))
			entry := newCanonicalEntry("X")
			rc.mutate(&entry)
			err := r.Register(entry)
			if err == nil {
				t.Fatalf("expected validation error, got nil")
			}
			if !strings.Contains(err.Error(), rc.wantSub) {
				t.Fatalf("want error to contain %q, got %q", rc.wantSub, err.Error())
			}
		})
	}
}

// V8 — Advisory with SurfaceNone requires Justification.
func TestRegistry_Validation_V8_AdvisorySurfaceNoneNeedsJustification(t *testing.T) {
	r := NewProjectionRegistry(fixedEpoch(0))
	entry := newAdvisoryEntry("Adv")
	entry.ObservabilitySurface = Surface{Kind: SurfaceNone} // empty Justification
	err := r.Register(entry)
	if err == nil {
		t.Fatalf("expected V8 error, got nil")
	}
	if !strings.Contains(err.Error(), "ObservabilitySurface=None requires Justification (PR-4)") {
		t.Fatalf("want V8 error substring, got %q", err.Error())
	}
}

// V11 exception: Canonical with AllowIdleWithJustification may omit StateProbe.
func TestRegistry_V11_Exception_IdleCanonicalNeedsNoProbe(t *testing.T) {
	r := NewProjectionRegistry(fixedEpoch(0))
	entry := newCanonicalEntry("ChallengeResolutionStore")
	entry.AllowIdleWithJustification = true
	entry.IdleJustification = "CR-9 named exception; producer deferred to challenge-path workstream"
	entry.StateProbe = nil
	if err := r.Register(entry); err != nil {
		t.Fatalf("idle Canonical without probe should pass: %v", err)
	}
}

// T2.20 — MustRegister success does not panic.
func TestRegistry_MustRegister_Success(t *testing.T) {
	r := NewProjectionRegistry(fixedEpoch(0))
	defer func() {
		if rec := recover(); rec != nil {
			t.Fatalf("MustRegister on valid entry must not panic, got: %v", rec)
		}
	}()
	r.MustRegister(newCanonicalEntry("X"))
	if r.Len() != 1 {
		t.Fatalf("Len after MustRegister: want 1, got %d", r.Len())
	}
}

// T2.21 — MustRegister panic on invalid.
func TestRegistry_MustRegister_PanicOnInvalid(t *testing.T) {
	r := NewProjectionRegistry(fixedEpoch(0))
	entry := newCanonicalEntry("X")
	entry.LiveConsumerRef = "" // trips V4
	defer func() {
		rec := recover()
		if rec == nil {
			t.Fatalf("MustRegister on invalid entry must panic")
		}
		msg := fmt.Sprintf("%v", rec)
		if !strings.Contains(msg, "MustRegister failed") {
			t.Fatalf("panic must mention MustRegister failed: %q", msg)
		}
		if !strings.Contains(msg, "LiveConsumerRef is required (PR-1)") {
			t.Fatalf("panic must include underlying validation error: %q", msg)
		}
	}()
	r.MustRegister(entry)
}

// T2.22 — Concurrent reads while registering (-race).
func TestRegistry_ConcurrentReadsWhileRegistering(t *testing.T) {
	r := NewProjectionRegistry(fixedEpoch(0))
	const workers = 4
	const iterations = 50

	var wg sync.WaitGroup
	// Writer goroutine: registers A, B, C, D sequentially.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i, n := range []string{"A", "B", "C", "D"} {
			e := newCanonicalEntry(n)
			// Vary an innocuous-looking field to make sure each is unique.
			e.StoreType = fmt.Sprintf("Store%d", i)
			if err := r.Register(e); err != nil {
				t.Errorf("register %s: %v", n, err)
				return
			}
		}
	}()
	// Reader goroutines: hammer Len/Get/List.
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				_ = r.Len()
				_, _ = r.Get("A")
				_ = r.List()
			}
		}()
	}
	wg.Wait()
	if r.Len() != 4 {
		t.Fatalf("final Len: want 4, got %d", r.Len())
	}
}

// T2.23 — List is sorted by Name.
func TestRegistry_List_Sorted(t *testing.T) {
	r := NewProjectionRegistry(fixedEpoch(0))
	for _, n := range []string{"Zeta", "Alpha", "Mu"} {
		mustRegister(t, r, newCanonicalEntry(n))
	}
	got := r.List()
	if len(got) != 3 {
		t.Fatalf("List len: want 3, got %d", len(got))
	}
	want := []string{"Alpha", "Mu", "Zeta"}
	for i, p := range got {
		if p.Name != want[i] {
			t.Fatalf("List[%d].Name: want %q, got %q (full list: %v)",
				i, want[i], p.Name, projectionNames(got))
		}
	}
}

func projectionNames(ps []CanonicalProjection) []string {
	out := make([]string, len(ps))
	for i, p := range ps {
		out[i] = p.Name
	}
	return out
}

// T2.24 — Register captures registeredAtEpoch.
// Validated by: register at epoch=5, advance to epoch=5+EligibilityWindow+1
// (=9), HealthCheck must see ageEpochs=4 > 3 and call the probe. We use an
// empty probe so the entry status flips to HealthEmpty — only possible if
// the registry captured epoch=5 at register time.
func TestRegistry_Register_CapturesEpoch(t *testing.T) {
	getEpoch, setEpoch := atomicEpoch()
	r := NewProjectionRegistry(getEpoch)
	setEpoch(5)
	entry := newCanonicalEntry("X")
	entry.StateProbe = emptyProbe
	mustRegister(t, r, entry)

	setEpoch(9) // ageEpochs = 4 > EligibilityWindow (3)
	hs := r.HealthCheck(context.Background())
	if hs.Overall != HealthEmpty {
		t.Fatalf("Overall: want HealthEmpty (registration at epoch 5 should be past window by epoch 9), got %v", hs.Overall)
	}
}

// Sanity: reflect.DeepEqual on SourceEvents catches slice mismatches the
// field-by-field comparator relies on.
func TestRegistry_SourceEventsDeepEqualSanity(t *testing.T) {
	a := []EventType{"A", "B"}
	b := []EventType{"A", "B"}
	c := []EventType{"A", "C"}
	if !reflect.DeepEqual(a, b) {
		t.Fatalf("DeepEqual must match identical slices")
	}
	if reflect.DeepEqual(a, c) {
		t.Fatalf("DeepEqual must not match differing slices")
	}
}

// Ensure Register returns a non-nil error sentinel for callers that want
// errors.Is-style checks. This is a soft check — we just verify the error
// is not an ambiguous sentinel shared across distinct validation failures.
func TestRegistry_DistinctErrors(t *testing.T) {
	r := NewProjectionRegistry(fixedEpoch(0))
	e1 := newCanonicalEntry("X")
	e1.Name = ""
	err1 := r.Register(e1)

	e2 := newCanonicalEntry("Y")
	e2.LiveConsumerRef = ""
	err2 := r.Register(e2)

	if errors.Is(err1, err2) || errors.Is(err2, err1) {
		t.Fatalf("distinct validation errors must not alias: %v / %v", err1, err2)
	}
}
