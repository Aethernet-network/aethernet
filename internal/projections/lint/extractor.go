package lint

import (
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
	"strings"

	"golang.org/x/tools/go/packages"
)

// registeredProjectionsFQN is the fully-qualified name of the
// CanonicalProjection type. The extractor looks for functions that
// return this type.
const registeredProjectionsFQN = "github.com/Aethernet-network/aethernet/internal/projections.CanonicalProjection"

// RegisteredEntry is the static view of a registered projection, extracted
// by parsing a *Projection() constructor's return composite literal.
type RegisteredEntry struct {
	// PackagePath is the Go import path of the package that owns the
	// constructor.
	PackagePath string

	// StoreType is the value of the CanonicalProjection.StoreType field.
	StoreType string

	// Name is the value of CanonicalProjection.Name.
	Name string

	// Classification is the CanonicalProjection.Classification
	// enum name as spelled in source (e.g. "Canonical" or "Advisory").
	// Empty if not a simple selector expression the extractor can read.
	Classification string

	// IntegrationTestRef is the value of CanonicalProjection.IntegrationTestRef.
	IntegrationTestRef string

	// SourceFile and SourceLine locate the *Projection() constructor's
	// declaration.
	SourceFile string
	SourceLine int

	// ConstructorName is the name of the constructor function (for
	// diagnostic use).
	ConstructorName string

	// Opaque indicates that the extractor found a *Projection()
	// constructor but could not fold the returned struct literal
	// (non-literal expression, computed fields, etc.). Warning is
	// recorded in the Report; this entry counts as registered.
	Opaque bool

	// OpaqueReason explains why the entry is Opaque, if true.
	OpaqueReason string
}

// RegisteredSet is the extracted view of all *Projection() constructors
// across the module. Keyed by (PackagePath, StoreType).
type RegisteredSet struct {
	ByStoreType map[registeredKey]*RegisteredEntry
	All         []*RegisteredEntry
}

type registeredKey struct {
	pkgPath   string
	storeType string
}

// NewRegisteredSet returns an empty set.
func NewRegisteredSet() *RegisteredSet {
	return &RegisteredSet{
		ByStoreType: make(map[registeredKey]*RegisteredEntry),
	}
}

// Has reports whether a type with the given package path and name is
// registered. Matches the extractor key used by the matcher.
func (s *RegisteredSet) Has(pkgPath, storeType string) bool {
	_, ok := s.ByStoreType[registeredKey{pkgPath: pkgPath, storeType: storeType}]
	return ok
}

// extractRegisteredSet walks every package in pkgs and identifies
// functions whose return type is projections.CanonicalProjection,
// extracting the static fields of the returned composite literal.
//
// Per plan §D2: the static-AST approach covers the canonical shape
// (function returning a literal struct). Dynamic registration patterns
// (loops, conditionals, helper-builder returns) defeat the extractor
// and produce Opaque entries — flagged as warnings in the Report.
func extractRegisteredSet(pkgs []*packages.Package) (*RegisteredSet, []string) {
	set := NewRegisteredSet()
	var warnings []string

	for _, pkg := range pkgs {
		if pkg.Types == nil || pkg.TypesInfo == nil || pkg.Syntax == nil {
			continue
		}
		for i, file := range pkg.Syntax {
			_ = i
			for _, decl := range file.Decls {
				fn, ok := decl.(*ast.FuncDecl)
				if !ok || fn.Type == nil || fn.Type.Results == nil {
					continue
				}
				if !returnsCanonicalProjection(pkg, fn) {
					continue
				}
				entry, warn := extractEntryFromFunc(pkg, fn)
				if entry == nil {
					if warn != "" {
						warnings = append(warnings, warn)
					}
					continue
				}
				set.ByStoreType[registeredKey{pkgPath: pkg.PkgPath, storeType: entry.StoreType}] = entry
				set.All = append(set.All, entry)
				if warn != "" {
					warnings = append(warnings, warn)
				}
			}
		}
	}
	return set, warnings
}

// returnsCanonicalProjection reports whether fn's return type resolves
// to projections.CanonicalProjection. Uses type info rather than syntax
// so it is robust to aliasing and renamed imports.
func returnsCanonicalProjection(pkg *packages.Package, fn *ast.FuncDecl) bool {
	if fn.Type.Results == nil || len(fn.Type.Results.List) != 1 {
		return false
	}
	resultExpr := fn.Type.Results.List[0].Type
	t := pkg.TypesInfo.TypeOf(resultExpr)
	if t == nil {
		return false
	}
	named, ok := t.(*types.Named)
	if !ok {
		return false
	}
	obj := named.Obj()
	if obj == nil || obj.Pkg() == nil {
		return false
	}
	return obj.Pkg().Path() == "github.com/Aethernet-network/aethernet/internal/projections" &&
		obj.Name() == "CanonicalProjection"
}

