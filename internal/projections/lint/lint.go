package lint

import (
	"fmt"
	"strings"

	"golang.org/x/tools/go/packages"
)

// Check loads the module rooted at modulePath, runs the extractor, matcher,
// and testref check, and returns a Report. The Report's HasFailures
// method reports whether any Canonical finding warrants failing the build.
//
// Warnings (non-failure) are included in Report.Warnings and surfaced but
// do not flip HasFailures. Examples: interface-indirect persistence fields
// (D3 cost-gate deferral), files excluded under default build tags, a
// *Projection() constructor that returns a non-literal expression.
func Check(modulePath string) (*Report, error) {
	cfg := &packages.Config{
		Dir: modulePath,
		Mode: packages.NeedName |
			packages.NeedFiles |
			packages.NeedCompiledGoFiles |
			packages.NeedImports |
			packages.NeedTypes |
			packages.NeedTypesInfo |
			packages.NeedSyntax |
			packages.NeedModule,
	}
	pkgs, err := packages.Load(cfg, "./...")
	if err != nil {
		return nil, fmt.Errorf("lint: load packages: %w", err)
	}
	// Surface package-level load errors as warnings so a broken sub-package
	// does not silently prevent lint coverage of healthy ones.
	report := &Report{}
	for _, pkg := range pkgs {
		for _, e := range pkg.Errors {
			report.Warnings = append(report.Warnings,
				fmt.Sprintf("package %s: load error: %v", pkg.PkgPath, e))
		}
	}
	// Step-2 commits will hang this together in follow-up commits;
	// skeleton returns an empty Report so the test harness compiles.
	return report, nil
}

// Report is the composed output of a lint run.
type Report struct {
	// UnregisteredTypes lists suspect types (by the D3 heuristic) that are
	// not covered by a registered projection entry.
	UnregisteredTypes []SuspectType

	// MissingRefs lists Canonical registry entries whose IntegrationTestRef
	// does not resolve to a Test function in the codebase.
	MissingRefs []MissingRef

	// InsufficientSuppressions lists suppression pragmas whose reason did
	// not meet the D4 minimum (≥20 chars, ≥3 words).
	InsufficientSuppressions []InsufficientSuppression

	// Warnings are non-failure diagnostics: interface-indirect persistence
	// (D3 cost-gate deferral), tagged-out files, non-literal *Projection()
	// return values, package load errors.
	Warnings []string
}

// SuspectType is a type flagged by the D3 heuristic that is not registered.
type SuspectType struct {
	PackagePath string
	TypeName    string
	File        string
	Line        int
	Evidence    string // e.g. "has *badger.DB field; writer methods: Save, Delete"
}

// MissingRef is a Canonical registry entry with a broken IntegrationTestRef.
type MissingRef struct {
	EntryName          string
	DeclaredRef        string
	SourceFile         string
	SourceLine         int
	ResolutionFailure  string
}

// InsufficientSuppression describes a rejected suppression pragma.
type InsufficientSuppression struct {
	File     string
	Line     int
	Reason   string
	CharCount int
	WordCount int
}

// HasFailures reports whether the Report contains any finding that must
// fail the build. Warnings alone do not flip this.
func (r *Report) HasFailures() bool {
	return len(r.UnregisteredTypes) > 0 ||
		len(r.MissingRefs) > 0 ||
		len(r.InsufficientSuppressions) > 0
}

// Format renders a human-readable failure report. Stable formatting so the
// output is diffable across lint runs.
func (r *Report) Format() string {
	var b strings.Builder
	for _, s := range r.UnregisteredTypes {
		b.WriteString(formatUnregistered(s))
		b.WriteString("\n")
	}
	for _, m := range r.MissingRefs {
		b.WriteString(formatMissingRef(m))
		b.WriteString("\n")
	}
	for _, s := range r.InsufficientSuppressions {
		b.WriteString(formatInsufficient(s))
		b.WriteString("\n")
	}
	if len(r.Warnings) > 0 {
		b.WriteString("\nprojections/lint: WARNINGS (non-failure)\n")
		for _, w := range r.Warnings {
			b.WriteString("  - ")
			b.WriteString(w)
			b.WriteString("\n")
		}
	}
	return b.String()
}

func formatUnregistered(s SuspectType) string {
	return fmt.Sprintf(`projections/lint: UNREGISTERED STORE
  Type:         %s.%s
  File:         %s
  Line:         %d
  Evidence:     %s
  Remediation:  EITHER
                  (a) register this type. Add a %s.%sProjection() constructor
                      returning a literal projections.CanonicalProjection{...}
                      and wire it via projReg.MustRegister in cmd/node/main.go.
                      See internal/epoch/, internal/escrow/, internal/ledger/,
                      internal/ocs/, internal/reputation/, internal/taskverification/
                      for the step-2 pattern.
                  OR
                  (b) suppress the lint. Add immediately above the type decl:
                          // projections:lint ignore "<reason>"
                      The reason must be at least 20 characters AND at least 3
                      words, and must describe why the type is NOT
                      consensus-adjacent in terms a reviewer can evaluate.
  Heuristic:    this is the CI heuristic from plan §9.3, an approximation of
                the principle-level definition (any durable state projection
                whose outputs can affect canonical protocol behavior).
  Dynamic ok:   this lint relies on registration via a *Projection() constructor
                with a literal CanonicalProjection{...} struct. Dynamic
                registration patterns defeat the static analysis and require
                code-review confirmation of registration completeness.
`, s.PackagePath, s.TypeName, s.File, s.Line, s.Evidence, s.PackagePath, s.TypeName)
}

func formatMissingRef(m MissingRef) string {
	return fmt.Sprintf(`projections/lint: MISSING INTEGRATION TEST
  Entry:        %s
  Declared ref: %s
  Decl file:    %s:%d
  Resolution:   %s
  Required:     EITHER create the test function at the declared path,
                OR update the Projection() constructor to point at an
                existing test symbol. The IntegrationTestRef must resolve
                to a func(*testing.T) in the codebase (PR-3 invariant).
`, m.EntryName, m.DeclaredRef, m.SourceFile, m.SourceLine, m.ResolutionFailure)
}

func formatInsufficient(s InsufficientSuppression) string {
	return fmt.Sprintf(`projections/lint: INSUFFICIENT SUPPRESSION JUSTIFICATION
  File:         %s
  Line:         %d
  Got reason:   %q (%d chars, %d words)
  Required:     at least 20 characters AND at least 3 whitespace-separated words
  Required:     replace with an actionable justification a future reviewer can
                evaluate (e.g. "this is a test fixture for the lint itself; the
                matched type is intentionally synthetic")
`, s.File, s.Line, s.Reason, s.CharCount, s.WordCount)
}
