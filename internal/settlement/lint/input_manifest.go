package lint

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/tools/go/packages"
)

// SettlementInputManifestAnalyzer is the named entry point exposed by
// this package per F5 Phase 5A.4.c. It bundles together the manifest
// loader, AST extractor, and matcher into a single high-level check.
//
// The repository follows the projections/lint precedent: rather than
// expose a `golang.org/x/tools/go/analysis.Analyzer` (which is not
// vendored in this module), we expose a Check function that runs as
// part of `go test ./...` via TestLintRepository. The Analyzer struct
// here documents the analyzer's identity for tooling that wants to
// reflect on the available checks; the actual orchestration lives in
// Check().
var SettlementInputManifestAnalyzer = Analyzer{
	Name: "settlementinputmanifest",
	Doc: "validates that every read-shape AST node in internal/settlement/, " +
		"internal/escrow/, internal/ledger/, and internal/reputation/ has its " +
		"file:line declared in docs/architecture/settlement-input-manifest.yaml " +
		"under one of the `inputs:` entries' `source_locations:` lists.",
	Run: func(modulePath string) (*Report, error) {
		return Check(modulePath)
	},
}

// Analyzer is a thin wrapper that documents an analyzer's identity and
// provides a Run hook for orchestrators. Mirrors the shape of
// `golang.org/x/tools/go/analysis.Analyzer` without taking the dep.
type Analyzer struct {
	Name string
	Doc  string
	Run  func(modulePath string) (*Report, error)
}

// Check loads the manifest at <modulePath>/docs/architecture/settlement-
// input-manifest.yaml, loads the in-scope Go packages under modulePath,
// extracts every read-shape AST node, and matches them against the
// manifest. Returns a Report whose HasFailures() indicates whether the
// build should fail.
//
// modulePath is the absolute path to a Go module root. For the host
// repo this is the AetherNet repo root; for negative tests this is a
// temporary directory containing a synthetic module.
func Check(modulePath string) (*Report, error) {
	report := &Report{
		ModulePath: modulePath,
	}

	// Load the manifest. Halt on parse failure (F5 5A.4.c halt condition).
	manifestPath := filepath.Join(modulePath, ManifestPath)
	manifest, err := LoadManifest(manifestPath)
	if err != nil {
		return nil, fmt.Errorf("settlement-lint: load manifest: %w", err)
	}
	report.Manifest = manifest

	// Load the module's packages (./...). We need types so the extractor
	// can resolve write targets accurately.
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
		return nil, fmt.Errorf("settlement-lint: load packages: %w", err)
	}
	for _, pkg := range pkgs {
		for _, e := range pkg.Errors {
			report.Warnings = append(report.Warnings,
				fmt.Sprintf("package %s: load error: %v", pkg.PkgPath, e))
		}
	}

	moduleImportPath := readModuleImportPath(modulePath)

	// Extract read sites from in-scope packages.
	reads := extractReadSites(pkgs, moduleImportPath, modulePath)
	report.ReadSiteCount = len(reads)

	// Extract the broader set of "any AST activity" lines in scope,
	// used by the stale-manifest check to distinguish orphaned
	// references (line has no AST at all) from declaration-site
	// references (line has AST but no read).
	activeLines := extractActiveLines(pkgs, moduleImportPath, modulePath)

	// Match against manifest.
	undeclared, insufficient, stale := matchReadsAgainstManifest(manifest, reads, activeLines)
	report.UndeclaredReads = undeclared
	report.InsufficientPragmas = insufficient
	report.StaleManifestEntries = stale

	// Manifest-side integrity: verify every declared file:line points at
	// a real line in the source tree (line number is within the file's
	// length AND the file exists). Missing files / out-of-range lines
	// surface as warnings — this catches typos in manifest edits without
	// failing the build for an intentional cross-package reference.
	report.ManifestIntegrityIssues = checkManifestIntegrity(manifest, modulePath)

	// DerivationInputs construction-discipline check (multi-AI Item 1
	// composite, 2026-04-25). External composite-literal construction of
	// derivation.DerivationInputs is forbidden — all such construction
	// must go through derivation.NewDerivationInputs to satisfy the §2.1
	// contract validation. The unexported fields prevent field assignment
	// from outside, but a zero-value `derivation.DerivationInputs{}`
	// would still compile; this check catches that residual surface.
	report.DerivationInputsConstructions = extractDerivationInputsConstructions(pkgs, moduleImportPath, modulePath)

	return report, nil
}

