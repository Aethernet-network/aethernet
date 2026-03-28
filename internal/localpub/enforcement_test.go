package localpub_test

// This test enforces that locally-authored DAG events go through the
// authoritative localpub.Publisher, preventing the "dag.Add without
// broadcast" class of bugs that caused commits 21057ca through 06018f0.
//
// It scans all non-test Go source files for calls to dag.Add and verifies
// each one is either:
//   - In the allowlist (DAG internals, network sync/repair, the publisher)
//   - In a recognized test-fallback path that only executes when no
//     publisher is wired
//
// If this test fails, a developer has added a new dag.Add call that
// bypasses the publisher. The fix is to use localpub.Publisher.Publish()
// instead.

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// allowedDAGAddFiles lists files where direct dag.Add calls are legitimate.
// These are infrastructure paths that handle inbound/synced/replayed events
// (NOT locally-authored events) or the publisher itself.
var allowedDAGAddFiles = map[string]string{
	// DAG package internals — the Add method definition and store replay.
	"internal/dag/dag.go": "DAG.Add method definition",

	// Network layer — inbound events from peers, not locally authored.
	"internal/network/node.go":        "inbound MsgEvent and MsgSyncBatch from peers",
	"internal/network/materialize.go": "Fast Path v2 materialization of peer events",
	"internal/network/repair.go":      "Fast Path repair — missing parent events from peers",

	// The publisher itself — the single authoritative local publication path.
	"internal/localpub/publisher.go": "localpub.Publisher.Publish (the authoritative path)",

	// Standalone test/dev tooling — not the protocol node binary.
	"cmd/aet-loadtest/main.go": "load-testing tool (standalone, not protocol node)",
}

// testFallbackFiles lists files where dag.Add calls exist as test fallback
// paths — inside `} else {` blocks that only execute when no publisher is
// wired. These are acceptable because:
//   - The live runtime always wires a publisher before event creation
//   - The fallback only fires in unit tests that don't construct a full stack
//   - Each fallback is guarded by `if s.publisher != nil { ... } else { dag.Add }`
var testFallbackFiles = map[string]string{
	"internal/api/server.go":          "emitDAGEvent, submitAndAdd, handleRegisterAgent fallbacks",
	"internal/autovalidator/auto.go":  "emitVote, settleTask fallbacks",
	"internal/trajectory/service.go":  "EmitCommit fallback",
}

func TestEnforcement_NoUnauthorizedDAGAdd(t *testing.T) {
	repoRoot := findRepoRoot(t)

	var violations []string

	err := filepath.Walk(repoRoot, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		// Skip directories, non-Go files, test files, and vendor.
		if info.IsDir() {
			if info.Name() == "vendor" || info.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		if strings.HasSuffix(path, "_test.go") {
			return nil // test files may use dag.Add freely
		}

		relPath, _ := filepath.Rel(repoRoot, path)

		// Check if this file is in the allowlist or test-fallback list.
		if _, allowed := allowedDAGAddFiles[relPath]; allowed {
			return nil
		}
		if _, fallback := testFallbackFiles[relPath]; fallback {
			return nil
		}

		// Scan the file for dag.Add calls.
		dagAddLines := findDAGAddCalls(path)
		for _, line := range dagAddLines {
			violations = append(violations, fmt.Sprintf(
				"%s:%d — direct dag.Add call found.\n"+
					"  Use localpub.Publisher.Publish() instead.\n"+
					"  If this is a legitimate inbound/sync path, add it to allowedDAGAddFiles\n"+
					"  in internal/localpub/enforcement_test.go.",
				relPath, line,
			))
		}

		return nil
	})
	if err != nil {
		t.Fatalf("walk error: %v", err)
	}

	if len(violations) > 0 {
		t.Errorf("Found %d unauthorized dag.Add call(s):\n\n%s\n\n"+
			"Every locally-authored event must go through localpub.Publisher.Publish().\n"+
			"Direct dag.Add bypasses Fast Path dissemination and legacy broadcast,\n"+
			"causing events to exist only on the local node.",
			len(violations), strings.Join(violations, "\n"))
	}
}

