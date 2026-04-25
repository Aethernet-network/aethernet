package lint

import (
	"fmt"
	"go/ast"
	"go/types"
	"sort"
	"strings"

	"golang.org/x/tools/go/packages"
)

// derivationInputsTypeName is the unqualified type name the construction
// check matches. The full type identity is verified by walking up the
// resolved *types.Named to the package's import path.
const derivationInputsTypeName = "DerivationInputs"

// derivationInputsPackageSuffix is the import-path suffix that identifies
// the derivation package whose DerivationInputs type is under audit.
// Used both to (a) identify the type at the construction site and (b)
// exclude construction sites that are themselves inside the derivation
// package (in-package access is allowed).
const derivationInputsPackageSuffix = "/internal/settlement/derivation"

// DerivationInputsConstruction is a single composite-literal construction
// of derivation.DerivationInputs found outside the derivation package.
//
// Per multi-AI Item 1 composite (2026-04-25), all such sites are
// violations: external callers MUST go through derivation.NewDerivationInputs.
// The unexported fields prevent field assignment from outside, but a
// zero-value composite literal `derivation.DerivationInputs{}` would
// still compile — this check catches that residual surface.
type DerivationInputsConstruction struct {
	// File is the manifest-relative path of the construction site.
	File string

	// Line is the 1-based line of the composite literal's `{` brace.
	Line int

	// PackagePath is the full Go import path of the package containing
	// the violation (diagnostic use).
	PackagePath string

	// AbsFile is the absolute filesystem path (used for pragma reads if
	// suppression is added later).
	AbsFile string
}

// extractDerivationInputsConstructions walks every loaded in-scope
// package and returns every `*ast.CompositeLit` whose type resolves to
// `<module>/internal/settlement/derivation.DerivationInputs` AND whose
// containing package is NOT the derivation package itself.
//
// The check uses the type-resolved identity (pkg.TypesInfo.TypeOf) — not
// a syntactic string match — so import aliases (e.g.,
// `derivation2 "github.com/Aethernet-network/aethernet/internal/settlement/derivation"`)
// are caught correctly.
//
// Test files are deliberately scanned in addition to production files:
// in-scope tests outside the derivation package have no legitimate
// reason to bypass the constructor (the constructor accepts the same
// canonical-frozen primitives that test fakes already produce).
// In-derivation-package tests are excluded by the package-identity
// check.
func extractDerivationInputsConstructions(
	pkgs []*packages.Package,
	moduleImportPath, moduleRoot string,
) []DerivationInputsConstruction {
	var sites []DerivationInputsConstruction

	for _, pkg := range pkgs {
		if pkgIsDerivation(pkg.PkgPath) {
			continue
		}
		if !inScope(pkg.PkgPath, moduleImportPath) {
			continue
		}
		if pkg.TypesInfo == nil {
			continue
		}
		for _, file := range pkg.Syntax {
			absPath := pkg.Fset.Position(file.Pos()).Filename
			if strings.Contains(absPath, "/testdata/") {
				continue
			}
			relPath := manifestRelPath(absPath, moduleRoot)
			ast.Inspect(file, func(n ast.Node) bool {
				cl, ok := n.(*ast.CompositeLit)
				if !ok || cl.Type == nil {
					return true
				}
				if !isDerivationInputsType(pkg.TypesInfo.TypeOf(cl.Type)) {
					return true
				}
				pos := pkg.Fset.Position(cl.Lbrace)
				sites = append(sites, DerivationInputsConstruction{
					File:        relPath,
					Line:        pos.Line,
					PackagePath: pkg.PkgPath,
					AbsFile:     absPath,
				})
				return true
			})
		}
	}

	sort.Slice(sites, func(i, j int) bool {
		if sites[i].File != sites[j].File {
			return sites[i].File < sites[j].File
		}
		return sites[i].Line < sites[j].Line
	})
	return sites
}

// pkgIsDerivation reports whether a package's import path identifies the
// derivation package whose DerivationInputs type is under audit. Same
// matching rule the type-identity check uses, so the two stay in sync.
func pkgIsDerivation(pkgPath string) bool {
	if pkgPath == "" {
		return false
	}
	return strings.HasSuffix(pkgPath, derivationInputsPackageSuffix)
}

// isDerivationInputsType reports whether the resolved type identifies
// the DerivationInputs type defined in the derivation package. Resolves
// via the type's *types.Named.Obj().Pkg() so import aliases at the use
// site are transparent.
func isDerivationInputsType(t types.Type) bool {
	if t == nil {
		return false
	}
	named, ok := t.(*types.Named)
	if !ok {
		return false
	}
	obj := named.Obj()
	if obj == nil || obj.Name() != derivationInputsTypeName {
		return false
	}
	pkg := obj.Pkg()
	if pkg == nil {
		return false
	}
	return pkgIsDerivation(pkg.Path())
}

// formatDerivationInputsConstruction renders a single violation
// diagnostic. Mirrors the formatting style of formatUndeclared.
func formatDerivationInputsConstruction(c DerivationInputsConstruction) string {
	return fmt.Sprintf(`settlement/lint: ILLEGAL DerivationInputs CONSTRUCTION
  Location:    %s:%d
  Package:     %s
  Diagnosis:   composite-literal construction of derivation.DerivationInputs is
               forbidden outside the derivation package itself. The unexported
               fields prevent field assignment from outside the package, but a
               zero-value composite literal still compiles — this lint catches
               that residual surface.
  Remediation: replace with derivation.NewDerivationInputs(...). The constructor
               performs §2.1 contract validation (TreasuryID identity, required
               services non-nil, activation EventIDs accepted as-is) at the
               boundary and is the only supported external construction path.
               See internal/settlement/derivation/inputs.go and the multi-AI
               Item 1 composite design (2026-04-25) for context.
`, c.File, c.Line, c.PackagePath)
}
