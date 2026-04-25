package lint

import (
	"go/ast"
	"go/token"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"golang.org/x/tools/go/packages"
)

// scopedPackagePrefixes is the set of import-path suffixes the lint
// scans, per F5 5A.4.c scope. Out-of-scope packages
// (`internal/staking/`, `internal/fees/`, `internal/identity/`) are
// explicitly NOT scanned even when settlement code calls into them.
var scopedPackagePrefixes = []string{
	"internal/settlement",
	"internal/escrow",
	"internal/ledger",
	"internal/reputation",
}

// ReadSite is a single AST read-shape node found in scope.
type ReadSite struct {
	// File is the manifest-relative file path
	// (e.g. `internal/settlement/verification_consensus_settler.go`),
	// matching the convention used in the manifest's source_locations.
	File string

	// Line is the 1-based line number of the read.
	Line int

	// Kind identifies the AST node shape: "call", "selector", or "index".
	Kind string

	// Detail is a short description of the read (e.g. the selector name)
	// for diagnostic clarity.
	Detail string

	// PackagePath is the full Go import path (diagnostic use).
	PackagePath string

	// AbsFile is the absolute filesystem path of the file (used to read
	// suppression pragmas and to produce diagnostics).
	AbsFile string
}

// Key returns the canonical "file:line" string used for manifest lookup.
func (r ReadSite) Key() string {
	return r.File + ":" + strconv.Itoa(r.Line)
}

// extractReadSites walks every loaded package whose import path matches
// scopedPackagePrefixes and returns every read-shape AST node found in
// non-test, non-testdata source files.
//
// The extractor deliberately considers the right-hand-side of an
// assignment as the "read" — left-hand-side targets of assignment are
// NOT collected. This matches the manifest's role as an audit of READ
// sites (writes are tracked separately at the projection layer).
//
// `moduleImportPath` is the host module's import path (read from go.mod
// at the analyzer working dir) and `moduleRoot` is the absolute path to
// the module root. Together they let the extractor convert absolute
// file paths to manifest-relative paths.
func extractReadSites(pkgs []*packages.Package, moduleImportPath, moduleRoot string) []ReadSite {
	var sites []ReadSite

	for _, pkg := range pkgs {
		if !inScope(pkg.PkgPath, moduleImportPath) {
			continue
		}
		for _, file := range pkg.Syntax {
			absPath := pkg.Fset.Position(file.Pos()).Filename
			if shouldSkipFile(absPath) {
				continue
			}
			relPath := manifestRelPath(absPath, moduleRoot)
			sites = append(sites, extractFromFile(file, pkg.Fset, relPath, absPath, pkg.PkgPath)...)
		}
	}

	// Stable ordering for deterministic test output.
	sort.Slice(sites, func(i, j int) bool {
		if sites[i].File != sites[j].File {
			return sites[i].File < sites[j].File
		}
		if sites[i].Line != sites[j].Line {
			return sites[i].Line < sites[j].Line
		}
		if sites[i].Kind != sites[j].Kind {
			return sites[i].Kind < sites[j].Kind
		}
		return sites[i].Detail < sites[j].Detail
	})
	return sites
}

// extractActiveLines returns the set of file:line keys that contain ANY
// AST node activity in scope. Used by the stale-manifest check: a
// manifest source_locations entry pointing at a line with no AST node
// at all is unambiguously stale; one pointing at a line with an AST
// node (even just a struct-field declaration) might be a legitimate
// declaration-site reference (manifest convention permits this).
func extractActiveLines(pkgs []*packages.Package, moduleImportPath, moduleRoot string) map[string]bool {
	out := make(map[string]bool)
	for _, pkg := range pkgs {
		if !inScope(pkg.PkgPath, moduleImportPath) {
			continue
		}
		for _, file := range pkg.Syntax {
			absPath := pkg.Fset.Position(file.Pos()).Filename
			if shouldSkipFile(absPath) {
				continue
			}
			relPath := manifestRelPath(absPath, moduleRoot)
			ast.Inspect(file, func(n ast.Node) bool {
				if n == nil {
					return true
				}
				start := pkg.Fset.Position(n.Pos())
				end := pkg.Fset.Position(n.End())
				for ln := start.Line; ln <= end.Line; ln++ {
					out[relPath+":"+strconv.Itoa(ln)] = true
				}
				return true
			})
		}
	}
	return out
}

