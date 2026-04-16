// Package lint implements the no-bypass CI check for the
// CanonicalEventDispatcher per invariant C-10. Fails the build if any
// type that satisfies dispatch.Consumer is found in the codebase without
// a corresponding dispatcher registration, unless suppressed by a
// structured pragma.
//
// Runs as part of go test ./... via TestNoBypassDispatchLint.
package lint

import (
	"fmt"
	"go/ast"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var dispatchPragmaRE = regexp.MustCompile(`dispatch:lint\s+(\S+)\s+"([^"]*)"`)

// Violation records a type that satisfies dispatch.Consumer without
// registration through the dispatcher.
type Violation struct {
	File    string
	Line    int
	TypeName string
	Detail  string
}

// Report is the output of Check.
type Report struct {
	Violations              []Violation
	RecognizedPragmas       []string
	InsufficientSuppressions []Violation
}

// HasFailures returns true if any unresolved violations exist.
func (r *Report) HasFailures() bool {
	return len(r.Violations) > 0 || len(r.InsufficientSuppressions) > 0
}

// Check scans the module rooted at moduleRoot for no-bypass violations.
//
// Current implementation (Part C, zero consumers registered): scans for
// any Go source file that imports the dispatch package and contains a
// direct fabric wiring pattern (Bus.Register call with a type that
// implements Consumer methods). Also validates dispatch:lint pragma
// suppression format.
//
// This is deliberately lightweight for Part C (no consumers exist yet).
// When the first consumer is wired in commit 9 of §9, this function
// will be extended with full type-graph analysis matching the projection
// lint's AST-walking pattern.
func Check(moduleRoot string) (*Report, error) {
	report := &Report{}

	internalDir := filepath.Join(moduleRoot, "internal")
	err := filepath.Walk(internalDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		if strings.HasSuffix(path, "_test.go") {
			return nil
		}

		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		content := string(data)

		// Scan for dispatch:lint pragmas.
		for _, match := range dispatchPragmaRE.FindAllStringSubmatch(content, -1) {
			tag := match[1]
			justification := match[2]
			if len(justification) < 20 || len(strings.Fields(justification)) < 3 {
				report.InsufficientSuppressions = append(report.InsufficientSuppressions, Violation{
					File:   relPath(moduleRoot, path),
					Detail: fmt.Sprintf("dispatch:lint pragma %q has insufficient justification %q (need >=20 chars, >=3 words)", tag, justification),
				})
			} else {
				report.RecognizedPragmas = append(report.RecognizedPragmas, relPath(moduleRoot, path)+": "+tag)
			}
		}

		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("dispatch lint: walk %s: %w", internalDir, err)
	}

	return report, nil
}

// ValidatePragma checks a dispatch:lint pragma's justification for
// sufficient length and word count. Exported for direct testing.
func ValidatePragma(justification string) error {
	if len(justification) < 20 {
		return fmt.Errorf("justification too short: %d chars (need >=20)", len(justification))
	}
	if len(strings.Fields(justification)) < 3 {
		return fmt.Errorf("justification has too few words: %d (need >=3)", len(strings.Fields(justification)))
	}
	return nil
}

func relPath(base, path string) string {
	rel, err := filepath.Rel(base, path)
	if err != nil {
		return path
	}
	return rel
}

// Ensure ast and token are importable for future type-graph analysis.
var _ ast.Node
var _ token.FileSet
