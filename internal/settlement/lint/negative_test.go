package lint

import (
	"os"
	"strings"
	"testing"
)

// TestLint_FailsOnUndeclaredRead is the deliberate negative test
// required by F5 5A.4.c (Gate 5A.1 Finding 5): a synthetic module
// contains a settlement-package source file with two read sites; the
// manifest declares only one. The lint must flag the undeclared read.
//
// This is the proof that the analyzer mechanism works end-to-end:
// loader → extractor → matcher → diagnostic.
func TestLint_FailsOnUndeclaredRead(t *testing.T) {
	dir := t.TempDir()
	// Two read sites in the synthetic settlement file. The manifest
	// declares the first (line 7 — payload.TaskID) but NOT the second
	// (line 8 — payload.WorkerID). Lint must flag the second.
	syntheticGo := `package settlement

type Payload struct {
	TaskID   string
	WorkerID string
}

func Read(p *Payload) (string, string) {
	a := p.TaskID
	b := p.WorkerID
	return a, b
}
`
	syntheticManifest := `inputs:
  - id: declared_field
    source: synthetic
    source_locations:
      - internal/settlement/example.go:9
`
	writeSyntheticModule(t, dir, "example.com/m",
		map[string]string{"internal/settlement/example.go": syntheticGo},
		syntheticManifest, "")

	report, err := Check(dir)
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if !report.HasFailures() {
		t.Fatalf("expected lint to flag undeclared read; got no failures.\n%s", report.Format())
	}
	if len(report.UndeclaredReads) == 0 {
		t.Fatalf("expected at least one UndeclaredRead; got 0\nReport: %+v", report)
	}
	// Verify the SECOND field read (line 10 in the synthetic file —
	// `b := p.WorkerID`) was flagged. The first read (line 9) is the
	// one we declared.
	var sawWorkerID bool
	for _, u := range report.UndeclaredReads {
		if u.Detail == "WorkerID" {
			sawWorkerID = true
			if u.Line != 10 {
				t.Errorf("WorkerID read line: want 10, got %d", u.Line)
			}
		}
	}
	if !sawWorkerID {
		t.Errorf("expected to flag WorkerID read; flagged reads were: %+v", report.UndeclaredReads)
	}
	// Verify the diagnostic carries the required elements.
	diag := report.Format()
	mustContain := []string{
		"UNDECLARED SETTLEMENT READ",
		"internal/settlement/example.go",
		"WorkerID",
		"settlement-input-manifest.yaml",
		"settlement:lint ignore",
	}
	for _, want := range mustContain {
		if !strings.Contains(diag, want) {
			t.Errorf("diagnostic missing %q\n---\n%s", want, diag)
		}
	}
}

// TestLint_PragmaOnPrecedingLineSuppresses verifies that a pragma
// placed on the line immediately before the read is also recognized.
// This matches the convention used by other Go lints that allow
// contributors to place a `// foo:lint ignore` comment immediately
// above the offending code.
func TestLint_PragmaOnPrecedingLineSuppresses(t *testing.T) {
	dir := t.TempDir()
	syntheticGo := `package settlement

type Payload struct {
	TaskID   string
	WorkerID string
}

func Read(p *Payload) (string, string) {
	a := p.TaskID
	// settlement:lint ignore "preceding-line pragma form; helper-only logic that does not influence canonical payout derivation"
	b := p.WorkerID
	return a, b
}
`
	syntheticManifest := `inputs:
  - id: declared_field
    source: synthetic
    source_locations:
      - internal/settlement/example.go:9
`
	writeSyntheticModule(t, dir, "example.com/m",
		map[string]string{"internal/settlement/example.go": syntheticGo},
		syntheticManifest, "")

	report, err := Check(dir)
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if report.HasFailures() {
		t.Fatalf("expected preceding-line pragma to suppress the lint; got failures:\n%s", report.Format())
	}
}

// TestLint_PragmaSuppressesUndeclared verifies that an inline pragma
// with a sufficient justification suppresses the lint for that line.
func TestLint_PragmaSuppressesUndeclared(t *testing.T) {
	dir := t.TempDir()
	syntheticGo := `package settlement

type Payload struct {
	TaskID   string
	WorkerID string
}

func Read(p *Payload) (string, string) {
	a := p.TaskID
	b := p.WorkerID // settlement:lint ignore "synthetic test fixture; this read is intentionally undeclared to verify the pragma path"
	return a, b
}
`
	syntheticManifest := `inputs:
  - id: declared_field
    source: synthetic
    source_locations:
      - internal/settlement/example.go:9
`
	writeSyntheticModule(t, dir, "example.com/m",
		map[string]string{"internal/settlement/example.go": syntheticGo},
		syntheticManifest, "")

	report, err := Check(dir)
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if report.HasFailures() {
		t.Fatalf("expected pragma to suppress the lint; got failures:\n%s", report.Format())
	}
}