// extractFromFile walks a single file's AST and emits ReadSite entries
// for every CallExpr / SelectorExpr / IndexExpr that is a READ (not a
// write target on the LHS of an assignment, and not the .Fun of an
// enclosing CallExpr — that case is collapsed into the call site).
func extractFromFile(file *ast.File, fset *token.FileSet, relPath, absPath, pkgPath string) []ReadSite {
	// Pre-pass: build (a) LHS-write position set, (b) selector-as-call-fun
	// set, (c) index-as-call-fun set. Both sets are used to dedupe the
	// main pass so a `pkg.Func()` call is reported once as a call, not
	// also as a selector-read.
	lhsPositions := make(map[token.Pos]bool)
	callFunSelectors := make(map[*ast.SelectorExpr]bool)
	callFunIndexes := make(map[*ast.IndexExpr]bool)

	ast.Inspect(file, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.AssignStmt:
			for _, lhs := range x.Lhs {
				markLHS(lhs, lhsPositions)
			}
		case *ast.IncDecStmt:
			markLHS(x.X, lhsPositions)
		case *ast.CallExpr:
			switch fun := x.Fun.(type) {
			case *ast.SelectorExpr:
				callFunSelectors[fun] = true
			case *ast.IndexExpr:
				callFunIndexes[fun] = true
			}
		}
		return true
	})

	var sites []ReadSite

	ast.Inspect(file, func(n ast.Node) bool {
		if n == nil {
			return true
		}
		switch x := n.(type) {
		case *ast.CallExpr:
			pos := fset.Position(x.Lparen)
			sites = append(sites, ReadSite{
				File:        relPath,
				Line:        pos.Line,
				Kind:        "call",
				Detail:      callDetail(x),
				PackagePath: pkgPath,
				AbsFile:     absPath,
			})
		case *ast.SelectorExpr:
			if callFunSelectors[x] {
				return true // collapsed into the enclosing call
			}
			if lhsPositions[x.Pos()] {
				return true
			}
			pos := fset.Position(x.Sel.NamePos)
			sites = append(sites, ReadSite{
				File:        relPath,
				Line:        pos.Line,
				Kind:        "selector",
				Detail:      x.Sel.Name,
				PackagePath: pkgPath,
				AbsFile:     absPath,
			})
		case *ast.IndexExpr:
			if callFunIndexes[x] {
				return true
			}
			if lhsPositions[x.Pos()] {
				return true
			}
			pos := fset.Position(x.Lbrack)
			sites = append(sites, ReadSite{
				File:        relPath,
				Line:        pos.Line,
				Kind:        "index",
				Detail:      "index",
				PackagePath: pkgPath,
				AbsFile:     absPath,
			})
		}
		return true
	})
	return sites
}

// markLHS recursively marks every CallExpr / SelectorExpr / IndexExpr
// position appearing on the LHS of an assignment as a write target so
// the extractor's read-collection pass can skip them.
func markLHS(expr ast.Expr, into map[token.Pos]bool) {
	if expr == nil {
		return
	}
	into[expr.Pos()] = true
	switch x := expr.(type) {
	case *ast.SelectorExpr:
		markLHS(x.X, into)
	case *ast.IndexExpr:
		markLHS(x.X, into)
		// Note: x.Index is a READ even within an LHS expression
		// (e.g. m[k] = v reads k); we leave it unmarked so it is
		// captured as a read site.
	case *ast.StarExpr:
		markLHS(x.X, into)
	case *ast.ParenExpr:
		markLHS(x.X, into)
	}
}

// callDetail returns a short string describing a CallExpr (the called
// symbol's name when extractable; "<expr>" otherwise).
func callDetail(c *ast.CallExpr) string {
	switch f := c.Fun.(type) {
	case *ast.Ident:
		return f.Name + "()"
	case *ast.SelectorExpr:
		return f.Sel.Name + "()"
	default:
		return "<call>"
	}
}

// inScope reports whether pkgPath is under one of the scopedPackagePrefixes
// rooted at moduleImportPath. If moduleImportPath is "", we accept any
// pkgPath whose suffix matches (used by synthetic test modules).
func inScope(pkgPath, moduleImportPath string) bool {
	if pkgPath == "" {
		return false
	}
	for _, suffix := range scopedPackagePrefixes {
		if moduleImportPath != "" {
			if strings.HasPrefix(pkgPath, moduleImportPath+"/"+suffix) ||
				pkgPath == moduleImportPath+"/"+suffix {
				return true
			}
		}
		// Synthetic / module-agnostic match.
		if strings.HasSuffix(pkgPath, "/"+suffix) || strings.HasSuffix(pkgPath, suffix) {
			return true
		}
	}
	return false
}

// shouldSkipFile excludes test files, generated code, and testdata
// fixtures from extraction. Test files are excluded because the
// manifest enumerates production reads only.
func shouldSkipFile(absPath string) bool {
	if strings.HasSuffix(absPath, "_test.go") {
		return true
	}
	if strings.Contains(absPath, "/testdata/") {
		return true
	}
	return false
}

// manifestRelPath converts an absolute file path under moduleRoot to
// the manifest-relative form (e.g. "internal/settlement/foo.go"). If
// the path is not under moduleRoot, the absolute path is returned as a
// fallback (the lookup will simply not match the manifest, which is
// the correct behavior for out-of-tree files).
func manifestRelPath(absPath, moduleRoot string) string {
	if moduleRoot == "" {
		return absPath
	}
	rel, err := filepath.Rel(moduleRoot, absPath)
	if err != nil {
		return absPath
	}
	// Manifest uses forward slashes regardless of OS.
	return filepath.ToSlash(rel)
}