// readModuleImportPath reads the module declaration from go.mod at root
// and returns the module import path. Returns "" if go.mod is missing
// or unparseable; the lint degrades gracefully (synthetic-module fallback
// matching kicks in).
func readModuleImportPath(root string) string {
	f, err := os.Open(filepath.Join(root, "go.mod"))
	if err != nil {
		return ""
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(line, "module ") {
			return strings.TrimSpace(strings.TrimPrefix(line, "module "))
		}
	}
	return ""
}

// checkManifestIntegrity verifies every declared file:line in the
// manifest resolves to a real line in the source tree. Returns one
// ManifestIntegrityIssue per problem found. Pure data-validation
// against the file system; does NOT walk the AST.
func checkManifestIntegrity(manifest *Manifest, moduleRoot string) []ManifestIntegrityIssue {
	var out []ManifestIntegrityIssue
	// Cache file line counts to avoid repeated open/close.
	lineCounts := make(map[string]int)

	for _, loc := range manifest.AllSourceLocations() {
		if loc.File == "" {
			out = append(out, ManifestIntegrityIssue{
				EntryID: loc.EntryID,
				Raw:     loc.Raw,
				Reason:  "source_locations entry has no parseable file path",
			})
			continue
		}
		if loc.Line == 0 {
			out = append(out, ManifestIntegrityIssue{
				EntryID: loc.EntryID,
				File:    loc.File,
				Raw:     loc.Raw,
				Reason:  "source_locations entry has no parseable line number",
			})
			continue
		}
		abs := filepath.Join(moduleRoot, filepath.FromSlash(loc.File))
		count, ok := lineCounts[abs]
		if !ok {
			n, err := countLines(abs)
			if err != nil {
				out = append(out, ManifestIntegrityIssue{
					EntryID: loc.EntryID,
					File:    loc.File,
					Line:    loc.Line,
					Reason:  fmt.Sprintf("manifest references file %q which is not readable: %v", loc.File, err),
				})
				lineCounts[abs] = -1
				continue
			}
			count = n
			lineCounts[abs] = count
		}
		if count < 0 {
			continue // already reported the file as unreadable
		}
		if loc.Line > count {
			out = append(out, ManifestIntegrityIssue{
				EntryID: loc.EntryID,
				File:    loc.File,
				Line:    loc.Line,
				Reason: fmt.Sprintf(
					"manifest references line %d but file %q is only %d lines long",
					loc.Line, loc.File, count),
			})
		}
	}
	return out
}

func countLines(path string) (int, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024)
	n := 0
	for scanner.Scan() {
		n++
	}
	return n, scanner.Err()
}

// Report is the composed output of a settlement-lint run.
type Report struct {
	// ModulePath is the module root the lint was run against.
	ModulePath string

	// Manifest is the parsed manifest (informational; Format includes
	// summary statistics).
	Manifest *Manifest

	// ReadSiteCount is the total number of read-shape AST nodes the
	// extractor found in scope (informational).
	ReadSiteCount int

	// UndeclaredReads lists in-scope reads not declared in the manifest
	// and not suppressed by a valid pragma.
	UndeclaredReads []UndeclaredRead

	// InsufficientPragmas lists pragmas whose reason failed validation.
	InsufficientPragmas []InsufficientPragma

	// StaleManifestEntries lists manifest source_locations whose
	// file:line did not match any extracted read site (manifest-side
	// drift detection).
	StaleManifestEntries []StaleManifestEntry

	// ManifestIntegrityIssues lists declared source_locations that
	// reference unreadable files or out-of-range line numbers.
	ManifestIntegrityIssues []ManifestIntegrityIssue

	// DerivationInputsConstructions lists illegal composite-literal
	// constructions of derivation.DerivationInputs found outside the
	// derivation package. Per multi-AI Item 1 composite (2026-04-25):
	// external callers must use derivation.NewDerivationInputs.
	DerivationInputsConstructions []DerivationInputsConstruction

	// Warnings are non-failure diagnostics (package load errors, etc.).
	Warnings []string
}

