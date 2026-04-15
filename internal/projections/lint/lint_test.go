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

// TestReport_AnyUnregisteredFlipsFailure tests that a single suspect type
// causes HasFailures to return true.
func TestReport_AnyUnregisteredFlipsFailure(t *testing.T) {
	r := &Report{
		UnregisteredTypes: []SuspectType{
			{PackagePath: "example.com/foo", TypeName: "Bar", File: "bar.go", Line: 10, Evidence: "has *badger.DB field"},
		},
	}
	if !r.HasFailures() {
		t.Fatalf("Report with one UnregisteredType must HasFailures")
	}
}

// TestReport_FormatNamesTypeAndRemediation verifies the D6 diagnostic
// enhancement: the failure message includes the type name, file path,
// line number, and both (a) and (b) remediation paths.
func TestReport_FormatNamesTypeAndRemediation(t *testing.T) {
	r := &Report{
		UnregisteredTypes: []SuspectType{
			{
				PackagePath: "internal/example",
				TypeName:    "SecretStore",
				File:        "internal/example/secret.go",
				Line:        42,
				Evidence:    "has *badger.DB field; writer methods: Put, Delete",
			},
		},
	}
	out := r.Format()
	mustContain := []string{
		"UNREGISTERED STORE",
		"internal/example.SecretStore",
		"internal/example/secret.go",
		"Line:         42",
		"(a) register this type",
		"(b) suppress the lint",
		"at least 20 characters",
		"at least 3",
		"registration patterns defeat the static analysis",
	}
	for _, want := range mustContain {
		if !strings.Contains(out, want) {
			t.Fatalf("Format output missing %q\n---\n%s", want, out)
		}
	}
}

// TestReport_MissingRefDiagnostic verifies the MISSING INTEGRATION TEST
// diagnostic shape.
func TestReport_MissingRefDiagnostic(t *testing.T) {
	r := &Report{
		MissingRefs: []MissingRef{
			{
				EntryName:         "RoundCounter",
				DeclaredRef:       "example.com/integration.TestNotReal",
				SourceFile:        "internal/epoch/projection.go",
				SourceLine:        30,
				ResolutionFailure: "package example.com/integration not loaded",
			},
		},
	}
	out := r.Format()
	mustContain := []string{
		"MISSING INTEGRATION TEST",
		"RoundCounter",
		"example.com/integration.TestNotReal",
		"internal/epoch/projection.go:30",
		"PR-3 invariant",
	}
	for _, want := range mustContain {
		if !strings.Contains(out, want) {
			t.Fatalf("Format output missing %q\n---\n%s", want, out)
		}
	}
}

// TestReport_InsufficientSuppressionDiagnostic verifies the D4 rejection
// shape for too-short pragma reasons.
func TestReport_InsufficientSuppressionDiagnostic(t *testing.T) {
	r := &Report{
		InsufficientSuppressions: []InsufficientSuppression{
			{
				File:      "internal/foo/bar.go",
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
		"internal/foo/bar.go",
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

// TestReport_WarningsAreNonFailure verifies warnings don't flip HasFailures.
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

// TestCheck_Smoke is the commit-1 smoke: Check() must return a non-nil
// Report without error when run against the current module root. Full
// functional checks land in commits 2–7.
func TestCheck_Smoke(t *testing.T) {
	// Run against the current directory — the lint package itself.
	// Commit-1 skeleton returns an empty Report; functional checks land
	// in later commits. This smoke ensures package loading works.
	rep, err := Check(".")
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if rep == nil {
		t.Fatalf("Check returned nil Report")
	}
}
