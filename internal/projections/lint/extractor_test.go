package lint

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/tools/go/packages"
)

// writeSyntheticModule writes a tiny Go module to dir with the given
// files. The module imports the real projections package from the host
// module by replacing the module path — tests that need the real
// projections type can use replace directives.
func writeSyntheticModule(t *testing.T, dir string, modulePath string, files map[string]string, hostModuleRoot string) {
	t.Helper()
	goMod := "module " + modulePath + "\n\ngo 1.25\n"
	if hostModuleRoot != "" {
		goMod += "\nrequire github.com/Aethernet-network/aethernet v0.0.0\n"
		goMod += "\nreplace github.com/Aethernet-network/aethernet => " + hostModuleRoot + "\n"
	}
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte(goMod), 0o644); err != nil {
		t.Fatalf("write go.mod: %v", err)
	}
	for name, content := range files {
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", p, err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", p, err)
		}
	}
	// `go mod tidy` to populate go.sum and resolve the replaced module so
	// packages.Load can type-check. Synthetic test modules don't need
	// network access since the only require is a local replace.
	cmd := exec.Command("go", "mod", "tidy")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GOFLAGS=-mod=mod")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go mod tidy in %s: %v\n%s", dir, err, out)
	}
}

// loadModule loads packages from the given directory.
func loadModule(t *testing.T, dir string) []*packages.Package {
	t.Helper()
	cfg := &packages.Config{
		Dir: dir,
		Mode: packages.NeedName |
			packages.NeedFiles |
			packages.NeedCompiledGoFiles |
			packages.NeedImports |
			packages.NeedTypes |
			packages.NeedTypesInfo |
			packages.NeedSyntax,
	}
	pkgs, err := packages.Load(cfg, "./...")
	if err != nil {
		t.Fatalf("packages.Load: %v", err)
	}
	return pkgs
}

// moduleRoot returns the host module root so synthetic testdata can
// `require` the real projections package.
func moduleRoot(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	// We are in internal/projections/lint. Walk up to the module root.
	return filepath.Join(wd, "..", "..", "..")
}

// TestExtractor_FindsCanonicalProjection builds a synthetic package with
// a *Projection() constructor returning a literal CanonicalProjection
// and asserts the extractor reads every required field correctly.
func TestExtractor_FindsCanonicalProjection(t *testing.T) {
	dir := t.TempDir()
	writeSyntheticModule(t, dir, "example.com/synthetic", map[string]string{
		"store/store.go": `package store

import (
	"context"

	"github.com/Aethernet-network/aethernet/internal/projections"
)

type MyStore struct{}

func MyStoreProjection(s *MyStore) projections.CanonicalProjection {
	return projections.CanonicalProjection{
		Name:                 "MyStore",
		Package:              "example.com/synthetic/store",
		StoreType:            "MyStore",
		Classification:       projections.Canonical,
		SourceEvents:         []projections.EventType{"TestEvent"},
		LiveConsumerRef:      "example.com/synthetic.LiveConsumer",
		ReplayConsumerRef:    "example.com/synthetic.ReplayConsumer",
		ObservabilitySurface: projections.Surface{Kind: projections.SurfaceHealth},
		IntegrationTestRef:   "example.com/synthetic/integration.TestMyStore_X",
		Owner:                "owner",
		CreatedAt:            "2026-04-15",
		StateProbe: func(ctx context.Context) (bool, error) {
			return false, nil
		},
	}
}
`,
	}, moduleRoot(t))

	pkgs := loadModule(t, dir)
	set, warnings := extractRegisteredSet(pkgs)

	if len(warnings) != 0 {
		t.Fatalf("expected no warnings, got: %v", warnings)
	}
	if !set.Has("example.com/synthetic/store", "MyStore") {
		t.Fatalf("extractor missed MyStore; got: %+v", set.All)
	}
	if len(set.All) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(set.All))
	}
	e := set.All[0]
	if e.Name != "MyStore" {
		t.Errorf("Name: want MyStore, got %q", e.Name)
	}
	if e.StoreType != "MyStore" {
		t.Errorf("StoreType: want MyStore, got %q", e.StoreType)
	}
	if e.Classification != "Canonical" {
		t.Errorf("Classification: want Canonical, got %q", e.Classification)
	}
	if !e.IsCanonical() {
		t.Errorf("IsCanonical(): want true")
	}
	if e.IntegrationTestRef != "example.com/synthetic/integration.TestMyStore_X" {
		t.Errorf("IntegrationTestRef: got %q", e.IntegrationTestRef)
	}
	if e.ConstructorName != "MyStoreProjection" {
		t.Errorf("ConstructorName: got %q", e.ConstructorName)
	}
}

