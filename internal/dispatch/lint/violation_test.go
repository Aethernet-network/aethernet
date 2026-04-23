package lint

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestDeliberateViolation_Detected verifies the no-bypass lint actually
// catches a direct call to a canonical settler from outside the
// dispatcher's own consumer Apply path. Per F4 plan §8.3: "verify by
// introducing a deliberate violation and observing build failure."
//
// The fixture is constructed in a temp dir mirroring the layout the
// lint expects (<root>/internal/<pkg>/<file>.go) so the path-based
// dispatcher exemption does NOT apply. The fixture's go.mod exists so
// findModuleRoot semantics are mirrored, but Check itself only walks
// internal/ and parses files standalone — it does not require the
// fixture to compile.
//
// The fixture references a synthetic type "FixtureCanonicalSettler"
// which is registered in canonicalSettlerTypeNames specifically so
// tests can exercise the bypass path without depending on the real
// production settler type living in a particular relative location.
func TestDeliberateViolation_Detected(t *testing.T) {
	tmp := t.TempDir()
	pkgDir := filepath.Join(tmp, "internal", "fakepkg")
	if err := os.MkdirAll(pkgDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	// go.mod stub so the temp dir looks module-rooted.
	if err := os.WriteFile(filepath.Join(tmp, "go.mod"), []byte("module fixturemod\n\ngo 1.22\n"), 0o644); err != nil {
		t.Fatalf("write go.mod: %v", err)
	}

	// The deliberate-violation fixture: a package outside internal/dispatch
	// that holds a *FixtureCanonicalSettler field and calls .Settle on it
	// without any pragma. Should be flagged.
	bypassSrc := `package fakepkg

import "context"

type FixtureCanonicalSettler struct{}

func (s *FixtureCanonicalSettler) Settle(ctx context.Context) error { return nil }

type Bypasser struct {
	settler *FixtureCanonicalSettler
}

func (b *Bypasser) Run(ctx context.Context) error {
	return b.settler.Settle(ctx)
}
`
	if err := os.WriteFile(filepath.Join(pkgDir, "bypass.go"), []byte(bypassSrc), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	report, err := Check(tmp)
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if !report.HasFailures() {
		t.Fatalf("expected violation for fixture bypass call; got none. recognized=%v", report.RecognizedPragmas)
	}

	// Confirm the violation points at our fixture file and the right method.
	found := false
	for _, v := range report.Violations {
		if strings.HasSuffix(v.File, "bypass.go") && v.TypeName == "settler" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected violation in bypass.go on receiver 'settler'; got %+v", report.Violations)
	}
}

// TestDeliberateViolation_PragmaSuppresses verifies the same fixture is
// NOT flagged when an inline dispatch:lint pragma with a valid
// justification appears within 3 lines above the call. Confirms the
// pragma-exemption path round-trips correctly.
func TestDeliberateViolation_PragmaSuppresses(t *testing.T) {
	tmp := t.TempDir()
	pkgDir := filepath.Join(tmp, "internal", "fakepkg")
	if err := os.MkdirAll(pkgDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(tmp, "go.mod"), []byte("module fixturemod\n\ngo 1.22\n"), 0o644); err != nil {
		t.Fatalf("write go.mod: %v", err)
	}

	suppressedSrc := `package fakepkg

import "context"

type FixtureCanonicalSettler struct{}

func (s *FixtureCanonicalSettler) Settle(ctx context.Context) error { return nil }

type Bypasser struct {
	settler *FixtureCanonicalSettler
}

func (b *Bypasser) Run(ctx context.Context) error {
	// dispatch:lint fixture-test "fixture exercising the pragma suppression path under deliberate-violation test coverage"
	return b.settler.Settle(ctx)
}
`
	if err := os.WriteFile(filepath.Join(pkgDir, "suppressed.go"), []byte(suppressedSrc), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	report, err := Check(tmp)
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if report.HasFailures() {
		t.Fatalf("expected pragma to suppress violation; got violations=%+v insufficient=%+v", report.Violations, report.InsufficientSuppressions)
	}
	// Confirm the pragma was recognized.
	found := false
	for _, p := range report.RecognizedPragmas {
		if strings.Contains(p, "fixture-test") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected fixture-test pragma to be recognized; got %v", report.RecognizedPragmas)
	}
}

// TestDispatcherInternalExempt verifies a Settle call inside
// internal/dispatch/ is allowed without any pragma — that path is the
// canonical settlement entrypoint per F3-B C-10.
func TestDispatcherInternalExempt(t *testing.T) {
	tmp := t.TempDir()
	pkgDir := filepath.Join(tmp, "internal", "dispatch")
	if err := os.MkdirAll(pkgDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(tmp, "go.mod"), []byte("module fixturemod\n\ngo 1.22\n"), 0o644); err != nil {
		t.Fatalf("write go.mod: %v", err)
	}

	dispatchSrc := `package dispatch

import "context"

type FixtureCanonicalSettler struct{}

func (s *FixtureCanonicalSettler) Settle(ctx context.Context) error { return nil }

type Consumer struct {
	settler *FixtureCanonicalSettler
}

func (c *Consumer) Apply(ctx context.Context) error {
	return c.settler.Settle(ctx)
}
`
	if err := os.WriteFile(filepath.Join(pkgDir, "consumer.go"), []byte(dispatchSrc), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	report, err := Check(tmp)
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if report.HasFailures() {
		t.Fatalf("expected dispatcher-internal call to be exempt; got violations=%+v", report.Violations)
	}
}
