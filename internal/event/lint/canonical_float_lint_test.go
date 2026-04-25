package lint

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestCanonicalFloatLint is the CI gate. Runs Check() against the real
// internal/event/ package and asserts zero violations. Regression guard:
// the first developer to introduce a float-bearing field in a canonical
// payload type triggers this test.
func TestCanonicalFloatLint(t *testing.T) {
	moduleRoot := findModuleRoot(t)
	report, err := Check(moduleRoot, nil)
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if report.HasFailures() {
		for _, v := range report.Violations {
			t.Errorf("canonical payload float-freedom violation: %s", v)
		}
		t.Errorf("\n→ to fix: change the field to an integer type (see docs/plans/2026-04-20-canonical-distribution-integer-migration-v2.md §4.1 for BasisPoints), or if the field is genuinely non-canonical, split the type so the canonical half is float-free.")
	}
}

// injectOverlay constructs a packages.Config.Overlay that adds a synthetic
// Go source file to internal/event/ (on top of the real files) declaring
// a canonical payload type named "SyntheticInjectedPayload". The injected
// type is appended to the canonical list for the duration of the test so
// Check() visits it.
//
// The overlay mechanism is stdlib-supported via the Overlay field on
// packages.Config; tests do not write to disk.
func injectOverlay(t *testing.T, body string) (map[string][]byte, func()) {
	t.Helper()
	moduleRoot := findModuleRoot(t)
	injectedPath := filepath.Join(moduleRoot, "internal", "event", "zz_testinjected.go")
	src := `package event

` + body
	overlay := map[string][]byte{injectedPath: []byte(src)}

	// Splice the injected type name into the canonical list for the test,
	// restoring the original afterward.
	originalList := canonicalPayloadTypeNames
	canonicalPayloadTypeNames = append(append([]string(nil), originalList...), "SyntheticInjectedPayload")
	restore := func() {
		canonicalPayloadTypeNames = originalList
	}
	return overlay, restore
}

// runInjected loads the module with the overlay and returns the Report.
func runInjected(t *testing.T, overlay map[string][]byte) *Report {
	t.Helper()
	report, err := Check(findModuleRoot(t), overlay)
	if err != nil {
		t.Fatalf("Check with overlay: %v", err)
	}
	return report
}

// hasViolationContaining returns true if any violation's String() contains
// sub.
func hasViolationContaining(report *Report, sub string) bool {
	for _, v := range report.Violations {
		if strings.Contains(v.String(), sub) {
			return true
		}
	}
	return false
}

func TestCanonicalFloatLint_InjectedFloat_Fails(t *testing.T) {
	overlay, restore := injectOverlay(t, `
type SyntheticInjectedPayload struct {
	OK    string
	BadF  float64
}
`)
	defer restore()
	report := runInjected(t, overlay)
	if !report.HasFailures() {
		t.Fatal("expected failure on injected float64 field")
	}
	if !hasViolationContaining(report, "SyntheticInjectedPayload.BadF") {
		t.Errorf("expected violation on SyntheticInjectedPayload.BadF; got %v", report.Violations)
	}
	if !hasViolationContaining(report, "float64") {
		t.Errorf("expected reason float64; got %v", report.Violations)
	}
}

func TestCanonicalFloatLint_InjectedFloat32_Fails(t *testing.T) {
	overlay, restore := injectOverlay(t, `
type SyntheticInjectedPayload struct {
	Bad float32
}
`)
	defer restore()
	report := runInjected(t, overlay)
	if !hasViolationContaining(report, "float32") {
		t.Errorf("expected float32 violation; got %v", report.Violations)
	}
}

func TestCanonicalFloatLint_InterfaceField_Fails(t *testing.T) {
	overlay, restore := injectOverlay(t, `
type SyntheticInjectedPayload struct {
	Bad interface{}
}
`)
	defer restore()
	report := runInjected(t, overlay)
	if !hasViolationContaining(report, "SyntheticInjectedPayload.Bad") ||
		!hasViolationContaining(report, "interface") {
		t.Errorf("expected interface violation; got %v", report.Violations)
	}
}

func TestCanonicalFloatLint_AnyField_Fails(t *testing.T) {
	overlay, restore := injectOverlay(t, `
type SyntheticInjectedPayload struct {
	Bad any
}
`)
	defer restore()
	report := runInjected(t, overlay)
	if !hasViolationContaining(report, "interface") {
		t.Errorf("expected interface violation for any; got %v", report.Violations)
	}
}

func TestCanonicalFloatLint_NestedStructFloat_Fails(t *testing.T) {
	overlay, restore := injectOverlay(t, `
type syntheticNested struct {
	Deep float64
}
type SyntheticInjectedPayload struct {
	Inner syntheticNested
}
`)
	defer restore()
	report := runInjected(t, overlay)
	if !hasViolationContaining(report, "SyntheticInjectedPayload.Inner.Deep") {
		t.Errorf("expected nested-path violation; got %v", report.Violations)
	}
}

func TestCanonicalFloatLint_PointerToFloat_Fails(t *testing.T) {
	overlay, restore := injectOverlay(t, `
type SyntheticInjectedPayload struct {
	Bad *float64
}
`)
	defer restore()
	report := runInjected(t, overlay)
	if !hasViolationContaining(report, "SyntheticInjectedPayload.Bad*") ||
		!hasViolationContaining(report, "float64") {
		t.Errorf("expected pointer-to-float violation; got %v", report.Violations)
	}
}

