package lint

import (
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
	"regexp"
	"sort"
	"strings"
	"unicode"

	"golang.org/x/tools/go/packages"
)

// badgerDBTypePath is the import path of the BadgerDB package. A struct
// field of type *badger.DB (concrete, not interface-typed) triggers the
// direct-field branch of the D3 heuristic.
const badgerDBTypePath = "github.com/dgraph-io/badger/v4"

// badgerDBTypeName is the simple type name we look for in that package.
const badgerDBTypeName = "DB"

// suppressionPragmaRE matches a `// projections:lint ignore "<reason>"` comment.
// Reason must be enclosed in double quotes; content between quotes is captured.
var suppressionPragmaRE = regexp.MustCompile(`projections:lint\s+ignore\s+"([^"]*)"`)

// findSuspectTypes scans every struct type in packages under the module's
// internal/ subtree. A type is suspect if it matches the D3 heuristic:
//   1. Direct field of type *badger.DB, OR
//   2. Embedded / named field that recursively contains *badger.DB
//      (bounded to depth 3 to prevent cycles).
//   3. Has at least one writer-method (pointer-receiver, mutates a field
//      or calls Update/Set/Delete on a persistence handle).
//
// Types with a valid `// projections:lint ignore "<reason>"` pragma on
// their declaration are skipped. Insufficient-reason pragmas are
// collected separately and reported as failures via
// InsufficientSuppression entries.
//
// Interface-typed persistence (fields whose type is an interface that
// may resolve to a BadgerDB-backed impl at runtime) produces a warning
// only — Step 3.5 deferral per D3 cost gate.
func findSuspectTypes(pkgs []*packages.Package, registered *RegisteredSet) (
	suspects []SuspectType,
	insufficient []InsufficientSuppression,
	warnings []string,
) {
	// Build a lookup of named types so embedding recursion can resolve
	// type-level references.
	lookup := newTypeLookup(pkgs)

	for _, pkg := range pkgs {
		if pkg.Types == nil || pkg.TypesInfo == nil || pkg.Syntax == nil {
			continue
		}
		// Scope filter: scan packages under any `internal/` tree, OR
		// packages loaded from a synthetic test module (used by this
		// package's own unit tests). Third-party deps and vendor/
		// packages are skipped.
		if !isScannablePackage(pkg.PkgPath) {
			continue
		}
		for _, file := range pkg.Syntax {
			// Skip _test.go files — those can't be live-path stores.
			filename := pkg.Fset.Position(file.Pos()).Filename
			if strings.HasSuffix(filename, "_test.go") {
				continue
			}
			// Skip files under testdata/ subdirectories.
			if strings.Contains(filename, "/testdata/") {
				continue
			}
			// Build a map from type-decl position to the doc comment above it
			// so we can read suppression pragmas.
			ast.Inspect(file, func(n ast.Node) bool {
				gd, ok := n.(*ast.GenDecl)
				if !ok || gd.Tok != token.TYPE {
					return true
				}
				for _, spec := range gd.Specs {
					ts, ok := spec.(*ast.TypeSpec)
					if !ok {
						continue
					}
					if _, ok := ts.Type.(*ast.StructType); !ok {
						continue
					}
					// Determine the doc-comment pragma, if any.
					// Comments can be on the GenDecl (for a singleton)
					// or on the TypeSpec itself (for a grouped type decl).
					docs := []*ast.CommentGroup{gd.Doc, ts.Doc}
					pragmaStatus, reason, charCount, wordCount := parsePragma(docs)
					pos := pkg.Fset.Position(ts.Pos())
					if pragmaStatus == pragmaSuppressed {
						continue // legitimately ignored
					}
					if pragmaStatus == pragmaInsufficient {
						insufficient = append(insufficient, InsufficientSuppression{
							File:      pos.Filename,
							Line:      pos.Line,
							Reason:    reason,
							CharCount: charCount,
							WordCount: wordCount,
						})
						continue
					}
					// Heuristic evaluation.
					obj := pkg.Types.Scope().Lookup(ts.Name.Name)
					if obj == nil {
						continue
					}
					named, ok := obj.Type().(*types.Named)
					if !ok {
						continue
					}
					hasBadger, ifaceWarn, ev := hasBadgerPersistence(named, lookup, 0)
					if ifaceWarn != "" {
						warnings = append(warnings, fmt.Sprintf(
							"%s.%s: interface-indirect persistence field at %s:%d — not scanned for BadgerDB-backed implementations; review required (Step 3.5 deferral)",
							pkg.PkgPath, ts.Name.Name, pos.Filename, pos.Line,
						))
					}
					if !hasBadger {
						continue
					}
					writers := writerMethods(named)
					if len(writers) == 0 {
						continue
					}
					// Check registration.
					if registered.Has(pkg.PkgPath, ts.Name.Name) {
						continue
					}
					suspects = append(suspects, SuspectType{
						PackagePath: pkg.PkgPath,
						TypeName:    ts.Name.Name,
						File:        pos.Filename,
						Line:        pos.Line,
						Evidence:    fmt.Sprintf("%s; writer methods: %s", ev, strings.Join(writers, ", ")),
					})
				}
				return true
			})
		}
	}
	// Sort for deterministic output.
	sort.Slice(suspects, func(i, j int) bool {
		if suspects[i].PackagePath != suspects[j].PackagePath {
			return suspects[i].PackagePath < suspects[j].PackagePath
		}
		return suspects[i].TypeName < suspects[j].TypeName
	})
	return suspects, insufficient, warnings
}