// TestExtractor_AdvisoryEntry verifies Advisory classification extraction.
func TestExtractor_AdvisoryEntry(t *testing.T) {
	dir := t.TempDir()
	writeSyntheticModule(t, dir, "example.com/synthetic", map[string]string{
		"store/store.go": `package store

import "github.com/Aethernet-network/aethernet/internal/projections"

type AdvStore struct{}

func AdvStoreProjection(s *AdvStore) projections.CanonicalProjection {
	return projections.CanonicalProjection{
		Name:                 "AdvStore",
		Package:              "example.com/synthetic/store",
		StoreType:            "AdvStore",
		Classification:       projections.Advisory,
		SourceEvents:         []projections.EventType{"AdvEvt"},
		LiveConsumerRef:      "example.com/synthetic.LiveConsumer",
		ReplayConsumerRef:    "example.com/synthetic.ReplayConsumer",
		ObservabilitySurface: projections.Surface{Kind: projections.SurfaceNone, Justification: "advisory only for test"},
		IntegrationTestRef:   "",
		Owner:                "owner",
		CreatedAt:            "2026-04-15",
	}
}
`,
	}, moduleRoot(t))

	pkgs := loadModule(t, dir)
	set, _ := extractRegisteredSet(pkgs)
	if !set.Has("example.com/synthetic/store", "AdvStore") {
		t.Fatalf("extractor missed AdvStore")
	}
	e := set.All[0]
	if e.Classification != "Advisory" {
		t.Errorf("Classification: want Advisory, got %q", e.Classification)
	}
	if e.IsCanonical() {
		t.Errorf("IsCanonical(): want false for Advisory")
	}
}

// TestExtractor_OpaqueConstructor verifies that a constructor returning
// a non-literal expression produces an Opaque entry + warning rather
// than a silent miss. Per plan §D2 dynamic-registration clause.
func TestExtractor_OpaqueConstructor(t *testing.T) {
	dir := t.TempDir()
	writeSyntheticModule(t, dir, "example.com/synthetic", map[string]string{
		"store/store.go": `package store

import "github.com/Aethernet-network/aethernet/internal/projections"

type DynStore struct{}

func buildProjection() projections.CanonicalProjection {
	return projections.CanonicalProjection{
		Name:      "Dyn",
		StoreType: "DynStore",
	}
}

func DynStoreProjection(s *DynStore) projections.CanonicalProjection {
	// Dynamic/wrapped return — static extractor cannot fold this.
	p := buildProjection()
	return p
}
`,
	}, moduleRoot(t))

	pkgs := loadModule(t, dir)
	set, warnings := extractRegisteredSet(pkgs)
	// DynStoreProjection returns `p`, not a literal — extractor should emit a warning
	// and mark the entry Opaque (or skip it entirely in favor of buildProjection's
	// returned literal, depending on walk order). Either way we expect at least
	// one warning naming the dynamic constructor.
	foundOpaqueWarning := false
	for _, w := range warnings {
		if strings.Contains(w, "DynStoreProjection") && strings.Contains(w, "opaque") {
			foundOpaqueWarning = true
			break
		}
	}
	if !foundOpaqueWarning {
		t.Fatalf("expected opaque-constructor warning for DynStoreProjection; got warnings: %v", warnings)
	}
	_ = set // not asserting on set contents for opaque case — warning is the contract
}

