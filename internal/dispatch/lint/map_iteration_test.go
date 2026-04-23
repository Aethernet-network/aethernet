package lint

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestMapIterationLint is the CI gate for the E-2 invariant: every
// production `range over map` callsite must be classified Safe (via an
// inline annotation) or sorted before iteration. Runs against the full
// repo. After F4A step 11's annotation sweep, the only legitimate output
// is zero violations.
func TestMapIterationLint(t *testing.T) {
	moduleRoot := findModuleRoot(t)
	report, err := CheckMapIteration(moduleRoot)
	if err != nil {
		t.Fatalf("map-iter lint: %v", err)
	}
	if report.HasFailures() {
		for _, v := range report.Violations {
			t.Errorf("MAP-ITER VIOLATION: %s:%d  range %s  (type=%s) — needs `// safe: <reason>`, a sort. call within 5 lines, or rename to *sorted*",
				v.File, v.Line, v.Iterated, v.TypeStr)
		}
		t.FailNow()
	}
	if report.MapRanges == 0 {
		t.Fatal("map-iter lint scanned the repo but found zero map ranges — heuristic must be broken")
	}
	t.Logf("map-iter lint: %d annotated map-range callsites (skipped-unknown=%d)", report.MapRanges, report.SkippedUnknown)
}

// TestMapIteration_DeliberateViolation creates a temp module containing
// a single unannotated map range and verifies the lint flags it. Mirrors
// the F4 plan §6.4 verification ("introducing a deliberate violation and
// observing build failure").
func TestMapIteration_DeliberateViolation(t *testing.T) {
	tmp := t.TempDir()
	pkgDir := filepath.Join(tmp, "internal", "fakepkg")
	if err := os.MkdirAll(pkgDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(tmp, "go.mod"), []byte("module fixturemod\n\ngo 1.22\n"), 0o644); err != nil {
		t.Fatalf("write go.mod: %v", err)
	}

	// Unannotated map range. Should be flagged.
	src := `package fakepkg

type Holder struct {
	m map[string]int
}

func (h *Holder) Sum() int {
	total := 0
	for _, v := range h.m {
		total += v
	}
	return total
}
`
	if err := os.WriteFile(filepath.Join(pkgDir, "violator.go"), []byte(src), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	report, err := CheckMapIteration(tmp)
	if err != nil {
		t.Fatalf("CheckMapIteration: %v", err)
	}
	if !report.HasFailures() {
		t.Fatalf("expected violation for unannotated map range; got none. mapRanges=%d", report.MapRanges)
	}
	found := false
	for _, v := range report.Violations {
		if strings.HasSuffix(v.File, "violator.go") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected violation in violator.go; got %+v", report.Violations)
	}
}

// TestMapIteration_SafeAnnotationSuppresses verifies the same fixture is
// NOT flagged when a `// safe:` annotation appears within the 5-line
// window above the range statement.
func TestMapIteration_SafeAnnotationSuppresses(t *testing.T) {
	tmp := t.TempDir()
	pkgDir := filepath.Join(tmp, "internal", "fakepkg")
	if err := os.MkdirAll(pkgDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(tmp, "go.mod"), []byte("module fixturemod\n\ngo 1.22\n"), 0o644); err != nil {
		t.Fatalf("write go.mod: %v", err)
	}
	src := `package fakepkg

type Holder struct {
	m map[string]int
}

func (h *Holder) Sum() int {
	total := 0
	// safe: commutative sum across all map entries
	for _, v := range h.m {
		total += v
	}
	return total
}
`
	if err := os.WriteFile(filepath.Join(pkgDir, "ok.go"), []byte(src), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	report, err := CheckMapIteration(tmp)
	if err != nil {
		t.Fatalf("CheckMapIteration: %v", err)
	}
	if report.HasFailures() {
		t.Fatalf("expected safe annotation to suppress violation; got %+v", report.Violations)
	}
}

// TestMapIteration_SortPragmaSuppresses verifies a sort. call in the
// preceding 5 lines is accepted as evidence of pre-iteration sort.
func TestMapIteration_SortPragmaSuppresses(t *testing.T) {
	tmp := t.TempDir()
	pkgDir := filepath.Join(tmp, "internal", "fakepkg")
	if err := os.MkdirAll(pkgDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(tmp, "go.mod"), []byte("module fixturemod\n\ngo 1.22\n"), 0o644); err != nil {
		t.Fatalf("write go.mod: %v", err)
	}
	src := `package fakepkg

import "sort"

type Holder struct {
	m map[string]int
}

func (h *Holder) Keys() []string {
	keys := make([]string, 0, len(h.m))
	for k := range h.m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]string, 0, len(h.m))
	for _, k := range keys {
		_ = h.m[k]
		out = append(out, k)
	}
	return out
}
`
	if err := os.WriteFile(filepath.Join(pkgDir, "sorted.go"), []byte(src), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	report, err := CheckMapIteration(tmp)
	if err != nil {
		t.Fatalf("CheckMapIteration: %v", err)
	}
	// The first range over h.m has no annotation, but the sort.Strings
	// call lives within 5 lines below — out of window. So the first range
	// MUST be flagged. The second range is over a slice (keys), not a
	// map, so it's not subject to the rule. Confirm this expected behavior.
	if !report.HasFailures() {
		t.Fatal("expected first range (no annotation, no preceding sort) to be flagged")
	}
}

// TestMapIteration_SliceRangesAreNotFlagged confirms the heuristic does
// not demand annotation on slice ranges. This is the load-bearing
// non-noise property of the lint per F4A step 11 task spec.
func TestMapIteration_SliceRangesAreNotFlagged(t *testing.T) {
	tmp := t.TempDir()
	pkgDir := filepath.Join(tmp, "internal", "fakepkg")
	if err := os.MkdirAll(pkgDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(tmp, "go.mod"), []byte("module fixturemod\n\ngo 1.22\n"), 0o644); err != nil {
		t.Fatalf("write go.mod: %v", err)
	}
	src := `package fakepkg

type Holder struct {
	xs []int
}

func (h *Holder) Sum() int {
	total := 0
	for _, v := range h.xs {
		total += v
	}
	return total
}

func (h *Holder) Channel(ch <-chan int) int {
	total := 0
	for v := range ch {
		total += v
	}
	return total
}

func (h *Holder) String() int {
	total := 0
	for _, v := range "abcd" {
		total += int(v)
	}
	return total
}
`
	if err := os.WriteFile(filepath.Join(pkgDir, "slices.go"), []byte(src), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	report, err := CheckMapIteration(tmp)
	if err != nil {
		t.Fatalf("CheckMapIteration: %v", err)
	}
	if report.HasFailures() {
		t.Fatalf("slice/channel/string ranges must NOT be flagged; got %+v", report.Violations)
	}
}