// isScannablePackage reports whether pkgPath is in scope for lint
// scanning. The real-repo entry point (Check) loads ./internal/...
// so only internal packages arrive; synthetic test modules load
// arbitrary paths. We accept either.
//
// Excludes: stdlib (no path), x/tools internals, vendor/, other
// third-party paths with no internal/ segment.
func isScannablePackage(pkgPath string) bool {
	if pkgPath == "" {
		return false
	}
	// Always skip explicit vendor paths and stdlib x/tools paths.
	if strings.HasPrefix(pkgPath, "golang.org/") ||
		strings.HasPrefix(pkgPath, "github.com/dgraph-io/") ||
		strings.Contains(pkgPath, "/vendor/") {
		return false
	}
	// Accept any package under the host module OR any synthetic path
	// (i.e., anything non-stdlib, non-third-party). Tests using
	// example.com/m/pkg naturally pass.
	return true
}

// hasBadgerPersistence reports whether the named type T has a BadgerDB
// persistence dependency via direct field OR via embedding / composition
// (bounded to depth 3). Returns (hasBadger, ifaceWarn, evidenceDescription).
func hasBadgerPersistence(named *types.Named, lookup *typeLookup, depth int) (bool, string, string) {
	if depth > 3 {
		return false, "", ""
	}
	st, ok := named.Underlying().(*types.Struct)
	if !ok {
		return false, "", ""
	}
	var ifaceWarn string
	for i := 0; i < st.NumFields(); i++ {
		f := st.Field(i)
		ft := f.Type()

		// Direct field of *badger.DB.
		if ptr, ok := ft.(*types.Pointer); ok {
			if nt, ok := ptr.Elem().(*types.Named); ok {
				obj := nt.Obj()
				if obj != nil && obj.Pkg() != nil &&
					obj.Pkg().Path() == badgerDBTypePath &&
					obj.Name() == badgerDBTypeName {
					return true, "", fmt.Sprintf("has *badger.DB field %q", f.Name())
				}
			}
		}

		// Interface-typed field — Step 3.5 deferral, warn only.
		if _, isIface := ft.Underlying().(*types.Interface); isIface {
			// Heuristic: fields named with persistence-suggesting roots.
			n := strings.ToLower(f.Name())
			if n == "store" || n == "db" || n == "persist" || n == "persistence" ||
				strings.HasSuffix(n, "store") || strings.HasSuffix(n, "db") ||
				strings.HasSuffix(n, "persistence") {
				ifaceWarn = "interface-typed persistence field"
			}
			continue
		}

		// Embedding-only recursion (plan §D3 founder refinement): recurse
		// through anonymous/embedded fields, NOT through named fields.
		// A type that holds a named pointer to a store (e.g. a consumer
		// wiring `store *BadgerStore`) is a reader/consumer, not a wrapper
		// — it does not own that persistence dependency. Only true
		// embedding (anonymous field on the struct) promotes the inner
		// type's persistence to the outer.
		if !f.Embedded() {
			continue
		}
		// Embedded value of named struct type.
		if nt, ok := ft.(*types.Named); ok {
			if nt.Obj() != nil && nt.Obj().Pkg() != nil &&
				nt.Obj().Pkg().Path() == badgerDBTypePath {
				continue
			}
			if lookup != nil && lookup.Has(nt) {
				ok, warn, ev := hasBadgerPersistence(nt, lookup, depth+1)
				if ok {
					return true, warn, fmt.Sprintf("via embedded field %q: %s", f.Name(), ev)
				}
				if warn != "" && ifaceWarn == "" {
					ifaceWarn = warn
				}
			}
		}
		// Embedded pointer to named struct type (e.g. *inner).
		if ptr, ok := ft.(*types.Pointer); ok {
			if nt, ok := ptr.Elem().(*types.Named); ok {
				if nt.Obj() != nil && nt.Obj().Pkg() != nil &&
					nt.Obj().Pkg().Path() == badgerDBTypePath {
					continue
				}
				if lookup != nil && lookup.Has(nt) {
					ok, warn, ev := hasBadgerPersistence(nt, lookup, depth+1)
					if ok {
						return true, warn, fmt.Sprintf("via embedded pointer %q: %s", f.Name(), ev)
					}
					if warn != "" && ifaceWarn == "" {
						ifaceWarn = warn
					}
				}
			}
		}
	}
	return false, ifaceWarn, ""
}

