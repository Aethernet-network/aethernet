package lint

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// writeFileImpl is a tiny helper for tests that need to write a single
// file without the full synthetic-module scaffolding.
func writeFileImpl(path, content string) error {
	return os.WriteFile(path, []byte(content), 0o644)
}

// moduleRoot returns the host module root so synthetic testdata can
// `require` the real packages.
func moduleRoot(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	// We are in internal/settlement/lint. Walk up to the module root.
	return filepath.Join(wd, "..", "..", "..")
}

// writeSyntheticModule writes a tiny Go module to dir with the given
// files, plus a docs/architecture/settlement-input-manifest.yaml file
// containing the provided manifestYAML body. The synthetic module
// imports the real aethernet module via a `replace` directive so
// in-scope packages can reference real types.
func writeSyntheticModule(
	t *testing.T,
	dir string,
	modulePath string,
	files map[string]string,
	manifestYAML string,
	hostModuleRoot string,
) {
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
	// Place the manifest where Check() expects it.
	manifestPath := filepath.Join(dir, "docs", "architecture", "settlement-input-manifest.yaml")
	if err := os.MkdirAll(filepath.Dir(manifestPath), 0o755); err != nil {
		t.Fatalf("mkdir docs/architecture: %v", err)
	}
	if err := os.WriteFile(manifestPath, []byte(manifestYAML), 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	// `go mod tidy` so packages.Load can type-check the require.
	if hostModuleRoot != "" {
		cmd := exec.Command("go", "mod", "tidy")
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GOFLAGS=-mod=mod")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("go mod tidy in %s: %v\n%s", dir, err, out)
		}
	}
}
