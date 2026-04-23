// Package lint implements the no-bypass CI check for the
// CanonicalEventDispatcher per invariant C-10. Fails the build if any
// canonical settlement effect (currently: VerificationConsensusSettler.Settle)
// is invoked outside the dispatcher's own consumer Apply path, unless the
// call site is justified by an inline dispatch:lint pragma.
//
// Runs as part of go test ./... via TestNoBypassDispatchLint.
package lint

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var dispatchPragmaRE = regexp.MustCompile(`dispatch:lint\s+(\S+)\s+"([^"]*)"`)

// canonicalSettlerTypeNames lists receiver types whose method calls are
// canonical settlement effects subject to the no-bypass rule. The lint
// flags any call to a canonical method (see canonicalMethodsByType) on
// a value whose declared type matches one of these names (matched as
// the trailing ident of the type expression).
//
// To extend the rule to a new canonical-effect emitter:
//  1. Add its type name here.
//  2. Add the canonical-effect method name(s) to canonicalMethodsByType.
//
// Tests for this lint use a fixture-only sentinel name
// ("FixtureCanonicalSettler") to verify detection without requiring a
// real production type for the test.
var canonicalSettlerTypeNames = map[string]bool{
	"VerificationConsensusSettler": true,
	// F4B §5.2.4: settlement.Applicator.Apply is a canonical effect
	// once Settlement events are routed through the dispatcher's
	// logical-key admission. Direct Apply calls outside
	// internal/dispatch/ bypass the cluster-uniform verdict derivation
	// and re-introduce the per-node selection-race bug class.
	"Applicator":               true,
	"FixtureCanonicalSettler":  true, // test-only fixture sentinel
	"FixtureCanonicalApplicator": true, // test-only fixture sentinel (Applicator-shape)
}

// canonicalMethodsByType maps a canonical receiver type to the set of
// methods on that type whose invocation is a canonical-effect call.
// The lint flags call sites where the (type, method) pair is in this
// map AND the call is outside internal/dispatch/ AND the call is not
// authorized by a nearby dispatch:lint pragma.
//
// An empty map value (nil) means "any method on this type" — reserved
// for future use; today every entry names an explicit method set.
var canonicalMethodsByType = map[string]map[string]bool{
	"VerificationConsensusSettler": {"Settle": true},
	"Applicator":                   {"Apply": true},
	"FixtureCanonicalSettler":      {"Settle": true},
	"FixtureCanonicalApplicator":   {"Apply": true},
}

// dispatcherInternalPrefix marks files whose Settle calls are the
// legitimate dispatcher-mediated settlement path. The dispatcher's own
// consumer Apply path IS the canonical settlement entrypoint; flagging
// it would be circular.
const dispatcherInternalPrefix = "internal/dispatch/"

// Violation records a canonical settler call site that bypasses the
// dispatcher without an authorizing pragma.
type Violation struct {
	File     string
	Line     int
	TypeName string
	Detail   string
}

// Report is the output of Check.
type Report struct {
	Violations               []Violation
	RecognizedPragmas        []string
	InsufficientSuppressions []Violation
}

// HasFailures returns true if any unresolved violations exist.
func (r *Report) HasFailures() bool {
	return len(r.Violations) > 0 || len(r.InsufficientSuppressions) > 0
}