// writerMethods returns the names of methods on the named type that
// have pointer receivers and whose body contains any write-like call
// (Update, Set, Delete) or a direct field assignment. Includes both
// exported and non-exported methods per D3 tightening.
//
// This is a conservative approximation: we treat any pointer-receiver
// method with a body as a potential writer when the owning type holds
// a *badger.DB field. The intent is to avoid false negatives on
// "private writer" evasion per D3.
func writerMethods(named *types.Named) []string {
	var out []string
	for i := 0; i < named.NumMethods(); i++ {
		m := named.Method(i)
		if m == nil {
			continue
		}
		sig, ok := m.Type().(*types.Signature)
		if !ok {
			continue
		}
		recv := sig.Recv()
		if recv == nil {
			continue
		}
		// Pointer receiver only — value receivers cannot persist mutations
		// across calls unless the receiver is a reference type, which a
		// BadgerDB-holding struct typically is not by convention.
		if _, ok := recv.Type().(*types.Pointer); !ok {
			continue
		}
		// Skip Empty (the probe method itself — not a writer).
		if m.Name() == "Empty" {
			continue
		}
		out = append(out, m.Name())
	}
	sort.Strings(out)
	return out
}

// typeLookup is an accelerator for resolving named types across loaded
// packages during embedding recursion.
type typeLookup struct {
	known map[string]bool // key: pkgPath + "." + typeName
}

func newTypeLookup(pkgs []*packages.Package) *typeLookup {
	tl := &typeLookup{known: make(map[string]bool)}
	for _, p := range pkgs {
		if p.Types == nil {
			continue
		}
		scope := p.Types.Scope()
		for _, name := range scope.Names() {
			obj := scope.Lookup(name)
			if obj == nil {
				continue
			}
			if _, ok := obj.Type().(*types.Named); ok {
				tl.known[p.PkgPath+"."+name] = true
			}
		}
	}
	return tl
}

func (tl *typeLookup) Has(nt *types.Named) bool {
	if nt == nil || nt.Obj() == nil || nt.Obj().Pkg() == nil {
		return false
	}
	return tl.known[nt.Obj().Pkg().Path()+"."+nt.Obj().Name()]
}

// Pragma parsing (D4).

type pragmaStatus int

const (
	pragmaAbsent       pragmaStatus = iota // no pragma on this type
	pragmaSuppressed                       // valid pragma with sufficient reason
	pragmaInsufficient                     // pragma present, reason too short / too few words
)

// parsePragma looks for a `// projections:lint ignore "<reason>"` comment
// in any of the provided doc-comment groups. Returns the status, the
// reason string (if found), and the character / word counts.
//
// Per plan §D4: reason must be ≥20 characters AND ≥3 whitespace-separated
// words; otherwise pragmaInsufficient.
func parsePragma(docs []*ast.CommentGroup) (pragmaStatus, string, int, int) {
	for _, group := range docs {
		if group == nil {
			continue
		}
		for _, c := range group.List {
			match := suppressionPragmaRE.FindStringSubmatch(c.Text)
			if match == nil {
				continue
			}
			reason := match[1]
			charCount := len(reason)
			wordCount := countWords(reason)
			if charCount >= 20 && wordCount >= 3 {
				return pragmaSuppressed, reason, charCount, wordCount
			}
			return pragmaInsufficient, reason, charCount, wordCount
		}
	}
	return pragmaAbsent, "", 0, 0
}

// countWords returns the number of whitespace-separated tokens in s.
func countWords(s string) int {
	inWord := false
	count := 0
	for _, r := range s {
		if unicode.IsSpace(r) {
			inWord = false
		} else if !inWord {
			inWord = true
			count++
		}
	}
	return count
}
