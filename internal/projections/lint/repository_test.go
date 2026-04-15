package lint

import (
	"testing"
)

// TestLintRepository is the unsuppressable CI-gate test: it runs the
// full projection-registry lint against the live host module and fails
// with the formatted diagnostic report if any Canonical finding is
// present. `go test ./...` invokes this automatically; there is no vet
// flag to omit and no skip flag.
//
// Per step-3 plan §D7: this test is the structural counterpart to
// step-2's per-integration-test meta-assertion. Together they form a
// bidirectional check — step-2 catches rename drift from the test
// side (TestX asserts the projection entry names it); step-3 catches
// missing tests from the registry side (every entry's declared test
// symbol must exist).
//
// Failure output is the Report.Format() string, which explicitly names
// the offending type, file, line, and remediation path — a contributor
// can fix the issue without reading the lint's source.
func TestLintRepository(t *testing.T) {
	root := moduleRoot(t)
	report, err := Check(root)
	if err != nil {
		t.Fatalf("projections/lint: Check(%q): %v", root, err)
	}
	if report == nil {
		t.Fatalf("projections/lint: Check returned nil Report")
	}
	if report.HasFailures() {
		t.Fatalf("projections/lint: repository has unregistered stores or broken testrefs\n\n%s",
			report.Format())
	}
	// Warnings are non-failure; surface them in the test log so a
	// contributor running `go test -v` sees what was flagged.
	for _, w := range report.Warnings {
		t.Logf("projections/lint warning: %s", w)
	}
}