// Check scans the module rooted at moduleRoot for no-bypass violations.
//
// The check runs in two passes per file:
//  1. Pragma scan: validates dispatch:lint pragma format (>=20 chars,
//     >=3 words justification) and records line numbers for later
//     suppression matching.
//  2. AST scan: parses every non-test .go file under internal/, walks
//     for CallExpr whose method is "Settle" and whose receiver resolves
//     (via per-file struct-field and var-decl tables) to one of the
//     canonical settler types listed in canonicalSettlerTypeNames.
//     A flagged call is suppressed iff (a) its file is under
//     internal/dispatch/, or (b) a valid dispatch:lint pragma appears
//     within 3 lines above the call.
func Check(moduleRoot string) (*Report, error) {
	report := &Report{}
	internalDir := filepath.Join(moduleRoot, "internal")
	fset := token.NewFileSet()

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
		rel := relPath(moduleRoot, path)

		// Pragma scan + per-line line-number index.
		pragmaLines := scanPragmas(content, rel, report)

		// AST scan for Settle bypass.
		file, parseErr := parser.ParseFile(fset, path, data, parser.ParseComments)
		if parseErr != nil {
			// Non-fatal — file may have intentional parse-incompatible test
			// fixtures elsewhere. Skip and continue.
			return nil
		}
		settlerReceivers := collectSettlerReceivers(file)
		findSettleBypass(file, fset, settlerReceivers, pragmaLines, rel, report)

		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("dispatch lint: walk %s: %w", internalDir, err)
	}

	return report, nil
}

// scanPragmas extracts dispatch:lint pragmas, validates them, appends to
// the report's RecognizedPragmas/InsufficientSuppressions, and returns
// the set of line numbers (1-indexed) carrying valid pragmas. Line
// numbers are derived by counting newlines up to each match offset.
func scanPragmas(content, rel string, report *Report) map[int]bool {
	pragmaLines := make(map[int]bool)
	for _, idx := range dispatchPragmaRE.FindAllStringSubmatchIndex(content, -1) {
		start := idx[0]
		match := content[start:idx[1]]
		sub := dispatchPragmaRE.FindStringSubmatch(match)
		if len(sub) < 3 {
			continue
		}
		tag := sub[1]
		justification := sub[2]
		line := 1 + strings.Count(content[:start], "\n")
		if len(justification) < 20 || len(strings.Fields(justification)) < 3 {
			report.InsufficientSuppressions = append(report.InsufficientSuppressions, Violation{
				File:   rel,
				Line:   line,
				Detail: fmt.Sprintf("dispatch:lint pragma %q has insufficient justification %q (need >=20 chars, >=3 words)", tag, justification),
			})
			continue
		}
		report.RecognizedPragmas = append(report.RecognizedPragmas, rel+": "+tag)
		pragmaLines[line] = true
	}
	return pragmaLines
}

// collectSettlerReceivers returns a map from identifier name to the
// canonical settler type name it resolves to within file. The returned
// map covers:
//   - struct field names whose type is *<canonical>
//   - top-level var names whose type is *<canonical>
//   - function parameter / local var names whose type is *<canonical>
//
// The check is conservative: receiver identifier names in the map are
// candidates, but the final bypass decision still requires the call's
// .Sel.Name to match the canonical method registered for the receiver's
// type in canonicalMethodsByType.
func collectSettlerReceivers(file *ast.File) map[string]string {
	names := make(map[string]string)

	// Walk struct field declarations.
	ast.Inspect(file, func(n ast.Node) bool {
		ts, ok := n.(*ast.TypeSpec)
		if !ok {
			return true
		}
		st, ok := ts.Type.(*ast.StructType)
		if !ok {
			return true
		}
		for _, field := range st.Fields.List {
			typeName := canonicalSettlerTypeName(field.Type)
			if typeName == "" {
				continue
			}
			for _, name := range field.Names {
				names[name.Name] = typeName
			}
		}
		return true
	})

	// Walk var/local declarations (DeclStmt + GenDecl + ValueSpec).
	ast.Inspect(file, func(n ast.Node) bool {
		switch d := n.(type) {
		case *ast.GenDecl:
			if d.Tok != token.VAR {
				return true
			}
			for _, spec := range d.Specs {
				vs, ok := spec.(*ast.ValueSpec)
				if !ok {
					continue
				}
				if vs.Type != nil {
					typeName := canonicalSettlerTypeName(vs.Type)
					if typeName != "" {
						for _, name := range vs.Names {
							names[name.Name] = typeName
						}
					}
				}
			}
		case *ast.FuncDecl:
			if d.Type.Params != nil {
				for _, field := range d.Type.Params.List {
					typeName := canonicalSettlerTypeName(field.Type)
					if typeName == "" {
						continue
					}
					for _, name := range field.Names {
						names[name.Name] = typeName
					}
				}
			}
		}
		return true
	})

	return names
}

