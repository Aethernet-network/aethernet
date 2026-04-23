// Map-iteration determinism lint per F4 plan v2 §6 invariant E-2.
//
// Goal: prevent silent re-introduction of cross-node non-determinism caused
// by Go map iteration order. Every production `range` over a map type must
// either be classified Safe via an inline comment, or be sorted before
// iteration (caller's responsibility).
//
// This lint runs as part of go test ./internal/dispatch/lint/... and walks
// every non-test .go file under internal/, cmd/, pkg/, skipping vendor/
// and testdata/. For each *ast.RangeStmt it heuristically classifies the
// iterated expression's type. If the type is a map, the callsite must
// carry an accepted annotation; otherwise the build fails.
//
// Type classification — pragmatic without full go/types
//
// We do NOT type-check the program; that would require a heavy
// golang.org/x/tools/go/packages pipeline. Instead, per file, we build a
// per-identifier declared-type table by walking every TypeSpec, GenDecl,
// AssignStmt, and FuncDecl in the file, and recording ident -> textual
// type. For each RangeStmt, we look up the iterated expression:
//
//   - *ast.Ident — direct lookup in the per-file table
//   - *ast.SelectorExpr (e.g., rec.Consumers) — lookup the trailing field
//     name in the per-file table (we accept the cross-package false-negative
//     this entails: if a struct is defined in another file and its field is
//     a map, we may miss it; the trade-off is that the lint stays simple
//     and produces zero false positives on slice ranges)
//   - *ast.IndexExpr or *ast.CallExpr — unknown; treated as non-map.
//     Documented as a known false-negative.
//
// Only types whose textual form starts with "map[" are classified as maps.
// This is conservative on purpose: false positives (annotation demanded on
// a slice) are worse than false negatives (a map slips through), because
// annotation noise erodes trust in the lint. Known false-negatives are
// enumerated in docs/architecture/known-map-iteration-dependencies.md and
// the audit doc; they are addressable by promoting the relevant
// identifiers into the per-file table or by manual annotation.
//
// Accepted annotations (any one suffices)
//
//   1. A Go comment containing  // safe: <reason>  on the same line as
//      the `for` keyword, OR within 5 lines above the RangeStmt.
//   2. A reference to sort. (e.g., a sort.Strings call) in a line within
//      5 lines above the RangeStmt — proxy for "the iterated structure or
//      its keys were sorted before this loop ran."
//   3. The iterated identifier name contains the substring "sorted"
//      (case-insensitive) — naming-discipline shortcut.
//   4. The RangeStmt is inside a file that lives under internal/dispatch/
//      lint/ itself — the lint package's test fixtures are exempt.
package lint

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// MapIterationViolation describes one unannotated map-range callsite.
type MapIterationViolation struct {
	File     string
	Line     int
	Iterated string // textual form of the iterated expression
	TypeStr  string // declared type as the heuristic saw it
}

// MapIterationReport is the output of CheckMapIteration.
type MapIterationReport struct {
	Violations []MapIterationViolation
	// MapRanges is the total number of range-over-map callsites observed
	// (including annotated ones). Useful for audit ratios.
	MapRanges int
	// SkippedUnknown counts ranges over CallExpr/IndexExpr that the
	// heuristic could not classify. These are documented false-negatives.
	SkippedUnknown int
}

// HasFailures reports whether the report contains any violations.
func (r *MapIterationReport) HasFailures() bool {
	return len(r.Violations) > 0
}

// CheckMapIteration scans the module rooted at moduleRoot for unannotated
// production map-range callsites. Walks internal/, cmd/, pkg/. Skips
// _test.go files, vendor/, and testdata/. Returns a report describing
// violations and aggregate counts.
func CheckMapIteration(moduleRoot string) (*MapIterationReport, error) {
	report := &MapIterationReport{}
	roots := []string{"internal", "cmd", "pkg"}
	fset := token.NewFileSet()

	for _, root := range roots {
		rootPath := filepath.Join(moduleRoot, root)
		if _, err := os.Stat(rootPath); err != nil {
			continue // pkg/ may not exist; tolerate
		}
		err := filepath.Walk(rootPath, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			if info.IsDir() {
				if info.Name() == "vendor" || info.Name() == "testdata" {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}

			data, readErr := os.ReadFile(path)
			if readErr != nil {
				return readErr
			}
			file, parseErr := parser.ParseFile(fset, path, data, parser.ParseComments)
			if parseErr != nil {
				// Tolerate parse failures (matches the no-bypass lint's posture).
				return nil
			}

			rel := relPath(moduleRoot, path)
			lines := strings.Split(string(data), "\n")
			fileTable := buildFileLevelTable(file)
			scanFileForMapRanges(file, fset, rel, lines, fileTable, report)
			return nil
		})
		if err != nil {
			return nil, fmt.Errorf("map-iter lint: walk %s: %w", rootPath, err)
		}
	}
	// Stable violation ordering (file then line) to keep build-failure
	// output reproducible across runs.
	sort.Slice(report.Violations, func(i, j int) bool {
		if report.Violations[i].File != report.Violations[j].File {
			return report.Violations[i].File < report.Violations[j].File
		}
		return report.Violations[i].Line < report.Violations[j].Line
	})
	return report, nil
}