// extractEntryFromFunc walks the body of a *Projection() function and
// returns the extracted entry. Returns (nil, "<warning>") if no
// return-statement composite literal is found, or if the return is a
// non-literal expression that the extractor cannot fold.
func extractEntryFromFunc(pkg *packages.Package, fn *ast.FuncDecl) (*RegisteredEntry, string) {
	if fn.Body == nil {
		return nil, ""
	}
	pos := pkg.Fset.Position(fn.Pos())
	entry := &RegisteredEntry{
		PackagePath:     pkg.PkgPath,
		ConstructorName: fn.Name.Name,
		SourceFile:      pos.Filename,
		SourceLine:      pos.Line,
	}

	// Walk the function body looking for `return projections.CanonicalProjection{...}`
	// or equivalent composite literals. Accept the first one.
	var lit *ast.CompositeLit
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		if lit != nil {
			return false // already found
		}
		ret, ok := n.(*ast.ReturnStmt)
		if !ok || len(ret.Results) != 1 {
			return true
		}
		cl, ok := ret.Results[0].(*ast.CompositeLit)
		if !ok {
			return true
		}
		// Verify the composite literal's type is CanonicalProjection.
		t := pkg.TypesInfo.TypeOf(cl)
		if t == nil {
			return true
		}
		named, ok := t.(*types.Named)
		if !ok {
			return true
		}
		if named.Obj() == nil || named.Obj().Pkg() == nil {
			return true
		}
		if named.Obj().Pkg().Path() != "github.com/Aethernet-network/aethernet/internal/projections" ||
			named.Obj().Name() != "CanonicalProjection" {
			return true
		}
		lit = cl
		return false
	})

	if lit == nil {
		entry.Opaque = true
		entry.OpaqueReason = "no return-statement composite literal found; " +
			"constructor returns a non-literal expression (computed, branched, or helper-built)"
		return entry, fmt.Sprintf(
			"%s.%s: opaque constructor — no literal CanonicalProjection{...} in return. "+
				"Static analysis cannot verify fields; code review must confirm registration completeness.",
			pkg.PkgPath, fn.Name.Name,
		)
	}

	// Walk the composite literal's elements and extract known string-literal fields.
	for _, el := range lit.Elts {
		kv, ok := el.(*ast.KeyValueExpr)
		if !ok {
			continue
		}
		keyIdent, ok := kv.Key.(*ast.Ident)
		if !ok {
			continue
		}
		switch keyIdent.Name {
		case "Name":
			if s, ok := stringLitValue(kv.Value); ok {
				entry.Name = s
			}
		case "StoreType":
			if s, ok := stringLitValue(kv.Value); ok {
				entry.StoreType = s
			}
		case "IntegrationTestRef":
			if s, ok := stringLitValue(kv.Value); ok {
				entry.IntegrationTestRef = s
			}
		case "Classification":
			// Read as a selector expression (projections.Canonical or
			// projections.Advisory). Fall back to identifier name if
			// unqualified.
			entry.Classification = classificationIdent(kv.Value)
		}
	}

	return entry, ""
}

// stringLitValue extracts the string value from a BasicLit of kind STRING.
// Returns ("", false) for non-literal expressions.
func stringLitValue(expr ast.Expr) (string, bool) {
	bl, ok := expr.(*ast.BasicLit)
	if !ok || bl.Kind != token.STRING {
		return "", false
	}
	// Strip surrounding quotes. ast.BasicLit values include them literally.
	v := bl.Value
	if len(v) >= 2 && (v[0] == '"' || v[0] == '`') {
		v = v[1 : len(v)-1]
	}
	return v, true
}

// classificationIdent returns the name of the Classification constant
// referenced by expr. Accepts selector expressions like
// "projections.Canonical" (returns "Canonical") or plain identifiers.
func classificationIdent(expr ast.Expr) string {
	switch e := expr.(type) {
	case *ast.SelectorExpr:
		return e.Sel.Name
	case *ast.Ident:
		return e.Name
	default:
		return ""
	}
}

// IsCanonical reports whether the entry is classified Canonical.
func (e *RegisteredEntry) IsCanonical() bool {
	return strings.EqualFold(e.Classification, "Canonical")
}
