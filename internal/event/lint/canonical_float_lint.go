// Package lint implements the canonical-payload float-freedom lint per
// the revised design in docs/plans/2026-04-20-canonical-distribution-
// integer-migration-v2.md §4.4.
//
// Invariant enforced: every canonical event payload type defined in
// internal/event/ is float-free transitively. No field (or nested field,
// or field reached through a pointer, slice, array, map, interface, or
// generic type parameter) may be float32 or float64.
//
// This lint is the compile-time defense; a reflection-based backstop
// lives at internal/event/canonical_payload_reflection_test.go.
//
// Pattern-conforms to internal/dispatch/lint/ and internal/projections/
// lint/: library Check() invoked from a test, which acts as the CI gate
// under go test ./...
package lint

import (
	"fmt"
	"go/types"
	"strings"

	"golang.org/x/tools/go/packages"
)

// canonicalPayloadTypeNames is the authoritative hardcoded list of the 17
// canonical event payload types defined in internal/event/ that must stay
// float-free. Adding a new canonical payload type requires updating this
// list AND the reflection-test list at
// internal/event/canonical_payload_reflection_test.go (kept in sync by
// TestCanonicalPayloadList_Complete in that file).
//
// Removing a type from this list is a breaking protocol change; see the
// plan document before editing.
var canonicalPayloadTypeNames = []string{
	"TransferPayload",
	"GenerationPayload",
	"AttestationPayload",
	"VerificationPayload",
	"DelegationPayload",
	"RegistrationPayload",
	"GenesisFundingPayload",
	"TaskPostedPayload",
	"TaskClaimedPayload",
	"TaskSubmittedPayload",
	"TaskApprovedPayload",
	"TaskDisputedPayload",
	"TaskVerificationVotePayload",
	"TaskVerificationConsensusPayload",
	"SlashingChallengePayload",
	"PrerequisiteWithholdingPayload",
	"TrajectoryCommitPayload",
	"IntegerMigrationActivationPayload",
}

// Violation records one offending field path within a canonical payload.
type Violation struct {
	TypeName  string // root canonical payload type, e.g. "VerificationPayload"
	FieldPath string // qualified path, e.g. "VerificationPayload.Nested.Score"
	Reason    string // human description, e.g. "float64" or "interface{} (may hold floats)"
}

func (v Violation) String() string {
	return fmt.Sprintf("%s: %s", v.FieldPath, v.Reason)
}

// Report is the lint outcome. HasFailures reports whether any violations
// were found.
type Report struct {
	Violations []Violation
}

// HasFailures reports whether the lint found any float-bearing fields.
func (r *Report) HasFailures() bool { return len(r.Violations) > 0 }

// Check loads internal/event/ from modulePath, walks every canonical
// payload type transitively, and returns a Report listing any
// float-bearing fields. Returns an error only for package-loading
// failures (not for lint violations — those populate Report).
//
// Extra *ast.File overlays can be supplied to inject synthetic source
// for the lint's own tests; production callers pass nil.
func Check(modulePath string, overlay map[string][]byte) (*Report, error) {
	cfg := &packages.Config{
		Dir: modulePath,
		Mode: packages.NeedName |
			packages.NeedFiles |
			packages.NeedCompiledGoFiles |
			packages.NeedImports |
			packages.NeedDeps |
			packages.NeedTypes |
			packages.NeedTypesInfo |
			packages.NeedSyntax,
		Overlay: overlay,
	}
	pkgs, err := packages.Load(cfg, "github.com/Aethernet-network/aethernet/internal/event")
	if err != nil {
		return nil, fmt.Errorf("lint: load package: %w", err)
	}
	if len(pkgs) != 1 {
		return nil, fmt.Errorf("lint: expected 1 package, got %d", len(pkgs))
	}
	pkg := pkgs[0]
	if len(pkg.Errors) > 0 {
		// Surface package-load errors — a broken event package would
		// silently defeat the lint.
		var sb strings.Builder
		sb.WriteString("lint: package errors:\n")
		for _, e := range pkg.Errors {
			sb.WriteString("  ")
			sb.WriteString(e.Error())
			sb.WriteString("\n")
		}
		return nil, fmt.Errorf("%s", sb.String())
	}

	report := &Report{}
	scope := pkg.Types.Scope()
	for _, name := range canonicalPayloadTypeNames {
		obj := scope.Lookup(name)
		if obj == nil {
			report.Violations = append(report.Violations, Violation{
				TypeName:  name,
				FieldPath: name,
				Reason:    "declared in canonical list but not found in internal/event/",
			})
			continue
		}
		named, ok := obj.Type().(*types.Named)
		if !ok {
			report.Violations = append(report.Violations, Violation{
				TypeName:  name,
				FieldPath: name,
				Reason:    fmt.Sprintf("canonical type is not a named type (got %T)", obj.Type()),
			})
			continue
		}
		visited := map[types.Type]bool{}
		walk(named, name, name, visited, report)
	}
	return report, nil
}

