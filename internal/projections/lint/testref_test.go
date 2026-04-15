package lint

import (
	"strings"
	"testing"
)

// TestTestRef_ResolvesValidRef verifies the happy path: a Canonical entry
// pointing at an existing Test* function in a loaded package passes.
func TestTestRef_ResolvesValidRef(t *testing.T) {
	dir := t.TempDir()
	writeSyntheticModule(t, dir, "example.com/m", map[string]string{
		"store/store.go": `package store

import "github.com/Aethernet-network/aethernet/internal/projections"

type S struct{}

func SProjection(s *S) projections.CanonicalProjection {
	return projections.CanonicalProjection{
		Name: "S", Package: "example.com/m/store", StoreType: "S",
		Classification: projections.Canonical,
		SourceEvents: []projections.EventType{"E"},
		LiveConsumerRef: "x.Live", ReplayConsumerRef: "x.Replay",
		ObservabilitySurface: projections.Surface{Kind: projections.SurfaceHealth},
		IntegrationTestRef: "example.com/m/integration.TestS_Works",
		Owner: "me", CreatedAt: "2026-04-15",
	}
}
`,
		"integration/integration_test.go": `package integration

import "testing"

func TestS_Works(t *testing.T) {}
`,
	}, moduleRoot(t))

	pkgs := loadModule(t, dir)
	set, _ := extractRegisteredSet(pkgs)
	missing := verifyTestRefs(pkgs, set, dir, "example.com/m")
	if len(missing) != 0 {
		t.Fatalf("valid testref should not produce missing-ref diagnostic; got: %+v", missing)
	}
}

// TestTestRef_MissingSymbol verifies that a Canonical entry pointing at
// a non-existent Test symbol is flagged.
func TestTestRef_MissingSymbol(t *testing.T) {
	dir := t.TempDir()
	writeSyntheticModule(t, dir, "example.com/m", map[string]string{
		"store/store.go": `package store

import "github.com/Aethernet-network/aethernet/internal/projections"

type S struct{}

func SProjection(s *S) projections.CanonicalProjection {
	return projections.CanonicalProjection{
		Name: "S", Package: "example.com/m/store", StoreType: "S",
		Classification: projections.Canonical,
		SourceEvents: []projections.EventType{"E"},
		LiveConsumerRef: "x.Live", ReplayConsumerRef: "x.Replay",
		ObservabilitySurface: projections.Surface{Kind: projections.SurfaceHealth},
		IntegrationTestRef: "example.com/m/integration.TestS_NotReal",
		Owner: "me", CreatedAt: "2026-04-15",
	}
}
`,
		"integration/integration_test.go": `package integration

import "testing"

func TestS_SomethingElse(t *testing.T) {}
`,
	}, moduleRoot(t))

	pkgs := loadModule(t, dir)
	set, _ := extractRegisteredSet(pkgs)
	missing := verifyTestRefs(pkgs, set, dir, "example.com/m")
	if len(missing) != 1 {
		t.Fatalf("want 1 missing-ref, got %d: %+v", len(missing), missing)
	}
	m := missing[0]
	if m.EntryName != "S" {
		t.Errorf("EntryName: want S, got %q", m.EntryName)
	}
	if !strings.Contains(m.ResolutionFailure, "TestS_NotReal") {
		t.Errorf("ResolutionFailure should mention the missing symbol: %q", m.ResolutionFailure)
	}
}

// TestTestRef_AdvisoryWithEmptyRefPasses verifies the Advisory exception
// per V11 — empty testref on an Advisory entry is not flagged.
func TestTestRef_AdvisoryWithEmptyRefPasses(t *testing.T) {
	dir := t.TempDir()
	writeSyntheticModule(t, dir, "example.com/m", map[string]string{
		"store/store.go": `package store

import "github.com/Aethernet-network/aethernet/internal/projections"

type A struct{}

func AProjection(a *A) projections.CanonicalProjection {
	return projections.CanonicalProjection{
		Name: "A", Package: "example.com/m/store", StoreType: "A",
		Classification: projections.Advisory,
		SourceEvents: []projections.EventType{"E"},
		LiveConsumerRef: "x.Live", ReplayConsumerRef: "x.Replay",
		ObservabilitySurface: projections.Surface{Kind: projections.SurfaceNone, Justification: "advisory test fixture; not canonical"},
		IntegrationTestRef: "",
		Owner: "me", CreatedAt: "2026-04-15",
	}
}
`,
	}, moduleRoot(t))

	pkgs := loadModule(t, dir)
	set, _ := extractRegisteredSet(pkgs)
	missing := verifyTestRefs(pkgs, set, dir, "example.com/m")
	if len(missing) != 0 {
		t.Fatalf("Advisory with empty testref should not be flagged; got: %+v", missing)
	}
}

// TestTestRef_MalformedRef verifies that a malformed ref (no dot) is
// flagged with a specific diagnostic.
func TestTestRef_MalformedRef(t *testing.T) {
	dir := t.TempDir()
	writeSyntheticModule(t, dir, "example.com/m", map[string]string{
		"store/store.go": `package store

import "github.com/Aethernet-network/aethernet/internal/projections"

type S struct{}

func SProjection(s *S) projections.CanonicalProjection {
	return projections.CanonicalProjection{
		Name: "S", Package: "example.com/m/store", StoreType: "S",
		Classification: projections.Canonical,
		SourceEvents: []projections.EventType{"E"},
		LiveConsumerRef: "x.Live", ReplayConsumerRef: "x.Replay",
		ObservabilitySurface: projections.Surface{Kind: projections.SurfaceHealth},
		IntegrationTestRef: "justASingleToken",
		Owner: "me", CreatedAt: "2026-04-15",
	}
}
`,
	}, moduleRoot(t))

	pkgs := loadModule(t, dir)
	set, _ := extractRegisteredSet(pkgs)
	missing := verifyTestRefs(pkgs, set, dir, "example.com/m")
	if len(missing) != 1 {
		t.Fatalf("malformed ref: want 1 missing record, got %d: %+v", len(missing), missing)
	}
	if !strings.Contains(missing[0].ResolutionFailure, "malformed") {
		t.Errorf("resolution failure should say malformed: %q", missing[0].ResolutionFailure)
	}
}

// TestTestRef_RealRepoAllRefsResolve verifies every Canonical step-2
// projection's IntegrationTestRef resolves to an actual Test* function
// in the real codebase. This is a regression guard — if any step-2
// entry points at a renamed or missing test, this test catches it.
func TestTestRef_RealRepoAllRefsResolve(t *testing.T) {
	pkgs, err := loadRealRepo(t)
	if err != nil {
		t.Fatalf("load real repo: %v", err)
	}
	set, warnings := extractRegisteredSet(pkgs)
	for _, w := range warnings {
		t.Logf("extractor warning (non-failure): %s", w)
	}
	root := moduleRoot(t)
	missing := verifyTestRefs(pkgs, set, root, "github.com/Aethernet-network/aethernet")
	if len(missing) != 0 {
		t.Fatalf("real repo has %d missing testref(s):\n%s", len(missing), formatMissingForTest(missing))
	}
}

func formatMissingForTest(ms []MissingRef) string {
	var b strings.Builder
	for _, m := range ms {
		b.WriteString("  - ")
		b.WriteString(m.EntryName)
		b.WriteString(" → ")
		b.WriteString(m.DeclaredRef)
		b.WriteString(": ")
		b.WriteString(m.ResolutionFailure)
		b.WriteString("\n")
	}
	return b.String()
}
