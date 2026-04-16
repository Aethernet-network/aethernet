package lint

import (
	"os"
	"path/filepath"
	"testing"
)

// TestNoBypassDispatchLint is the CI gate. It runs the no-bypass lint
// against the full repo. With zero consumers registered in Part C, the
// lint has nothing to flag except validating existing dispatch:lint
// pragmas.
func TestNoBypassDispatchLint(t *testing.T) {
	moduleRoot := findModuleRoot(t)
	report, err := Check(moduleRoot)
	if err != nil {
		t.Fatalf("dispatch lint: %v", err)
	}

	if report.HasFailures() {
		for _, v := range report.Violations {
			t.Errorf("VIOLATION: %s:%d type %s — %s", v.File, v.Line, v.TypeName, v.Detail)
		}
		for _, v := range report.InsufficientSuppressions {
			t.Errorf("INSUFFICIENT PRAGMA: %s — %s", v.File, v.Detail)
		}
		t.FailNow()
	}

	// Verify the marketplace exemption pragma was recognized.
	found := false
	for _, p := range report.RecognizedPragmas {
		if containsSubstring(p, "marketplace-exempt") {
			found = true
			break
		}
	}
	if !found {
		t.Error("marketplace exemption pragma at internal/marketplace/server.go not recognized")
	}
}

func TestPragmaRecognition_Valid(t *testing.T) {
	if err := ValidatePragma("marketplace binary operates own application-layer escrow; protocol-escrow integration tracked as follow-up workstream"); err != nil {
		t.Errorf("valid pragma rejected: %v", err)
	}
}

func TestPragmaRecognition_TooShort(t *testing.T) {
	if err := ValidatePragma("short"); err == nil {
		t.Error("expected error for short justification")
	}
}

func TestPragmaRecognition_TooFewWords(t *testing.T) {
	if err := ValidatePragma("this-is-one-long-word-but-only-one"); err == nil {
		t.Error("expected error for too few words")
	}
}

func TestMarketplaceExemption(t *testing.T) {
	moduleRoot := findModuleRoot(t)
	report, err := Check(moduleRoot)
	if err != nil {
		t.Fatalf("Check: %v", err)
	}

	for _, p := range report.RecognizedPragmas {
		if containsSubstring(p, "marketplace-exempt") {
			return
		}
	}
	t.Error("marketplace exemption not recognized; expected dispatch:lint pragma at internal/marketplace/server.go:355")
}

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

func containsSubstring(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(s) > 0 && findSub(s, sub))
}

func findSub(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