// UndeclaredRead is an in-scope read whose file:line is missing from
// the manifest (and not suppressed by a valid pragma).
type UndeclaredRead struct {
	File        string
	Line        int
	Kind        string
	Detail      string
	PackagePath string
}

// InsufficientPragma describes a rejected suppression pragma.
type InsufficientPragma struct {
	File      string
	Line      int
	Reason    string
	CharCount int
	WordCount int
}

// StaleManifestEntry describes a manifest source_locations entry whose
// file:line did not match any extracted read site.
type StaleManifestEntry struct {
	File    string
	Line    int
	EntryID string
}

// ManifestIntegrityIssue describes a manifest source_locations entry
// that fails basic file-system validation (file missing or line number
// out of range).
type ManifestIntegrityIssue struct {
	EntryID string
	File    string
	Line    int
	Raw     string
	Reason  string
}

// HasFailures reports whether the Report contains any finding that
// must fail the build. Stale manifest entries and undeclared reads are
// failures. Manifest-integrity issues are failures (they indicate the
// manifest is structurally invalid). Warnings alone do NOT flip
// HasFailures.
//
// Per F5 5A.4.c: the lint is a CI gate; any of (undeclared, insufficient,
// stale, integrity) failing means the manifest and source code have
// diverged and must be reconciled before merging.
func (r *Report) HasFailures() bool {
	return len(r.UndeclaredReads) > 0 ||
		len(r.InsufficientPragmas) > 0 ||
		len(r.StaleManifestEntries) > 0 ||
		len(r.ManifestIntegrityIssues) > 0 ||
		len(r.DerivationInputsConstructions) > 0
}

// Format renders a human-readable failure report. Stable formatting so
// the output is diffable across lint runs.
func (r *Report) Format() string {
	var b strings.Builder
	for _, u := range r.UndeclaredReads {
		b.WriteString(formatUndeclared(u))
		b.WriteString("\n")
	}
	for _, s := range r.InsufficientPragmas {
		b.WriteString(formatInsufficient(s))
		b.WriteString("\n")
	}
	for _, s := range r.StaleManifestEntries {
		b.WriteString(formatStaleManifest(s))
		b.WriteString("\n")
	}
	for _, i := range r.ManifestIntegrityIssues {
		b.WriteString(formatIntegrityIssue(i))
		b.WriteString("\n")
	}
	for _, c := range r.DerivationInputsConstructions {
		b.WriteString(formatDerivationInputsConstruction(c))
		b.WriteString("\n")
	}
	if len(r.Warnings) > 0 {
		b.WriteString("\nsettlement/lint: WARNINGS (non-failure)\n")
		for _, w := range r.Warnings {
			b.WriteString("  - ")
			b.WriteString(w)
			b.WriteString("\n")
		}
	}
	return b.String()
}

func formatIntegrityIssue(i ManifestIntegrityIssue) string {
	return fmt.Sprintf(`settlement/lint: MANIFEST INTEGRITY FAILURE
  Entry:    %s
  File:     %s
  Line:     %d
  Raw:      %s
  Diagnosis: %s
  Remediation: edit docs/architecture/settlement-input-manifest.yaml to fix or remove
               the entry's source_locations reference. The manifest is the audit baseline
               for F5 Phase 5D regression detection — broken references defeat the audit.
`, i.EntryID, i.File, i.Line, i.Raw, i.Reason)
}
