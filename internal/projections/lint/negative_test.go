package lint

import (
	"strings"
	"testing"
)

// TestLint_FailsOnUnregisteredStore is a deliberate negative test: it
// constructs a synthetic module containing a Suspect type (has
// *badger.DB field, has writer methods) that is NOT registered via a
// *Projection() constructor, runs the full lint, and asserts the
// Report flags it as an UnregisteredType.
//
// This is the step-3 inverse of step-2's per-test meta-assertion: the
// registry-side drift check.
func TestLint_FailsOnUnregisteredStore(t *testing.T) {
	dir := t.TempDir()
	writeSyntheticModule(t, dir, "example.com/m", map[string]string{
		"pkg/rogue.go": `package pkg

import (
	"context"

	badger "github.com/dgraph-io/badger/v4"
)

// RogueStore is deliberately unregistered to exercise the lint's
// unregistered-store diagnostic in the step-3 negative-test suite.
type RogueStore struct {
	db *badger.DB
}

func (r *RogueStore) PutSecret(ctx context.Context, k, v []byte) error {
	return r.db.Update(func(txn *badger.Txn) error {
		return txn.Set(k, v)
	})
}
`,
	}, moduleRoot(t))

	report, err := Check(dir)
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if !report.HasFailures() {
		t.Fatalf("lint must flag unregistered RogueStore; got: %+v", report)
	}
	if len(report.UnregisteredTypes) != 1 {
		t.Fatalf("want 1 UnregisteredType, got %d", len(report.UnregisteredTypes))
	}
	s := report.UnregisteredTypes[0]
	if s.TypeName != "RogueStore" {
		t.Errorf("TypeName: want RogueStore, got %q", s.TypeName)
	}
	// Verify the diagnostic string carries all four required elements.
	diag := report.Format()
	mustContain := []string{
		"UNREGISTERED STORE",
		"RogueStore",
		"rogue.go",
		"Line:",
		"(a) register this type",
		"(b) suppress the lint",
		"registration patterns defeat",
	}
	for _, want := range mustContain {
		if !strings.Contains(diag, want) {
			t.Errorf("diagnostic missing %q\n---\n%s", want, diag)
		}
	}
}

// TestLint_FailsOnMissingTestRef is a deliberate negative test: a
// synthetic module contains a Canonical *Projection() with an
// IntegrationTestRef pointing at a non-existent test symbol. The lint
// must flag this as a MissingRef failure.
func TestLint_FailsOnMissingTestRef(t *testing.T) {
	dir := t.TempDir()
	writeSyntheticModule(t, dir, "example.com/m", map[string]string{
		"pkg/store.go": `package pkg

import "github.com/Aethernet-network/aethernet/internal/projections"

type S struct{}

func SProjection(s *S) projections.CanonicalProjection {
	return projections.CanonicalProjection{
		Name: "S", Package: "example.com/m/pkg", StoreType: "S",
		Classification: projections.Canonical,
		SourceEvents: []projections.EventType{"E"},
		LiveConsumerRef: "x.Live", ReplayConsumerRef: "x.Replay",
		ObservabilitySurface: projections.Surface{Kind: projections.SurfaceHealth},
		IntegrationTestRef: "example.com/m/integration.TestS_DoesNotExist",
		Owner: "me", CreatedAt: "2026-04-15",
	}
}
`,
		"integration/integration_test.go": `package integration

import "testing"

// A different test; the projection points at TestS_DoesNotExist.
func TestS_SomethingElse(t *testing.T) {}
`,
	}, moduleRoot(t))

	report, err := Check(dir)
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if !report.HasFailures() {
		t.Fatalf("lint must flag missing testref; got report: %+v", report)
	}
	if len(report.MissingRefs) < 1 {
		t.Fatalf("want at least 1 MissingRef, got %d", len(report.MissingRefs))
	}
	// Check the diagnostic.
	diag := report.Format()
	mustContain := []string{
		"MISSING INTEGRATION TEST",
		"TestS_DoesNotExist",
		"PR-3 invariant",
	}
	for _, want := range mustContain {
		if !strings.Contains(diag, want) {
			t.Errorf("diagnostic missing %q\n---\n%s", want, diag)
		}
	}
}

// TestLint_FailsOnInsufficientPragma is a deliberate negative test:
// a synthetic module contains a Suspect type with a suppression pragma
// whose reason is too short OR too few words. The lint must flag this
// as an InsufficientSuppression failure rather than allowing the
// suppression through.
func TestLint_FailsOnInsufficientPragma(t *testing.T) {
	dir := t.TempDir()
	writeSyntheticModule(t, dir, "example.com/m", map[string]string{
		"pkg/x.go": `package pkg

import badger "github.com/dgraph-io/badger/v4"

// projections:lint ignore "too short"
type BadReason struct {
	db *badger.DB
}

func (b *BadReason) Put() error { return nil }
`,
	}, moduleRoot(t))

	report, err := Check(dir)
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if !report.HasFailures() {
		t.Fatalf("lint must flag insufficient pragma; got report: %+v", report)
	}
	if len(report.InsufficientSuppressions) != 1 {
		t.Fatalf("want 1 InsufficientSuppression, got %d", len(report.InsufficientSuppressions))
	}
	insuff := report.InsufficientSuppressions[0]
	if insuff.Reason != "too short" {
		t.Errorf("Reason: want 'too short', got %q", insuff.Reason)
	}
	if insuff.CharCount != 9 {
		t.Errorf("CharCount: want 9, got %d", insuff.CharCount)
	}
	if insuff.WordCount != 2 {
		t.Errorf("WordCount: want 2, got %d", insuff.WordCount)
	}
	// Additionally, because the pragma was rejected, the type should also
	// still appear — or NOT — depending on how the matcher handles the
	// rejection. Current semantics: insufficient pragma skips the type
	// from suspects but records it in InsufficientSuppressions. Either
	// way, the lint fails and points the contributor at the bad pragma.
	diag := report.Format()
	mustContain := []string{
		"INSUFFICIENT SUPPRESSION JUSTIFICATION",
		`"too short"`,
		"(9 chars, 2 words)",
		"at least 20 characters",
	}
	for _, want := range mustContain {
		if !strings.Contains(diag, want) {
			t.Errorf("diagnostic missing %q\n---\n%s", want, diag)
		}
	}
}

// TestLint_SuppressionWithValidPragma is the happy-counterpart to
// TestLint_FailsOnInsufficientPragma: a synthetic module with a
// Suspect type and a properly-justified pragma must pass the lint.
func TestLint_SuppressionWithValidPragma(t *testing.T) {
	dir := t.TempDir()
	writeSyntheticModule(t, dir, "example.com/m", map[string]string{
		"pkg/x.go": `package pkg

import badger "github.com/dgraph-io/badger/v4"

// projections:lint ignore "legitimate helper cache for rate limiting; never feeds any consensus decision path"
type ValidSuppression struct {
	db *badger.DB
}

func (v *ValidSuppression) Put() error { return nil }
`,
	}, moduleRoot(t))

	report, err := Check(dir)
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if report.HasFailures() {
		t.Fatalf("valid pragma should suppress the lint failure; got: %s", report.Format())
	}
}
