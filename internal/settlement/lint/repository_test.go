package lint

import (
	"testing"
)

// TestLintRepository is the unsuppressable CI-gate test: it runs the
// settlement-input-manifest lint against the live host module.
//
// Failure semantics (per F5 Phase 5A.4.c, mirroring the projections/lint
// precedent):
//
//   - Manifest-integrity issues (declared file:line points at unreadable
//     file or out-of-range line) → FAIL. These are clear bugs in the
//     manifest itself.
//   - Stale manifest entries (declared in-scope file:line but no
//     extracted read at that location) → FAIL. Drift detection.
//   - Insufficient suppression pragmas (reason fails the ≥20-char,
//     ≥3-word validation) → FAIL.
//   - Undeclared reads (in-scope read site not in manifest) → currently
//     surfaced as WARNINGS, not failures. The manifest is built up
//     across F5 Phase 5A; full read-coverage is the goal of subsequent
//     phases. Once 5D regression detection is wired, this gate flips
//     to FAIL.
//
// Per CLAUDE.md and Gate 5A.1 Finding 10: if this test starts failing
// after a change, the contributor MUST investigate whether the failing
// invariant is correct rather than "just updating the test." Stale
// manifest entries indicate code reorganized but the audit baseline
// did not — fix the audit, do not silence it.
func TestLintRepository(t *testing.T) {
	root := moduleRoot(t)
	report, err := Check(root)
	if err != nil {
		t.Fatalf("settlement/lint: Check(%q): %v", root, err)
	}
	if report == nil {
		t.Fatalf("settlement/lint: Check returned nil Report")
	}

	// Hard failures: integrity, stale entries, insufficient pragmas.
	if len(report.ManifestIntegrityIssues) > 0 ||
		len(report.StaleManifestEntries) > 0 ||
		len(report.InsufficientPragmas) > 0 {
		// Build a focused failure report (omit the noisy
		// undeclared-reads section while it is still informational).
		focused := &Report{
			ManifestIntegrityIssues: report.ManifestIntegrityIssues,
			StaleManifestEntries:    report.StaleManifestEntries,
			InsufficientPragmas:     report.InsufficientPragmas,
		}
		t.Fatalf("settlement/lint: manifest has structural failures (drift, integrity, or insufficient pragmas)\n\n%s",
			focused.Format())
	}

	// Surface undeclared reads as informational warnings so contributors
	// see what coverage gaps remain. This is non-failure during F5 5A;
	// 5D will flip this to FAIL.
	if len(report.UndeclaredReads) > 0 {
		t.Logf("settlement/lint: %d undeclared in-scope read sites (manifest coverage gap; non-failure during F5 5A — flips to FAIL in 5D)",
			len(report.UndeclaredReads))
	}

	// Other warnings.
	for _, w := range report.Warnings {
		t.Logf("settlement/lint warning: %s", w)
	}

	// Informational: how many in-scope reads were extracted total.
	t.Logf("settlement/lint: extracted %d in-scope read sites; manifest declares %d unique file:line locations across %d input rows",
		report.ReadSiteCount,
		len(report.Manifest.DeclaredLocations()),
		len(report.Manifest.Inputs))
}