// TestExtractor_MultipleConstructorsInOnePackage verifies that a package
// with multiple *Projection() functions (like internal/taskverification
// having both CalibrationProjection and RoundStoreProjection) extracts
// all of them.
func TestExtractor_MultipleConstructorsInOnePackage(t *testing.T) {
	dir := t.TempDir()
	writeSyntheticModule(t, dir, "example.com/synthetic", map[string]string{
		"multi/multi.go": `package multi

import "github.com/Aethernet-network/aethernet/internal/projections"

type A struct{}
type B struct{}

func AProjection(a *A) projections.CanonicalProjection {
	return projections.CanonicalProjection{
		Name: "A", Package: "example.com/synthetic/multi", StoreType: "A",
		Classification: projections.Canonical,
		SourceEvents: []projections.EventType{"E1"},
		LiveConsumerRef: "x.Live", ReplayConsumerRef: "x.Replay",
		ObservabilitySurface: projections.Surface{Kind: projections.SurfaceHealth},
		IntegrationTestRef: "x.TestA", Owner: "me", CreatedAt: "2026-04-15",
	}
}

func BProjection(b *B) projections.CanonicalProjection {
	return projections.CanonicalProjection{
		Name: "B", Package: "example.com/synthetic/multi", StoreType: "B",
		Classification: projections.Canonical,
		SourceEvents: []projections.EventType{"E2"},
		LiveConsumerRef: "x.Live", ReplayConsumerRef: "x.Replay",
		ObservabilitySurface: projections.Surface{Kind: projections.SurfaceHealth},
		IntegrationTestRef: "x.TestB", Owner: "me", CreatedAt: "2026-04-15",
	}
}
`,
	}, moduleRoot(t))

	pkgs := loadModule(t, dir)
	set, _ := extractRegisteredSet(pkgs)
	if !set.Has("example.com/synthetic/multi", "A") {
		t.Fatalf("missed A; set: %+v", set.All)
	}
	if !set.Has("example.com/synthetic/multi", "B") {
		t.Fatalf("missed B; set: %+v", set.All)
	}
}

// TestExtractor_RealRepoFindsStep2Projections runs the extractor against
// the host module and verifies every step-2 Projection() constructor is
// extracted correctly. This is a regression guard — if any step-2
// retrofit entry drifts off the literal-struct convention, this test
// fails fast.
func TestExtractor_RealRepoFindsStep2Projections(t *testing.T) {
	cfg := &packages.Config{
		Dir: moduleRoot(t),
		Mode: packages.NeedName |
			packages.NeedFiles |
			packages.NeedCompiledGoFiles |
			packages.NeedImports |
			packages.NeedTypes |
			packages.NeedTypesInfo |
			packages.NeedSyntax,
	}
	pkgs, err := packages.Load(cfg, "./internal/...")
	if err != nil {
		t.Fatalf("packages.Load: %v", err)
	}
	set, warnings := extractRegisteredSet(pkgs)

	// Step-2 canonical entries.
	want := []struct {
		pkg       string
		storeType string
		wantClass string
	}{
		{"github.com/Aethernet-network/aethernet/internal/epoch", "RoundCounter", "Canonical"},
		{"github.com/Aethernet-network/aethernet/internal/taskverification", "CalibrationStore", "Canonical"},
		{"github.com/Aethernet-network/aethernet/internal/taskverification", "BadgerStore", "Canonical"},
		{"github.com/Aethernet-network/aethernet/internal/escrow", "Escrow", "Canonical"},
		{"github.com/Aethernet-network/aethernet/internal/ledger", "TransferLedger", "Canonical"},
		{"github.com/Aethernet-network/aethernet/internal/ocs", "Engine.pending", "Canonical"},
		{"github.com/Aethernet-network/aethernet/internal/reputation", "ReputationManager", "Advisory"},
		{"github.com/Aethernet-network/aethernet/internal/blobsync", "BlobServingReputation", "Advisory"},
		{"github.com/Aethernet-network/aethernet/internal/roundprogress", "BadgerSnapshotStore", "Advisory"},
	}
	for _, w := range want {
		e, ok := set.ByStoreType[registeredKey{pkgPath: w.pkg, storeType: w.storeType}]
		if !ok {
			// Print all entries for diagnosis.
			var got []string
			for _, entry := range set.All {
				got = append(got, entry.PackagePath+"."+entry.StoreType)
			}
			t.Fatalf("missing registered entry %s.%s; got entries: %v", w.pkg, w.storeType, got)
		}
		if e.Classification != w.wantClass {
			t.Errorf("%s.%s classification: want %s, got %s", w.pkg, w.storeType, w.wantClass, e.Classification)
		}
	}
	// No opaque warnings expected from the real repo — every step-2
	// Projection() uses the canonical literal-struct pattern.
	for _, msg := range warnings {
		if strings.Contains(msg, "opaque") {
			t.Errorf("unexpected opaque warning from real repo: %s", msg)
		}
	}
}
