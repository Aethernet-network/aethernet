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

	"golang.org/x/tools/go/packages"
)

// verifyTestRefs checks every Canonical entry in the registered set to
// ensure its IntegrationTestRef resolves to an actual
// `func Name(t *testing.T)` in the module. Per plan §9.4 PR-3 and
// step-3 §D4 (testref existence).
//
// Advisory entries with empty IntegrationTestRef pass trivially (V11
// permits Advisory to opt out).
//
// Returns the list of missing-ref diagnostics.
func verifyTestRefs(pkgs []*packages.Package, set *RegisteredSet, moduleRoot string, moduleImportPath string) []MissingRef {
	if set == nil {
		return nil
	}
	// Two-source test-function lookup:
	// 1. Loaded packages (catches test files the packages.Load resolved).
	// 2. Direct filesystem walk for _test.go files under the module root
	//    (catches test-only packages that packages.Load sometimes omits
	//    from pkgs when Tests: true is unreliable).
	testFuncs := collectTestFunctions(pkgs)
	if moduleRoot != "" && moduleImportPath != "" {
		mergeTestFunctionsFromFilesystem(testFuncs, moduleRoot, moduleImportPath)
	}

	var missing []MissingRef
	for _, entry := range set.All {
		if !entry.IsCanonical() {
			continue // Advisory may have empty testref
		}
		if entry.IntegrationTestRef == "" {
			// Canonical without testref: this is caught at registration
			// time by the registry's PR-3 validator (V6). The lint
			// trusts that — if it somehow got through, flag it.
			missing = append(missing, MissingRef{
				EntryName:         entry.Name,
				DeclaredRef:       "",
				SourceFile:        entry.SourceFile,
				SourceLine:        entry.SourceLine,
				ResolutionFailure: "Canonical entry has empty IntegrationTestRef; PR-3 registry validation should have rejected this at startup",
			})
			continue
		}
		pkgPath, symbol, ok := splitTestRef(entry.IntegrationTestRef)
		if !ok {
			missing = append(missing, MissingRef{
				EntryName:         entry.Name,
				DeclaredRef:       entry.IntegrationTestRef,
				SourceFile:        entry.SourceFile,
				SourceLine:        entry.SourceLine,
				ResolutionFailure: "malformed ref — expected '<import-path>.<symbol>' (a dot-separated selector)",
			})
			continue
		}
		names, loaded := testFuncs[pkgPath]
		if !loaded {
			missing = append(missing, MissingRef{
				EntryName:         entry.Name,
				DeclaredRef:       entry.IntegrationTestRef,
				SourceFile:        entry.SourceFile,
				SourceLine:        entry.SourceLine,
				ResolutionFailure: fmt.Sprintf("package %q not loaded or has no test files", pkgPath),
			})
			continue
		}
		if _, ok := names[symbol]; !ok {
			missing = append(missing, MissingRef{
				EntryName:         entry.Name,
				DeclaredRef:       entry.IntegrationTestRef,
				SourceFile:        entry.SourceFile,
				SourceLine:        entry.SourceLine,
				ResolutionFailure: fmt.Sprintf("symbol %q not found in package %q (expected a func(*testing.T))", symbol, pkgPath),
			})
			continue
		}
	}
	// Stable ordering for diagnostics.
	sort.Slice(missing, func(i, j int) bool {
		if missing[i].EntryName != missing[j].EntryName {
			return missing[i].EntryName < missing[j].EntryName
		}
		return missing[i].DeclaredRef < missing[j].DeclaredRef
	})
	return missing
}

// splitTestRef splits "<import-path>.<symbol>" into (pkgPath, symbol).
// The import path is everything before the LAST dot; the symbol is
// everything after. Returns ok=false for refs without a dot.
func splitTestRef(ref string) (pkgPath, symbol string, ok bool) {
	idx := strings.LastIndex(ref, ".")
	if idx <= 0 || idx == len(ref)-1 {
		return "", "", false
	}
	return ref[:idx], ref[idx+1:], true
}

// mergeTestFunctionsFromFilesystem walks moduleRoot and finds every
// `_test.go` file under directories nested under moduleRoot. For each
// file with a `package <X>` declaration, it parses Test* functions and
// indexes them under the import path derived from the file's location
// relative to moduleRoot (prefixed with moduleImportPath).
//
// This is a fallback for packages.Load edge cases with test-only
// packages. Errors during walk are silently skipped — the packages.Load
// path already covers the majority of cases; this supplements.
func mergeTestFunctionsFromFilesystem(index map[string]map[string]struct{}, moduleRoot, moduleImportPath string) {
	fset := token.NewFileSet()
	_ = filepath.WalkDir(moduleRoot, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			name := d.Name()
			// Skip vendor, hidden dirs, and testdata subtrees (testdata
			// tests belong to their own temp modules in our unit tests).
			if name == "vendor" || name == "node_modules" ||
				strings.HasPrefix(name, ".") || name == "testdata" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, "_test.go") {
			return nil
		}
		file, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if err != nil {
			return nil
		}
		// Derive import path: moduleImportPath + "/" + relative dir.
		relDir, err := filepath.Rel(moduleRoot, filepath.Dir(path))
		if err != nil {
			return nil
		}
		relDir = filepath.ToSlash(relDir)
		var pkgPath string
		if relDir == "." {
			pkgPath = moduleImportPath
		} else {
			pkgPath = moduleImportPath + "/" + relDir
		}
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok {
				continue
			}
			if fn.Recv != nil {
				continue
			}
			if !strings.HasPrefix(fn.Name.Name, "Test") {
				continue
			}
			if fn.Type == nil || fn.Type.Params == nil || len(fn.Type.Params.List) != 1 {
				continue
			}
			if index[pkgPath] == nil {
				index[pkgPath] = make(map[string]struct{})
			}
			index[pkgPath][fn.Name.Name] = struct{}{}
		}
		return nil
	})
}

// collectTestFunctions returns a map from package path to the set of
// `Test*` function names declared in that package's test files.
func collectTestFunctions(pkgs []*packages.Package) map[string]map[string]struct{} {
	out := make(map[string]map[string]struct{})
	for _, pkg := range pkgs {
		if pkg.Syntax == nil {
			continue
		}
		for _, file := range pkg.Syntax {
			// Test files are included in pkg.Syntax only when the package
			// was loaded with the appropriate mode. We check the filename
			// ending to identify _test.go files, and we also include
			// all Test* functions from ordinary files since nothing
			// forbids a stub Test func in a non-test file (unusual but
			// possible).
			filename := pkg.Fset.Position(file.Pos()).Filename
			_ = filename // not filtering by suffix; include any Test* func
			for _, decl := range file.Decls {
				fn, ok := decl.(*ast.FuncDecl)
				if !ok {
					continue
				}
				if fn.Recv != nil {
					continue // method, not top-level func
				}
				if !strings.HasPrefix(fn.Name.Name, "Test") {
					continue
				}
				// Signature check: func(t *testing.T).
				if fn.Type == nil || fn.Type.Params == nil || len(fn.Type.Params.List) != 1 {
					continue
				}
				// Accept if the single param exists; full type-check
				// on the *testing.T selector would require NeedTypesInfo
				// and we want cheap. The prefix Test* + single param is
				// the standard signature.
				if out[pkg.PkgPath] == nil {
					out[pkg.PkgPath] = make(map[string]struct{})
				}
				out[pkg.PkgPath][fn.Name.Name] = struct{}{}
			}
		}
	}
	return out
}