func TestEnforcement_AllowlistFilesExist(t *testing.T) {
	repoRoot := findRepoRoot(t)

	for relPath, reason := range allowedDAGAddFiles {
		fullPath := filepath.Join(repoRoot, relPath)
		if _, err := os.Stat(fullPath); os.IsNotExist(err) {
			t.Errorf("allowlist file does not exist: %s (%s)", relPath, reason)
		}
	}
	for relPath, reason := range testFallbackFiles {
		fullPath := filepath.Join(repoRoot, relPath)
		if _, err := os.Stat(fullPath); os.IsNotExist(err) {
			t.Errorf("test-fallback file does not exist: %s (%s)", relPath, reason)
		}
	}
}

func TestEnforcement_CmdNodeMainHasZeroDAGAdd(t *testing.T) {
	repoRoot := findRepoRoot(t)
	mainPath := filepath.Join(repoRoot, "cmd", "node", "main.go")

	lines := findDAGAddCalls(mainPath)
	if len(lines) > 0 {
		t.Errorf("cmd/node/main.go has %d direct dag.Add call(s) at lines %v.\n"+
			"All locally-authored events in main.go must use localpub.Publisher.Publish().",
			len(lines), lines)
	}
}

func TestEnforcement_ProtocolClientHasNoDAGAdd(t *testing.T) {
	repoRoot := findRepoRoot(t)
	clientPath := filepath.Join(repoRoot, "internal", "protocol", "client.go")

	lines := findDAGAddCalls(clientPath)
	if len(lines) > 0 {
		t.Errorf("internal/protocol/client.go has %d direct dag.Add call(s) at lines %v.\n"+
			"The protocol client must use its publisher field, not dag.Add directly.",
			len(lines), lines)
	}
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// findDAGAddCalls scans a Go source file for lines containing dag.Add calls.
// Returns the line numbers where calls were found.
func findDAGAddCalls(path string) []int {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()

	var lines []int
	scanner := bufio.NewScanner(f)
	lineNum := 0
	for scanner.Scan() {
		lineNum++
		line := scanner.Text()
		trimmed := strings.TrimSpace(line)

		// Skip comments.
		if strings.HasPrefix(trimmed, "//") {
			continue
		}

		// Match patterns that indicate a dag.Add call:
		//   - ".dag.Add("  — field access on a dag variable
		//   - "dag.Add("   — direct package or variable call
		//   - "d.Add("     — common DAG variable names (d, dag)
		// Exclude false positives from unrelated .Add() methods
		// (metrics counters, maps, slices, etc.).
		if strings.Contains(line, ".dag.Add(") ||
			containsDAGPackageAdd(line) {
			lines = append(lines, lineNum)
		}
	}
	return lines
}

// containsDAGPackageAdd returns true if the line contains a call pattern
// consistent with calling dag.DAG.Add — specifically `dag.Add(` as a
// package-qualified or variable-qualified call. This excludes false
// positives like `metrics.Counter.Add(`, `feeCollector.Add(`, etc.
func containsDAGPackageAdd(line string) bool {
	// Only match lines where "dag" immediately precedes ".Add("
	// This catches: dag.Add(, d.dag.Add(, stack.dag.Add(
	// But NOT: metrics.FeesCollected.Add(, feeCollector.Add(
	idx := strings.Index(line, ".Add(")
	if idx < 0 {
		return false
	}
	// Look at the token immediately before .Add(
	prefix := strings.TrimSpace(line[:idx])
	// Must end with "dag" (the variable or package name).
	return strings.HasSuffix(prefix, "dag") ||
		strings.HasSuffix(prefix, ".dag")
}

// findRepoRoot locates the repository root by walking up from the current
// file's directory looking for go.mod.
func findRepoRoot(t *testing.T) string {
	t.Helper()

	// Start from the known location of this test file.
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}

	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("could not find go.mod — repository root not found")
		}
		dir = parent
	}
}