// scanFileForMapRanges walks file's RangeStmt nodes and emits violations
// into report for any unannotated map-range callsite.
//
// To correctly handle Go's scoping rules — and avoid false positives where
// a function-parameter slice shadows a struct-field map of the same name —
// we walk each function declaration with a function-local identifier-type
// table that includes parameters, results, and locally declared variables.
// The function-local table takes precedence over the file-level table
// (which holds struct fields, top-level vars, and methods' receivers).
func scanFileForMapRanges(
	file *ast.File,
	fset *token.FileSet,
	rel string,
	lines []string,
	fileTable map[string]string,
	report *MapIterationReport,
) {
	// Lint package's own files are exempt — they contain fixture loops
	// and meta-iterations that would create circular dependencies.
	if strings.HasPrefix(filepath.ToSlash(rel), "internal/dispatch/lint/") {
		return
	}

	// Walk top-level declarations. For each function, build a function-
	// local scope table; otherwise fall back to file-level table.
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok {
			// Top-level non-function decls cannot contain RangeStmt.
			continue
		}
		fnTable := buildFunctionLocalTable(fn)
		// Compose: function-local first, file-level fallback.
		scope := func(name string) (string, bool) {
			if t, ok := fnTable[name]; ok {
				return t, true
			}
			if t, ok := fileTable[name]; ok {
				return t, true
			}
			return "", false
		}
		ast.Inspect(fn, func(n ast.Node) bool {
			rs, ok := n.(*ast.RangeStmt)
			if !ok {
				return true
			}
			typeStr := classifyRangeExprScoped(rs.X, scope)
			switch {
			case typeStr == "?call" || typeStr == "?index" || typeStr == "?":
				report.SkippedUnknown++
				return true
			case strings.HasPrefix(typeStr, "map["):
				report.MapRanges++
				if !isAnnotated(rs, fset, lines) {
					pos := fset.Position(rs.Pos())
					report.Violations = append(report.Violations, MapIterationViolation{
						File:     rel,
						Line:     pos.Line,
						Iterated: exprText(rs.X),
						TypeStr:  typeStr,
					})
				}
			}
			return true
		})
	}
}

// buildFunctionLocalTable returns a name -> textual-type table for
// identifiers declared within fn's scope: receiver, parameters, results,
// and any local var/short-decl/type-spec inside the function body.
func buildFunctionLocalTable(fn *ast.FuncDecl) map[string]string {
	out := map[string]string{}
	// Receiver, parameters, results.
	if fn.Recv != nil {
		for _, f := range fn.Recv.List {
			ts := exprText(f.Type)
			for _, nm := range f.Names {
				out[nm.Name] = ts
			}
		}
	}
	if fn.Type.Params != nil {
		for _, f := range fn.Type.Params.List {
			ts := exprText(f.Type)
			for _, nm := range f.Names {
				out[nm.Name] = ts
			}
		}
	}
	if fn.Type.Results != nil {
		for _, f := range fn.Type.Results.List {
			ts := exprText(f.Type)
			for _, nm := range f.Names {
				out[nm.Name] = ts
			}
		}
	}
	// Body — short-decls and var-decls. We do NOT track scoping within
	// nested blocks; a single per-function table is sufficient because
	// Go disallows redeclaration with a different type in nested scope
	// where shadowing changes the semantic, AND because the lint only
	// flags maps (the false-positive risk we care about is slice-shadows-
	// map within one function, which the per-function table prevents).
	if fn.Body == nil {
		return out
	}
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		switch d := n.(type) {
		case *ast.AssignStmt:
			if d.Tok != token.DEFINE {
				return true
			}
			for i, lhs := range d.Lhs {
				id, ok := lhs.(*ast.Ident)
				if !ok || i >= len(d.Rhs) {
					continue
				}
				out[id.Name] = inferTypeFromExpr(d.Rhs[i])
			}
		case *ast.DeclStmt:
			gen, ok := d.Decl.(*ast.GenDecl)
			if !ok || gen.Tok != token.VAR {
				return true
			}
			for _, sp := range gen.Specs {
				vs, ok := sp.(*ast.ValueSpec)
				if !ok {
					continue
				}
				if vs.Type != nil {
					ts := exprText(vs.Type)
					for _, nm := range vs.Names {
						out[nm.Name] = ts
					}
				} else {
					for i, nm := range vs.Names {
						if i < len(vs.Values) {
							out[nm.Name] = inferTypeFromExpr(vs.Values[i])
						}
					}
				}
			}
		}
		return true
	})
	return out
}

