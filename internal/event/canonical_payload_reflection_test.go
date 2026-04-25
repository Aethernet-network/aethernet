package event

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// canonicalPayloadReflectTypes is the authoritative reflection-time list
// of the 18 canonical event payload types. Kept in sync with the AST
// lint's hardcoded list at internal/event/lint/canonical_float_lint.go
// via TestCanonicalPayloadList_Complete below.
//
// Adding a new canonical payload type requires adding it here AND to the
// lint's list. Removing a type from either is a breaking protocol change;
// see the plan document (docs/plans/2026-04-20-canonical-distribution-
// integer-migration-v2.md §4.4) before editing.
var canonicalPayloadReflectTypes = []reflect.Type{
	reflect.TypeOf(TransferPayload{}),
	reflect.TypeOf(GenerationPayload{}),
	reflect.TypeOf(AttestationPayload{}),
	reflect.TypeOf(VerificationPayload{}),
	reflect.TypeOf(DelegationPayload{}),
	reflect.TypeOf(RegistrationPayload{}),
	reflect.TypeOf(GenesisFundingPayload{}),
	reflect.TypeOf(TaskPostedPayload{}),
	reflect.TypeOf(TaskClaimedPayload{}),
	reflect.TypeOf(TaskSubmittedPayload{}),
	reflect.TypeOf(TaskApprovedPayload{}),
	reflect.TypeOf(TaskDisputedPayload{}),
	reflect.TypeOf(TaskVerificationVotePayload{}),
	reflect.TypeOf(TaskVerificationConsensusPayload{}),
	reflect.TypeOf(SlashingChallengePayload{}),
	reflect.TypeOf(PrerequisiteWithholdingPayload{}),
	reflect.TypeOf(TrajectoryCommitPayload{}),
	reflect.TypeOf(IntegerMigrationActivationPayload{}),
	reflect.TypeOf(EpochBoundaryPayload{}),
}

// TestCanonicalPayloadTypes_FloatFree is the runtime-mechanism defense
// for the canonical-payload float-freedom invariant. Walks each of the 18
// types via reflect and asserts no float fields exist transitively. Uses
// a different mechanism (reflection) than the AST lint at internal/event/
// lint/canonical_float_lint.go, so a bug in one defense does not mask a
// regression that the other catches.
func TestCanonicalPayloadTypes_FloatFree(t *testing.T) {
	for _, typ := range canonicalPayloadReflectTypes {
		visited := map[reflect.Type]bool{}
		if v := findFloatFields(typ, typ.Name(), visited); len(v) > 0 {
			for _, s := range v {
				t.Errorf("%s", s)
			}
		}
	}
}

// findFloatFields returns a slice of human-readable violation descriptions
// for any float-bearing field paths reachable from t. Mirrors the AST
// lint's traversal rules.
func findFloatFields(t reflect.Type, path string, visited map[reflect.Type]bool) []string {
	if visited[t] {
		return nil
	}
	visited[t] = true

	// json.RawMessage check — flag by fully-qualified name even though its
	// underlying type is []byte (which is otherwise safe).
	if t == reflect.TypeOf(json.RawMessage(nil)) {
		return []string{path + ": json.RawMessage (may encode floats)"}
	}

	switch t.Kind() {
	case reflect.Float32, reflect.Float64:
		return []string{path + ": " + t.Kind().String()}
	case reflect.Complex64, reflect.Complex128:
		return []string{path + ": " + t.Kind().String()}
	case reflect.Interface:
		return []string{path + ": interface (may hold floats at runtime)"}
	case reflect.Ptr:
		return findFloatFields(t.Elem(), path+"*", visited)
	case reflect.Slice, reflect.Array:
		return findFloatFields(t.Elem(), path+"[]", visited)
	case reflect.Map:
		out := findFloatFields(t.Key(), path+"[<key>]", visited)
		return append(out, findFloatFields(t.Elem(), path+"[<val>]", visited)...)
	case reflect.Struct:
		var out []string
		for i := 0; i < t.NumField(); i++ {
			f := t.Field(i)
			out = append(out, findFloatFields(f.Type, path+"."+f.Name, visited)...)
		}
		return out
	}
	return nil
}

// TestCanonicalPayloadList_Complete enforces drift: every type declared
// in internal/event/ with a name ending in "Payload" must appear in the
// canonicalPayloadReflectTypes list. Prevents silent addition of a new
// canonical event payload type that escapes lint + reflection coverage.
func TestCanonicalPayloadList_Complete(t *testing.T) {
	declared := scanEventPackagePayloadTypes(t)
	listed := make(map[string]bool, len(canonicalPayloadReflectTypes))
	for _, rt := range canonicalPayloadReflectTypes {
		listed[rt.Name()] = true
	}

	for _, name := range declared {
		if !listed[name] {
			t.Errorf(
				"payload type %q declared in internal/event/ but missing from "+
					"canonicalPayloadReflectTypes (in %s) AND the AST lint's "+
					"canonicalPayloadTypeNames (in internal/event/lint/"+
					"canonical_float_lint.go) — add to both lists to bring it under "+
					"the float-freedom invariant, or rename the type if it is "+
					"genuinely not a canonical event payload.",
				name, "internal/event/canonical_payload_reflection_test.go",
			)
		}
	}
	for _, rt := range canonicalPayloadReflectTypes {
		found := false
		for _, d := range declared {
			if d == rt.Name() {
				found = true
				break
			}
		}
		if !found {
			t.Errorf(
				"canonicalPayloadReflectTypes contains %q but no matching type "+
					"declaration was found in internal/event/. Did the type move "+
					"or get removed?",
				rt.Name(),
			)
		}
	}
}

// TestCanonicalPayloadList_Has19Entries pins the list length. Changing
// the count requires explicit intent (a future protocol change adding a
// new canonical payload type will update this test).
func TestCanonicalPayloadList_Has19Entries(t *testing.T) {
	if got := len(canonicalPayloadReflectTypes); got != 19 {
		t.Fatalf("canonicalPayloadReflectTypes len = %d; want 19", got)
	}
}

// scanEventPackagePayloadTypes parses every non-test .go file in
// internal/event/ and returns the names of type declarations whose name
// ends in "Payload". Used by TestCanonicalPayloadList_Complete to detect
// drift.
func scanEventPackagePayloadTypes(t *testing.T) []string {
	t.Helper()
	moduleRoot := findModuleRoot(t)
	eventDir := filepath.Join(moduleRoot, "internal", "event")
	entries, err := os.ReadDir(eventDir)
	if err != nil {
		t.Fatalf("read internal/event/: %v", err)
	}
	fset := token.NewFileSet()
	var names []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		// Skip the test-only injected file, if present (only appears when
		// the lint's own tests are in flight with an overlay).
		if strings.HasPrefix(e.Name(), "zz_testinjected") {
			continue
		}
		path := filepath.Join(eventDir, e.Name())
		f, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		ast.Inspect(f, func(n ast.Node) bool {
			ts, ok := n.(*ast.TypeSpec)
			if !ok {
				return true
			}
			if ts.Name == nil {
				return true
			}
			name := ts.Name.Name
			if !strings.HasSuffix(name, "Payload") {
				return true
			}
			// Only count struct type declarations; skip type aliases or
			// interface-named-Payload variants (none exist today, defensive).
			if _, ok := ts.Type.(*ast.StructType); !ok {
				return true
			}
			names = append(names, name)
			return true
		})
	}
	return names
}

// findModuleRoot walks up from the current directory until it finds go.mod.
// Duplicated locally so this test has no dependency on the lint package.
func findModuleRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("could not find module root (go.mod)")
		}
		dir = parent
	}
}
