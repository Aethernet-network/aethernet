package lint

import (
	"strings"
	"testing"
)

// TestLint_FlagsExternalDerivationInputsConstruction is the deliberate
// negative test for the multi-AI Item 1 composite (2026-04-25)
// construction-discipline check. A synthetic module contains a
// `package derivation` definition with a DerivationInputs type AND a
// `package settlement` source file that constructs it via composite
// literal (the only syntactic shape the unexported fields permit:
// `derivation.DerivationInputs{}`). The lint must flag the construction.
//
// This is the proof that the analyzer mechanism works end-to-end:
// loader → extractor (type-resolved, not syntactic) → matcher →
// diagnostic.
func TestLint_FlagsExternalDerivationInputsConstruction(t *testing.T) {
	dir := t.TempDir()

	// Synthetic derivation package with a DerivationInputs type.
	// The package import path resolves to
	// example.com/m/internal/settlement/derivation, which matches the
	// package-suffix check in derivation_inputs_construction.go.
	derivationGo := `package derivation

type DerivationInputs struct {
	x int
}
`
	// Synthetic settlement package that constructs DerivationInputs
	// via composite literal — the violation the lint should flag.
	settlementGo := `package settlement

import "example.com/m/internal/settlement/derivation"

func Construct() derivation.DerivationInputs {
	return derivation.DerivationInputs{}
}
`
	// Manifest must be non-empty (zero-inputs is a halt condition per
	// the loader). Use a benign declared read to satisfy the loader.
	syntheticManifest := `inputs:
  - id: declared
    source: synthetic
    source_locations:
      - internal/settlement/example.go:5
`
	writeSyntheticModule(t, dir, "example.com/m",
		map[string]string{
			"internal/settlement/derivation/inputs.go": derivationGo,
			"internal/settlement/example.go":           settlementGo,
		},
		syntheticManifest, "")

	report, err := Check(dir)
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if len(report.DerivationInputsConstructions) == 0 {
		t.Fatalf("expected DerivationInputsConstruction finding for external composite literal; got none.\nReport: %+v",
			report)
	}
	if !report.HasFailures() {
		t.Fatalf("expected HasFailures=true when a DerivationInputsConstruction is present")
	}
	// Verify the violation locates the settlement file's construction
	// site, NOT the derivation file's type definition.
	c := report.DerivationInputsConstructions[0]
	if !strings.HasSuffix(c.File, "internal/settlement/example.go") {
		t.Errorf("flagged file = %q, want internal/settlement/example.go", c.File)
	}
	if c.Line != 6 {
		t.Errorf("flagged line = %d, want 6 (the `derivation.DerivationInputs{}` literal)", c.Line)
	}
	// Verify the diagnostic carries the required elements.
	diag := report.Format()
	mustContain := []string{
		"ILLEGAL DerivationInputs CONSTRUCTION",
		"internal/settlement/example.go:6",
		"derivation.NewDerivationInputs",
		"§2.1 contract validation",
	}
	for _, want := range mustContain {
		if !strings.Contains(diag, want) {
			t.Errorf("diagnostic missing %q\n---\n%s", want, diag)
		}
	}
}

// TestLint_AllowsInDerivationPackageConstruction verifies the
// in-package-construction allowance: a composite literal of
// DerivationInputs INSIDE the derivation package itself is NOT flagged.
// This is the design allowance for in-package tests (which need to
// construct edge cases the constructor would reject) and for the
// constructor's own struct-literal return.
func TestLint_AllowsInDerivationPackageConstruction(t *testing.T) {
	dir := t.TempDir()

	// Derivation package defines DerivationInputs AND constructs it
	// internally — both should be allowed.
	derivationGo := `package derivation

type DerivationInputs struct {
	x int
}

func New() DerivationInputs {
	return DerivationInputs{}
}
`
	syntheticManifest := `inputs:
  - id: declared
    source: synthetic
    source_locations:
      - internal/settlement/derivation/inputs.go:3
`
	writeSyntheticModule(t, dir, "example.com/m",
		map[string]string{
			"internal/settlement/derivation/inputs.go": derivationGo,
		},
		syntheticManifest, "")

	report, err := Check(dir)
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if len(report.DerivationInputsConstructions) > 0 {
		t.Fatalf("in-derivation-package construction should NOT be flagged; got: %+v",
			report.DerivationInputsConstructions)
	}
}

// TestReport_DerivationInputsConstructionFlipsFailure verifies a single
// DerivationInputsConstruction finding causes HasFailures to return true.
func TestReport_DerivationInputsConstructionFlipsFailure(t *testing.T) {
	r := &Report{
		DerivationInputsConstructions: []DerivationInputsConstruction{
			{File: "internal/settlement/foo.go", Line: 42, PackagePath: "example.com/m/internal/settlement"},
		},
	}
	if !r.HasFailures() {
		t.Fatalf("Report with one DerivationInputsConstruction must HasFailures")
	}
	out := r.Format()
	mustContain := []string{
		"ILLEGAL DerivationInputs CONSTRUCTION",
		"internal/settlement/foo.go:42",
		"derivation.NewDerivationInputs",
	}
	for _, want := range mustContain {
		if !strings.Contains(out, want) {
			t.Fatalf("Format output missing %q\n---\n%s", want, out)
		}
	}
}