// walk recurses into t, appending violations to report. rootType is the
// canonical payload type at the top of this walk (used to populate
// Violation.TypeName); fieldPath is the dot-separated path of field
// accessors from the root. visited is the type-cycle guard.
func walk(t types.Type, rootType, fieldPath string, visited map[types.Type]bool, report *Report) {
	if visited[t] {
		return
	}
	visited[t] = true

	// Check for json.RawMessage by fully-qualified name before unwrapping
	// to the []byte underlying type.
	if named, ok := t.(*types.Named); ok {
		if isJSONRawMessage(named) {
			report.Violations = append(report.Violations, Violation{
				TypeName:  rootType,
				FieldPath: fieldPath,
				Reason:    "json.RawMessage (may encode floats)",
			})
			return
		}
	}

	switch u := t.Underlying().(type) {
	case *types.Basic:
		switch u.Kind() {
		case types.Float32, types.Float64,
			types.UntypedFloat, types.Complex64, types.Complex128, types.UntypedComplex:
			report.Violations = append(report.Violations, Violation{
				TypeName:  rootType,
				FieldPath: fieldPath,
				Reason:    u.String(),
			})
		}
	case *types.Struct:
		for i := 0; i < u.NumFields(); i++ {
			f := u.Field(i)
			walk(f.Type(), rootType, fieldPath+"."+f.Name(), visited, report)
		}
	case *types.Pointer:
		walk(u.Elem(), rootType, fieldPath+"*", visited, report)
	case *types.Slice:
		walk(u.Elem(), rootType, fieldPath+"[]", visited, report)
	case *types.Array:
		walk(u.Elem(), rootType, fieldPath+"[]", visited, report)
	case *types.Map:
		walk(u.Key(), rootType, fieldPath+"[<key>]", visited, report)
		walk(u.Elem(), rootType, fieldPath+"[<val>]", visited, report)
	case *types.Interface:
		report.Violations = append(report.Violations, Violation{
			TypeName:  rootType,
			FieldPath: fieldPath,
			Reason:    "interface (may hold floats at runtime)",
		})
	case *types.TypeParam:
		report.Violations = append(report.Violations, Violation{
			TypeName:  rootType,
			FieldPath: fieldPath,
			Reason:    "generic type parameter (may be instantiated with float)",
		})
	case *types.Signature, *types.Chan:
		// Functions and channels in canonical payloads are unusual but not
		// float-carrying on their own. Skip without violation.
	}
}

// isJSONRawMessage detects encoding/json.RawMessage by fully-qualified
// name rather than by its []byte underlying type, so plain []byte fields
// remain legal while RawMessage fields are flagged.
func isJSONRawMessage(n *types.Named) bool {
	obj := n.Obj()
	if obj == nil {
		return false
	}
	pkg := obj.Pkg()
	if pkg == nil {
		return false
	}
	return pkg.Path() == "encoding/json" && obj.Name() == "RawMessage"
}