// TestLint_InsufficientPragmaIsFlagged verifies that a pragma whose
// reason is too short OR has too few words is rejected and the rejection
// is recorded as InsufficientPragma (not a silent suppression).
func TestLint_InsufficientPragmaIsFlagged(t *testing.T) {
	dir := t.TempDir()
	syntheticGo := `package settlement

type Payload struct {
	TaskID   string
	WorkerID string
}

func Read(p *Payload) (string, string) {
	a := p.TaskID
	b := p.WorkerID // settlement:lint ignore "too short"
	return a, b
}
`
	syntheticManifest := `inputs:
  - id: declared_field
    source: synthetic
    source_locations:
      - internal/settlement/example.go:9
`
	writeSyntheticModule(t, dir, "example.com/m",
		map[string]string{"internal/settlement/example.go": syntheticGo},
		syntheticManifest, "")

	report, err := Check(dir)
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if !report.HasFailures() {
		t.Fatalf("expected insufficient pragma to fail the lint; got no failures.\n%s", report.Format())
	}
	if len(report.InsufficientPragmas) == 0 {
		t.Fatalf("expected at least one InsufficientPragma; got 0\nReport: %+v", report)
	}
	insuff := report.InsufficientPragmas[0]
	if insuff.Reason != "too short" {
		t.Errorf("Reason: want 'too short', got %q", insuff.Reason)
	}
}

// TestLint_StaleManifestEntryFlagged verifies the manifest-side drift
// check: a manifest entry pointing at a line in scope that has NO AST
// activity at all (truly orphaned reference) flags as stale.
//
// Note: lines with AST activity but no read shape (e.g. struct field
// declarations, const definitions) are NOT stale per the manifest
// convention — source_locations may legitimately point at definition
// sites. This test exercises the truly-orphaned case (a blank line at
// the end of the file).
func TestLint_StaleManifestEntryFlagged(t *testing.T) {
	dir := t.TempDir()
	syntheticGo := `package settlement

type Payload struct {
	TaskID string
}

func Read(p *Payload) string {
	a := p.TaskID
	return a
}

// trailing comment lines below have no AST activity
// (line 13 is this very comment, line 14 is blank)

`
	// Manifest declares line 8 (the actual read) and line 14 (a blank
	// line with no AST activity — truly orphaned reference).
	syntheticManifest := `inputs:
  - id: real_field
    source: synthetic
    source_locations:
      - internal/settlement/example.go:8
  - id: phantom_input
    source: nothing
    source_locations:
      - internal/settlement/example.go:14
`
	writeSyntheticModule(t, dir, "example.com/m",
		map[string]string{"internal/settlement/example.go": syntheticGo},
		syntheticManifest, "")

	report, err := Check(dir)
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if len(report.StaleManifestEntries) == 0 {
		t.Fatalf("expected stale manifest entry for orphaned reference; got none.\nReport: %+v", report)
	}
	var sawPhantom bool
	for _, s := range report.StaleManifestEntries {
		if s.EntryID == "phantom_input" {
			sawPhantom = true
		}
	}
	if !sawPhantom {
		t.Errorf("expected phantom_input flagged as stale; got: %+v", report.StaleManifestEntries)
	}
}

// TestLint_IntegrityCatchesOutOfRangeLine verifies the manifest-
// integrity check fires when a declared line is past the file's end.
func TestLint_IntegrityCatchesOutOfRangeLine(t *testing.T) {
	dir := t.TempDir()
	syntheticGo := `package settlement

type Payload struct {
	TaskID string
}
`
	// File is 5 lines long. Manifest claims line 9999.
	syntheticManifest := `inputs:
  - id: bogus
    source: synthetic
    source_locations:
      - internal/settlement/example.go:9999
`
	writeSyntheticModule(t, dir, "example.com/m",
		map[string]string{"internal/settlement/example.go": syntheticGo},
		syntheticManifest, "")

	report, err := Check(dir)
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if len(report.ManifestIntegrityIssues) == 0 {
		t.Fatalf("expected manifest-integrity issue for out-of-range line; got none.\nReport: %+v", report)
	}
	if !report.HasFailures() {
		t.Fatalf("expected HasFailures=true for integrity issue")
	}
}

