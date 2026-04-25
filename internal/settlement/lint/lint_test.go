package lint

import (
	"strings"
	"testing"
)

// TestReport_EmptyIsHealthy verifies the HasFailures contract: a Report
// with no entries must return false, and Format returns an empty string.
func TestReport_EmptyIsHealthy(t *testing.T) {
	r := &Report{}
	if r.HasFailures() {
		t.Fatalf("empty Report.HasFailures must be false")
	}
	if got := r.Format(); got != "" {
		t.Fatalf("empty Report.Format must be empty string, got %q", got)
	}
}

// TestReport_AnyUndeclaredFlipsFailure verifies a single undeclared
// read causes HasFailures to return true.
func TestReport_AnyUndeclaredFlipsFailure(t *testing.T) {
	r := &Report{
		UndeclaredReads: []UndeclaredRead{
			{File: "internal/settlement/foo.go", Line: 42, Kind: "selector", Detail: "Field"},
		},
	}
	if !r.HasFailures() {
		t.Fatalf("Report with one UndeclaredRead must HasFailures")
	}
}

// TestReport_StaleFlipsFailure verifies a stale-manifest entry causes
// HasFailures to return true.
func TestReport_StaleFlipsFailure(t *testing.T) {
	r := &Report{
		StaleManifestEntries: []StaleManifestEntry{
			{File: "internal/settlement/foo.go", Line: 99, EntryID: "phantom_input"},
		},
	}
	if !r.HasFailures() {
		t.Fatalf("Report with one StaleManifestEntry must HasFailures")
	}
}

// TestReport_IntegrityFlipsFailure verifies a manifest-integrity issue
// causes HasFailures to return true.
func TestReport_IntegrityFlipsFailure(t *testing.T) {
	r := &Report{
		ManifestIntegrityIssues: []ManifestIntegrityIssue{
			{EntryID: "x", File: "internal/settlement/missing.go", Line: 1, Reason: "file not readable"},
		},
	}
	if !r.HasFailures() {
		t.Fatalf("Report with one ManifestIntegrityIssue must HasFailures")
	}
}

// TestReport_FormatNamesLocationAndRemediation verifies the
// UndeclaredRead diagnostic includes the file:line, kind, detail, and
// both remediation paths (declare in manifest OR suppress with pragma).
func TestReport_FormatNamesLocationAndRemediation(t *testing.T) {
	r := &Report{
		UndeclaredReads: []UndeclaredRead{
			{
				File:        "internal/settlement/example.go",
				Line:        99,
				Kind:        "selector",
				Detail:      "MysteryField",
				PackagePath: "github.com/Aethernet-network/aethernet/internal/settlement",
			},
		},
	}
	out := r.Format()
	mustContain := []string{
		"UNDECLARED SETTLEMENT READ",
		"internal/settlement/example.go:99",
		"selector",
		"MysteryField",
		"settlement-input-manifest.yaml",
		"settlement:lint ignore",
		"at least 20 characters",
		"at least 3",
	}
	for _, want := range mustContain {
		if !strings.Contains(out, want) {
			t.Fatalf("Format output missing %q\n---\n%s", want, out)
		}
	}
}

// TestReport_InsufficientPragmaDiagnostic verifies the rejection shape
// for too-short pragma reasons.
func TestReport_InsufficientPragmaDiagnostic(t *testing.T) {
	r := &Report{
		InsufficientPragmas: []InsufficientPragma{
			{
				File:      "internal/settlement/example.go",
				Line:      7,
				Reason:    "false positive",
				CharCount: 14,
				WordCount: 2,
			},
		},
	}
	out := r.Format()
	mustContain := []string{
		"INSUFFICIENT SUPPRESSION JUSTIFICATION",
		"internal/settlement/example.go",
		`"false positive" (14 chars, 2 words)`,
		"at least 20 characters",
		"at least 3 whitespace-separated words",
	}
	for _, want := range mustContain {
		if !strings.Contains(out, want) {
			t.Fatalf("Format output missing %q\n---\n%s", want, out)
		}
	}
}

// TestReport_StaleManifestDiagnostic verifies the manifest-side drift
// diagnostic shape.
func TestReport_StaleManifestDiagnostic(t *testing.T) {
	r := &Report{
		StaleManifestEntries: []StaleManifestEntry{
			{
				File:    "internal/settlement/foo.go",
				Line:    100,
				EntryID: "phantom_input",
			},
		},
	}
	out := r.Format()
	mustContain := []string{
		"STALE MANIFEST ENTRY",
		"internal/settlement/foo.go:100",
		"phantom_input",
		"settlement-input-manifest.yaml",
		"target_classification: removed",
	}
	for _, want := range mustContain {
		if !strings.Contains(out, want) {
			t.Fatalf("Format output missing %q\n---\n%s", want, out)
		}
	}
}

// TestReport_WarningsAreNonFailure verifies warnings don't flip
// HasFailures.
func TestReport_WarningsAreNonFailure(t *testing.T) {
	r := &Report{
		Warnings: []string{"package foo: load error: xyz"},
	}
	if r.HasFailures() {
		t.Fatalf("warnings-only Report.HasFailures must be false")
	}
	out := r.Format()
	if !strings.Contains(out, "WARNINGS (non-failure)") {
		t.Fatalf("warnings should format with (non-failure) tag\n%s", out)
	}
	if !strings.Contains(out, "load error: xyz") {
		t.Fatalf("warning text should appear in Format output\n%s", out)
	}
}