func TestCanonicalFloatLint_MapValueFloat_Fails(t *testing.T) {
	overlay, restore := injectOverlay(t, `
type SyntheticInjectedPayload struct {
	Bad map[string]float64
}
`)
	defer restore()
	report := runInjected(t, overlay)
	if !hasViolationContaining(report, "SyntheticInjectedPayload.Bad[<val>]") ||
		!hasViolationContaining(report, "float64") {
		t.Errorf("expected map-value-float violation; got %v", report.Violations)
	}
}

func TestCanonicalFloatLint_SliceOfFloat_Fails(t *testing.T) {
	overlay, restore := injectOverlay(t, `
type SyntheticInjectedPayload struct {
	Bad []float64
}
`)
	defer restore()
	report := runInjected(t, overlay)
	if !hasViolationContaining(report, "SyntheticInjectedPayload.Bad[]") ||
		!hasViolationContaining(report, "float64") {
		t.Errorf("expected slice-of-float violation; got %v", report.Violations)
	}
}

func TestCanonicalFloatLint_JSONRawMessage_Fails(t *testing.T) {
	overlay, restore := injectOverlay(t, `
import "encoding/json"
type SyntheticInjectedPayload struct {
	Bad json.RawMessage
}
`)
	defer restore()
	report := runInjected(t, overlay)
	if !hasViolationContaining(report, "SyntheticInjectedPayload.Bad") ||
		!hasViolationContaining(report, "json.RawMessage") {
		t.Errorf("expected json.RawMessage violation; got %v", report.Violations)
	}
}

func TestCanonicalFloatLint_CyclicType_Terminates(t *testing.T) {
	// Self-referential type with NO float. Lint must terminate and pass.
	overlay, restore := injectOverlay(t, `
type syntheticSelfRef struct {
	Children []syntheticSelfRef
	Label    string
	Count    uint64
}
type SyntheticInjectedPayload struct {
	Tree syntheticSelfRef
}
`)
	defer restore()
	report := runInjected(t, overlay)
	if report.HasFailures() {
		t.Errorf("cyclic type with no float should pass; got %v", report.Violations)
	}
}

func TestCanonicalFloatLint_CyclicTypeWithFloat_Flags(t *testing.T) {
	// Self-referential type that also carries a float somewhere.
	overlay, restore := injectOverlay(t, `
type syntheticSelfRefFloat struct {
	Children []syntheticSelfRefFloat
	Score    float64
}
type SyntheticInjectedPayload struct {
	Tree syntheticSelfRefFloat
}
`)
	defer restore()
	report := runInjected(t, overlay)
	if !hasViolationContaining(report, "SyntheticInjectedPayload.Tree.Score") {
		t.Errorf("expected float in cyclic type to be flagged; got %v", report.Violations)
	}
}

func TestCanonicalFloatLint_NamedFloatAlias_Fails(t *testing.T) {
	// Named type whose underlying is float64.
	overlay, restore := injectOverlay(t, `
type syntheticScore float64
type SyntheticInjectedPayload struct {
	Bad syntheticScore
}
`)
	defer restore()
	report := runInjected(t, overlay)
	if !hasViolationContaining(report, "SyntheticInjectedPayload.Bad") ||
		!hasViolationContaining(report, "float64") {
		t.Errorf("expected named-float-alias violation; got %v", report.Violations)
	}
}

func TestCanonicalPayloadTypeNames_HasNineteen(t *testing.T) {
	if got := len(canonicalPayloadTypeNames); got != 19 {
		t.Errorf("canonicalPayloadTypeNames len = %d; want 19 (updating requires explicit intent)", got)
	}
}

// TestCanonicalFloatLint_UnknownType_ReportsViolation exercises the "type
// in the hardcoded list is missing from the event package" branch. This
// normally indicates the list has drifted from the code, and the lint
// flags it loudly rather than silently skipping.
func TestCanonicalFloatLint_UnknownType_ReportsViolation(t *testing.T) {
	originalList := canonicalPayloadTypeNames
	canonicalPayloadTypeNames = append([]string{"NonExistentPayload"}, originalList...)
	defer func() { canonicalPayloadTypeNames = originalList }()

	report, err := Check(findModuleRoot(t), nil)
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if !hasViolationContaining(report, "NonExistentPayload") ||
		!hasViolationContaining(report, "not found") {
		t.Errorf("expected missing-type violation for NonExistentPayload; got %v", report.Violations)
	}
}

// TestCanonicalFloatLint_BadModulePath_ReturnsError exercises the
// package-load error path; passing a directory with no go.mod yields a
// packages.Load failure that Check surfaces as an error.
func TestCanonicalFloatLint_BadModulePath_ReturnsError(t *testing.T) {
	// /dev is present on every platform we run on and has no go.mod.
	_, err := Check("/dev", nil)
	if err == nil {
		t.Error("expected error on non-module path; got nil")
	}
}

// findModuleRoot walks up from the current directory until it finds go.mod
// and returns that directory. Mirrors the helper in internal/dispatch/lint/.
func findModuleRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("could not find module root (go.mod)")
		}
		dir = parent
	}
}
