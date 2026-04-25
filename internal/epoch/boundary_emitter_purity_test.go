package epoch_test

import (
	"os"
	"strings"
	"testing"
)

// TestBoundaryEmitter_PurityGrep enforces sub-spec v2.2 §12.1 primary
// hidden-error pattern: the EpochBoundary emitter MUST read canonical
// DAG state ONLY via CountAncestorsByType. NO reads of round.RoundCounter,
// local counters, or non-canonical projections — those are exactly the
// pattern this sub-spec exists to fix.
//
// Mechanism: read boundary_emitter.go source bytes; assert no occurrence
// of the forbidden tokens. This is a coarse but load-bearing defense:
// it catches the obvious "for performance, let me cache the count" or
// "let me grab CurrentEpoch() from the counter" mistake at CI time
// before such a regression silently re-introduces the secondary halt.
//
// If the emitter source ever needs to import a different package for a
// legitimate reason, update the forbidden-token list deliberately —
// don't suppress this test.
func TestBoundaryEmitter_PurityGrep(t *testing.T) {
	source, err := os.ReadFile("boundary_emitter.go")
	if err != nil {
		t.Fatalf("read boundary_emitter.go: %v", err)
	}
	src := string(source)

	// Forbidden tokens. Each represents a class of non-canonical read
	// the §12.1 hidden-error pattern warns against. The list targets
	// USE-SITE syntactic forms (types, method calls, package-qualified
	// references) rather than free-form substrings — so the test does
	// not flag itself or the source's own rationale comments.
	//
	// Grouping:
	//   - Type references: "RoundCounter{", "*RoundCounter"
	//   - Method calls: ".CurrentEpoch(", ".Total("
	//   - Package-qualified: "epoch.RoundCounter", "counter.Total"
	//   - Local-cache/projection field declarations
	//
	// Code review remains the primary defense; this is the grep-level
	// safety net for the obvious "for performance, let me cache the
	// canonical count" or "let me read CurrentEpoch from the counter"
	// regression.
	forbidden := []string{
		// RoundCounter type usage (constructor, pointer, struct lit).
		"RoundCounter{",
		"*RoundCounter",
		// Package-qualified reference to the counter type or method.
		"epoch.RoundCounter",
		// Method calls on counter / similar primitives. Coupled with
		// the field name a typical caller would use.
		"counter.CurrentEpoch(",
		"counter.Total(",
		"roundCounter.CurrentEpoch(",
		"roundCounter.Total(",
		// Common forbidden field-name patterns that would indicate the
		// emitter is keeping its own non-canonical state.
		"localCounter",
		"localCache",
		"epochCache",
	}

	for _, tok := range forbidden {
		if strings.Contains(src, tok) {
			t.Errorf("boundary_emitter.go contains forbidden token %q — sub-spec §12.1 primary hidden-error pattern: emitter must read canonical DAG state ONLY via CountAncestorsByType, never RoundCounter or local counters", tok)
		}
	}

	// Affirmative check: the emitter MUST call CountAncestorsByType.
	// If this assertion ever fails, the emitter has been refactored to
	// use a different primitive — surface for review.
	if !strings.Contains(src, "CountAncestorsByType(") {
		t.Errorf("boundary_emitter.go does not call CountAncestorsByType — sub-spec §2.1 canonical trigger condition")
	}
}