// TestAnalyzer_NameAndDoc verifies the analyzer's identity surface.
func TestAnalyzer_NameAndDoc(t *testing.T) {
	if SettlementInputManifestAnalyzer.Name == "" {
		t.Fatal("Analyzer.Name must be non-empty")
	}
	if SettlementInputManifestAnalyzer.Doc == "" {
		t.Fatal("Analyzer.Doc must be non-empty")
	}
	if SettlementInputManifestAnalyzer.Run == nil {
		t.Fatal("Analyzer.Run must be non-nil")
	}
}

// TestManifestLoader_ParsesRealManifest is a focused parser smoke test
// against the real settlement-input-manifest.yaml. Verifies the loader
// extracts at least the sections the lint depends on.
func TestManifestLoader_ParsesRealManifest(t *testing.T) {
	manifestPath := moduleRoot(t) + "/" + ManifestPath
	m, err := LoadManifest(manifestPath)
	if err != nil {
		t.Fatalf("LoadManifest: %v", err)
	}
	if len(m.Inputs) == 0 {
		t.Fatalf("expected at least one input; got 0")
	}
	// Spot-check a known entry.
	var sawRoundVotes bool
	for _, e := range m.Inputs {
		if e.ID == "round_votes" {
			sawRoundVotes = true
			if len(e.SourceLocations) == 0 {
				t.Errorf("round_votes entry has empty source_locations")
			}
		}
	}
	if !sawRoundVotes {
		t.Errorf("expected to find round_votes entry; got %d entries with no match", len(m.Inputs))
	}
}

// TestManifestLoader_DeclaredLocations spot-checks the declared-set
// helper used by the matcher.
func TestManifestLoader_DeclaredLocations(t *testing.T) {
	manifestPath := moduleRoot(t) + "/" + ManifestPath
	m, err := LoadManifest(manifestPath)
	if err != nil {
		t.Fatalf("LoadManifest: %v", err)
	}
	declared := m.DeclaredLocations()
	if len(declared) == 0 {
		t.Fatal("expected non-empty declared-locations index")
	}
	// payload_task_id declares verification_consensus_settler.go:140.
	key := "internal/settlement/verification_consensus_settler.go:140"
	if _, ok := declared[key]; !ok {
		t.Errorf("expected %q in declared set; not found", key)
	}
}

// TestManifestLoader_RangeExpansion verifies the loader expands
// "file.go:280-289" into one entry per line in the range.
func TestManifestLoader_RangeExpansion(t *testing.T) {
	dir := t.TempDir()
	body := `inputs:
  - id: range_test
    source: synthetic
    source_locations:
      - some/file.go:10-12
`
	if err := writeFile(dir+"/m.yaml", body); err != nil {
		t.Fatalf("write: %v", err)
	}
	m, err := LoadManifest(dir + "/m.yaml")
	if err != nil {
		t.Fatalf("LoadManifest: %v", err)
	}
	if len(m.Inputs) != 1 {
		t.Fatalf("want 1 entry, got %d", len(m.Inputs))
	}
	locs := m.Inputs[0].SourceLocations
	if len(locs) != 3 {
		t.Fatalf("want 3 expanded locations, got %d: %+v", len(locs), locs)
	}
	for i, want := range []int{10, 11, 12} {
		if locs[i].Line != want {
			t.Errorf("loc[%d].Line = %d, want %d", i, locs[i].Line, want)
		}
	}
}

// TestManifestLoader_CommaExpansion verifies the loader expands
// "file.go:47,111" into two SourceLocation entries.
func TestManifestLoader_CommaExpansion(t *testing.T) {
	dir := t.TempDir()
	body := `inputs:
  - id: comma_test
    source: synthetic
    source_locations:
      - some/file.go:47,111
`
	if err := writeFile(dir+"/m.yaml", body); err != nil {
		t.Fatalf("write: %v", err)
	}
	m, err := LoadManifest(dir + "/m.yaml")
	if err != nil {
		t.Fatalf("LoadManifest: %v", err)
	}
	locs := m.Inputs[0].SourceLocations
	if len(locs) != 2 {
		t.Fatalf("want 2 locations, got %d: %+v", len(locs), locs)
	}
	if locs[0].Line != 47 || locs[1].Line != 111 {
		t.Errorf("got lines [%d, %d]; want [47, 111]", locs[0].Line, locs[1].Line)
	}
}

// TestManifestLoader_HaltOnEmpty verifies the loader returns an error
// for a manifest with zero inputs.
func TestManifestLoader_HaltOnEmpty(t *testing.T) {
	dir := t.TempDir()
	body := `# empty manifest
out_of_scope:
  - id: only_oos
    source: x
`
	if err := writeFile(dir+"/m.yaml", body); err != nil {
		t.Fatalf("write: %v", err)
	}
	_, err := LoadManifest(dir + "/m.yaml")
	if err == nil {
		t.Fatal("expected halt error for zero-inputs manifest; got nil")
	}
	if !strings.Contains(err.Error(), "zero `inputs:` entries") {
		t.Errorf("expected halt-condition error; got: %v", err)
	}
}

// writeFile is a tiny helper used by parser-only tests that don't need
// the full synthetic-module scaffolding.
func writeFile(path, content string) error {
	return writeFileImpl(path, content)
}