// TestLint_TestdataFixtures runs the analyzer against the
// testdata/ fixture pair (compliant + non-compliant) by copying the
// fixture files into a temp directory, running Check, and asserting:
//   - The compliant read at compliant.go:14 is NOT flagged.
//   - The non-compliant read at noncompliant.go:16 IS flagged.
//
// This is the testdata-driven leg of the negative test (the inline-
// synthetic-module leg is TestLint_FailsOnUndeclaredRead above).
func TestLint_TestdataFixtures(t *testing.T) {
	dir := t.TempDir()

	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	fixtureRoot := wd + "/testdata"

	// Copy testdata files into the temp dir, renaming .go.txt -> .go.
	mustCopyFixture(t, fixtureRoot+"/src/internal/settlement/compliant.go.txt",
		dir+"/internal/settlement/compliant.go")
	mustCopyFixture(t, fixtureRoot+"/src/internal/settlement/noncompliant.go.txt",
		dir+"/internal/settlement/noncompliant.go")
	mustCopyFixture(t, fixtureRoot+"/docs/architecture/settlement-input-manifest.yaml",
		dir+"/docs/architecture/settlement-input-manifest.yaml")

	// Minimal go.mod so packages.Load works without `replace`. The fixture
	// files import nothing, so we don't need the host module.
	if err := os.WriteFile(dir+"/go.mod", []byte("module example.com/fixture\n\ngo 1.25\n"), 0o644); err != nil {
		t.Fatalf("write go.mod: %v", err)
	}

	report, err := Check(dir)
	if err != nil {
		t.Fatalf("Check: %v", err)
	}

	// Compliant read at compliant.go:14 must NOT be in undeclared.
	for _, u := range report.UndeclaredReads {
		if u.File == "internal/settlement/compliant.go" && u.Line == 14 {
			t.Errorf("compliant read at compliant.go:14 should not be flagged: %+v", u)
		}
	}

	// Non-compliant read at noncompliant.go:16 (the WorkerID line) must
	// be flagged. The fixture file's `b := p.WorkerID` is at line 16.
	var sawWorkerID bool
	for _, u := range report.UndeclaredReads {
		if u.File == "internal/settlement/noncompliant.go" && u.Detail == "WorkerID" {
			sawWorkerID = true
		}
	}
	if !sawWorkerID {
		t.Errorf("expected WorkerID read in noncompliant.go to be flagged; got: %+v", report.UndeclaredReads)
	}
}

// mustCopyFixture copies src to dst, creating intermediate directories.
func mustCopyFixture(t *testing.T, src, dst string) {
	t.Helper()
	if err := os.MkdirAll(filepathDir(dst), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dst, err)
	}
	data, err := os.ReadFile(src)
	if err != nil {
		t.Fatalf("read %s: %v", src, err)
	}
	if err := os.WriteFile(dst, data, 0o644); err != nil {
		t.Fatalf("write %s: %v", dst, err)
	}
}

// filepathDir is a tiny re-export to avoid an extra import in this file.
func filepathDir(p string) string {
	for i := len(p) - 1; i >= 0; i-- {
		if p[i] == '/' {
			return p[:i]
		}
	}
	return "."
}

// TestLint_OutOfScopePackagesAreNotScanned verifies that reads in
// internal/staking, internal/fees, internal/identity are NOT flagged,
// per the F5 5A.4.c scope boundary.
func TestLint_OutOfScopePackagesAreNotScanned(t *testing.T) {
	dir := t.TempDir()
	// Two files: one in scope (settlement) declared in manifest; one
	// out of scope (staking) NOT declared. Lint must NOT flag the
	// staking read.
	settlementGo := `package settlement

type Payload struct{ TaskID string }

func Read(p *Payload) string { return p.TaskID }
`
	stakingGo := `package staking

type Stake struct{ Amount uint64 }

func Read(s *Stake) uint64 { return s.Amount }
`
	syntheticManifest := `inputs:
  - id: real_field
    source: synthetic
    source_locations:
      - internal/settlement/example.go:5
`
	writeSyntheticModule(t, dir, "example.com/m",
		map[string]string{
			"internal/settlement/example.go": settlementGo,
			"internal/staking/example.go":    stakingGo,
		},
		syntheticManifest, "")

	report, err := Check(dir)
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	for _, u := range report.UndeclaredReads {
		if strings.Contains(u.File, "internal/staking") {
			t.Errorf("staking package read should NOT be flagged (out of scope): %+v", u)
		}
	}
}
