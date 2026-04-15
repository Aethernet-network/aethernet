package lint

import (
	"testing"

	"golang.org/x/tools/go/packages"
)

// loadRealRepo loads the host module's ./internal/... packages WITH test
// files included, for tests that need to verify IntegrationTestRef
// symbols exist in integration test files.
func loadRealRepo(t *testing.T) ([]*packages.Package, error) {
	t.Helper()
	cfg := &packages.Config{
		Dir: moduleRoot(t),
		// Include test files — this is how Test* functions in
		// integration test files become visible to the extractor.
		Tests: true,
		Mode: packages.NeedName |
			packages.NeedFiles |
			packages.NeedCompiledGoFiles |
			packages.NeedImports |
			packages.NeedTypes |
			packages.NeedTypesInfo |
			packages.NeedSyntax,
	}
	return packages.Load(cfg, "./internal/...")
}