// classifyRangeExprScoped is like classifyRangeExpr but uses a scoped
// lookup function so function-local declarations shadow file-level ones.
func classifyRangeExprScoped(e ast.Expr, lookup func(string) (string, bool)) string {
	switch v := e.(type) {
	case *ast.Ident:
		if t, ok := lookup(v.Name); ok {
			return t
		}
		return "?"
	case *ast.SelectorExpr:
		if t, ok := lookup(v.Sel.Name); ok {
			return t
		}
		return "?"
	case *ast.CallExpr:
		return "?call"
	case *ast.IndexExpr:
		return "?index"
	}
	return "?"
}

// isAnnotated returns true if the range statement carries one of the
// accepted annotations (see file-level docstring).
func isAnnotated(rs *ast.RangeStmt, fset *token.FileSet, lines []string) bool {
	pos := fset.Position(rs.Pos())
	if pos.Line < 1 || pos.Line > len(lines) {
		return false
	}

	// (3) Iterated identifier name contains "sorted".
	if name := strings.ToLower(exprText(rs.X)); strings.Contains(name, "sorted") {
		return true
	}

	// (1) and (2) — scan up to 5 lines back.
	const window = 5
	startLine := pos.Line - window
	if startLine < 1 {
		startLine = 1
	}
	// Include the for-statement line itself for inline `// safe: ...`.
	for ln := startLine; ln <= pos.Line; ln++ {
		text := lines[ln-1]
		// Strip code from comment by finding `//` outside of strings —
		// good-enough heuristic for normal source files.
		idx := strings.Index(text, "//")
		if idx >= 0 {
			comment := text[idx:]
			if strings.Contains(strings.ToLower(comment), "// safe:") {
				return true
			}
			if strings.Contains(comment, "sort.") {
				return true
			}
		}
		// Also recognize a bare sort.* call on the line itself (no comment).
		if strings.Contains(text, "sort.") {
			return true
		}
	}
	return false
}

// buildFileLevelTable builds a name -> textual-type table containing only
// file-level scope: struct fields and top-level var/const declarations.
// Function bodies and parameter scopes are handled separately by
// buildFunctionLocalTable; including them here would cause function-local
// shadowing to leak across functions and produce false positives.
func buildFileLevelTable(file *ast.File) map[string]string {
	out := map[string]string{}
	for _, decl := range file.Decls {
		switch d := decl.(type) {
		case *ast.GenDecl:
			for _, sp := range d.Specs {
				switch s := sp.(type) {
				case *ast.TypeSpec:
					if st, ok := s.Type.(*ast.StructType); ok {
						for _, f := range st.Fields.List {
							ts := exprText(f.Type)
							for _, nm := range f.Names {
								out[nm.Name] = ts
							}
						}
					}
				case *ast.ValueSpec:
					if d.Tok != token.VAR && d.Tok != token.CONST {
						continue
					}
					if s.Type != nil {
						ts := exprText(s.Type)
						for _, nm := range s.Names {
							out[nm.Name] = ts
						}
					} else {
						for i, nm := range s.Names {
							if i < len(s.Values) {
								out[nm.Name] = inferTypeFromExpr(s.Values[i])
							}
						}
					}
				}
			}
		}
	}
	return out
}

// inferTypeFromExpr returns a textual type for an RHS expression,
// recognizing the common patterns: composite literals, make(T, ...),
// and explicit MapType.
func inferTypeFromExpr(e ast.Expr) string {
	switch v := e.(type) {
	case *ast.CallExpr:
		if id, ok := v.Fun.(*ast.Ident); ok && id.Name == "make" && len(v.Args) >= 1 {
			return exprText(v.Args[0])
		}
		return "?"
	case *ast.CompositeLit:
		return exprText(v.Type)
	case *ast.MapType:
		return exprText(v)
	}
	return "?"
}

// exprText renders an ast.Expr as a short textual type form. Stable enough
// for prefix matching against "map[".
func exprText(e ast.Expr) string {
	switch v := e.(type) {
	case nil:
		return "?"
	case *ast.Ident:
		return v.Name
	case *ast.SelectorExpr:
		return exprText(v.X) + "." + v.Sel.Name
	case *ast.StarExpr:
		return "*" + exprText(v.X)
	case *ast.ArrayType:
		return "[]" + exprText(v.Elt)
	case *ast.MapType:
		return "map[" + exprText(v.Key) + "]" + exprText(v.Value)
	case *ast.ChanType:
		return "chan " + exprText(v.Value)
	case *ast.InterfaceType:
		return "interface{}"
	case *ast.FuncType:
		return "func(...)"
	case *ast.IndexExpr:
		return exprText(v.X) + "[?]"
	case *ast.CallExpr:
		return exprText(v.Fun) + "(...)"
	case *ast.ParenExpr:
		return "(" + exprText(v.X) + ")"
	case *ast.Ellipsis:
		return "..." + exprText(v.Elt)
	case *ast.StructType:
		return "struct{}"
	}
	return ""
}