// canonicalSettlerTypeName returns the canonical type name (e.g.,
// "VerificationConsensusSettler", "Applicator") if expr is a pointer
// to one of canonicalSettlerTypeNames; "" otherwise. Accepts both
// bare-type form (*VerificationConsensusSettler) and qualified form
// (*settlement.VerificationConsensusSettler).
func canonicalSettlerTypeName(expr ast.Expr) string {
	star, ok := expr.(*ast.StarExpr)
	if !ok {
		return ""
	}
	switch t := star.X.(type) {
	case *ast.Ident:
		if canonicalSettlerTypeNames[t.Name] {
			return t.Name
		}
	case *ast.SelectorExpr:
		if canonicalSettlerTypeNames[t.Sel.Name] {
			return t.Sel.Name
		}
	}
	return ""
}

// findSettleBypass walks file for CallExpr nodes invoking a canonical
// method on a receiver in settlerReceivers and records violations
// subject to file-path and pragma-based exemptions. The (receiver
// type, method) pair must match canonicalMethodsByType for a
// violation to register.
func findSettleBypass(
	file *ast.File,
	fset *token.FileSet,
	settlerReceivers map[string]string,
	pragmaLines map[int]bool,
	rel string,
	report *Report,
) {
	if len(settlerReceivers) == 0 {
		return
	}
	if strings.HasPrefix(filepath.ToSlash(rel), dispatcherInternalPrefix) {
		// Path-based exemption: the dispatcher's own consumer Apply path
		// is the legitimate settlement entrypoint. F3-B C-10 explicitly
		// whitelists internal/dispatch/ as the canonical mediator.
		return
	}

	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		// Receiver must resolve to a known canonical settler identifier.
		recvName := receiverIdent(sel.X)
		if recvName == "" {
			return true
		}
		typeName, known := settlerReceivers[recvName]
		if !known {
			return true
		}
		methods, ok := canonicalMethodsByType[typeName]
		if !ok || !methods[sel.Sel.Name] {
			return true
		}

		pos := fset.Position(call.Lparen)
		// Pragma exemption: a valid dispatch:lint pragma within 3 lines
		// above the call site authorizes the bypass. The recognition
		// legacy else-branches use this pattern.
		if hasNearbyPragma(pragmaLines, pos.Line, 3) {
			return true
		}

		report.Violations = append(report.Violations, Violation{
			File:     rel,
			Line:     pos.Line,
			TypeName: typeName,
			Detail:   "direct call to canonical " + typeName + "." + sel.Sel.Name + " bypasses the dispatcher; route through dispatcher.Admit or add a justified dispatch:lint pragma within 3 lines above the call",
		})
		return true
	})
}

// receiverIdent returns the trailing identifier name of a receiver
// expression. For `c.settler` returns "settler"; for `settler` returns
// "settler"; for unsupported expressions returns "".
func receiverIdent(expr ast.Expr) string {
	switch e := expr.(type) {
	case *ast.Ident:
		return e.Name
	case *ast.SelectorExpr:
		return e.Sel.Name
	}
	return ""
}

// hasNearbyPragma reports whether any pragmaLine lies within window
// lines above callLine (inclusive). Window=3 matches the "comment
// directly above the call" convention used elsewhere in the repo.
func hasNearbyPragma(pragmaLines map[int]bool, callLine, window int) bool {
	for delta := 0; delta <= window; delta++ {
		if pragmaLines[callLine-delta] {
			return true
		}
	}
	return false
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
