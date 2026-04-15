package lint

import (
	"strings"
	"testing"
)

// TestMatcher_FlagsUnregisteredStoreWithBadgerField verifies the basic
// D3 happy-path: a type with *badger.DB and a writer method, not
// registered, is flagged.
func TestMatcher_FlagsUnregisteredStoreWithBadgerField(t *testing.T) {
	dir := t.TempDir()
	writeSyntheticModule(t, dir, "example.com/m", map[string]string{
		"pkg/unregistered.go": `package pkg

import (
	"context"

	badger "github.com/dgraph-io/badger/v4"
)

type Unregistered struct {
	db *badger.DB
}

func (u *Unregistered) Put(ctx context.Context, k, v []byte) error {
	return u.db.Update(func(txn *badger.Txn) error {
		return txn.Set(k, v)
	})
}
`,
	}, moduleRoot(t))

	pkgs := loadModule(t, dir)
	set := NewRegisteredSet() // nothing registered
	suspects, insuff, _ := findSuspectTypes(pkgs, set)
	if len(insuff) != 0 {
		t.Fatalf("no insufficient-pragma expected, got: %+v", insuff)
	}
	if len(suspects) != 1 {
		t.Fatalf("want 1 suspect, got %d: %+v", len(suspects), suspects)
	}
	s := suspects[0]
	if s.TypeName != "Unregistered" {
		t.Errorf("TypeName: want Unregistered, got %q", s.TypeName)
	}
	if s.PackagePath != "example.com/m/pkg" {
		t.Errorf("PackagePath: got %q", s.PackagePath)
	}
	if !strings.Contains(s.Evidence, "*badger.DB field") {
		t.Errorf("Evidence should mention *badger.DB field: %q", s.Evidence)
	}
	if !strings.Contains(s.Evidence, "Put") {
		t.Errorf("Evidence should mention writer method Put: %q", s.Evidence)
	}
}

// TestMatcher_SkipsRegisteredStore verifies that a type already in the
// RegisteredSet is not flagged.
func TestMatcher_SkipsRegisteredStore(t *testing.T) {
	dir := t.TempDir()
	writeSyntheticModule(t, dir, "example.com/m", map[string]string{
		"pkg/reg.go": `package pkg

import badger "github.com/dgraph-io/badger/v4"

type RegStore struct {
	db *badger.DB
}

func (r *RegStore) Write() error { return nil }
`,
	}, moduleRoot(t))

	pkgs := loadModule(t, dir)
	set := NewRegisteredSet()
	// Pre-register the type.
	set.ByStoreType[registeredKey{pkgPath: "example.com/m/pkg", storeType: "RegStore"}] = &RegisteredEntry{
		PackagePath: "example.com/m/pkg", StoreType: "RegStore",
	}
	suspects, _, _ := findSuspectTypes(pkgs, set)
	if len(suspects) != 0 {
		t.Fatalf("registered type must not be flagged, got: %+v", suspects)
	}
}

// TestMatcher_ValidPragmaSuppresses verifies a valid D4 pragma
// (≥20 chars, ≥3 words) suppresses the type from suspect output.
func TestMatcher_ValidPragmaSuppresses(t *testing.T) {
	dir := t.TempDir()
	writeSyntheticModule(t, dir, "example.com/m", map[string]string{
		"pkg/suppressed.go": `package pkg

import badger "github.com/dgraph-io/badger/v4"

// projections:lint ignore "legitimate helper cache for rate limiting; never feeds consensus paths"
type RateCache struct {
	db *badger.DB
}

func (r *RateCache) Write() error { return nil }
`,
	}, moduleRoot(t))

	pkgs := loadModule(t, dir)
	set := NewRegisteredSet()
	suspects, insuff, _ := findSuspectTypes(pkgs, set)
	if len(suspects) != 0 {
		t.Fatalf("pragma-suppressed type must not be flagged: %+v", suspects)
	}
	if len(insuff) != 0 {
		t.Fatalf("valid pragma must not appear in insufficient list: %+v", insuff)
	}
}

// TestMatcher_InsufficientReasonRejected verifies a pragma with too-short
// reason (< 20 chars OR < 3 words) is reported as InsufficientSuppression.
func TestMatcher_InsufficientReasonRejected(t *testing.T) {
	cases := []struct {
		name   string
		reason string
	}{
		{"too-short", `false positive`},     // 14 chars, 2 words
		{"enough-chars-too-few-words", `thisisalongstring`},    // 17 chars, 1 word
		{"three-words-but-short", `a b c`}, // 5 chars, 3 words
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			src := `package pkg

import badger "github.com/dgraph-io/badger/v4"

// projections:lint ignore "` + tc.reason + `"
type T struct {
	db *badger.DB
}

func (t *T) Write() error { return nil }
`
			writeSyntheticModule(t, dir, "example.com/m", map[string]string{
				"pkg/x.go": src,
			}, moduleRoot(t))

			pkgs := loadModule(t, dir)
			set := NewRegisteredSet()
			suspects, insuff, _ := findSuspectTypes(pkgs, set)
			if len(suspects) != 0 {
				t.Fatalf("insufficient pragma must not escape detection via suppression: %+v", suspects)
			}
			if len(insuff) != 1 {
				t.Fatalf("want 1 insufficient record, got %d: %+v", len(insuff), insuff)
			}
			if insuff[0].Reason != tc.reason {
				t.Errorf("reason: want %q, got %q", tc.reason, insuff[0].Reason)
			}
		})
	}
}

// TestMatcher_EmbeddingCatchesWrapperEvasion verifies the D3 embedding
// extension: a type embeds another type that holds *badger.DB; the
// outer type is still flagged.
func TestMatcher_EmbeddingCatchesWrapperEvasion(t *testing.T) {
	dir := t.TempDir()
	writeSyntheticModule(t, dir, "example.com/m", map[string]string{
		"pkg/outer.go": `package pkg

import badger "github.com/dgraph-io/badger/v4"

type inner struct {
	db *badger.DB
}

func (i *inner) writeInner() error { return nil }

// Outer embeds inner — it does NOT have a direct *badger.DB field
// but should still be flagged via the embedding path.
type Outer struct {
	*inner
}

func (o *Outer) WriteOuter() error { return nil }
`,
	}, moduleRoot(t))

	pkgs := loadModule(t, dir)
	set := NewRegisteredSet()
	suspects, _, _ := findSuspectTypes(pkgs, set)
	// Both `inner` and `Outer` should be flagged (both have writer
	// methods + transitive persistence).
	var foundOuter, foundInner bool
	for _, s := range suspects {
		switch s.TypeName {
		case "Outer":
			foundOuter = true
			if !strings.Contains(s.Evidence, "embedded") && !strings.Contains(s.Evidence, "via") {
				t.Errorf("Outer evidence should mention the embedding path: %q", s.Evidence)
			}
		case "inner":
			foundInner = true
		}
	}
	if !foundOuter {
		t.Fatalf("matcher missed Outer (embedding evasion): suspects=%+v", suspects)
	}
	if !foundInner {
		t.Fatalf("matcher missed inner (has direct *badger.DB)")
	}
}

// TestMatcher_InterfaceFieldWarnsNotFails verifies D3 cost-gate deferral:
// an interface-typed persistence field produces a warning, not a
// suspect entry. Step 3.5 will upgrade this.
func TestMatcher_InterfaceFieldWarnsNotFails(t *testing.T) {
	dir := t.TempDir()
	writeSyntheticModule(t, dir, "example.com/m", map[string]string{
		"pkg/iface.go": `package pkg

type PersistI interface {
	Save(key, value []byte) error
}

type IfaceStore struct {
	store PersistI
}

func (i *IfaceStore) Write() error { return nil }
`,
	}, moduleRoot(t))

	pkgs := loadModule(t, dir)
	set := NewRegisteredSet()
	suspects, _, warnings := findSuspectTypes(pkgs, set)
	if len(suspects) != 0 {
		t.Fatalf("interface-typed persistence must not fail the lint: %+v", suspects)
	}
	foundIfaceWarn := false
	for _, w := range warnings {
		if strings.Contains(w, "interface-indirect persistence") && strings.Contains(w, "IfaceStore") {
			foundIfaceWarn = true
			break
		}
	}
	if !foundIfaceWarn {
		t.Fatalf("expected interface-indirect persistence warning for IfaceStore; got: %v", warnings)
	}
}

// TestMatcher_SkipsTestFiles verifies that types declared in _test.go
// files are not flagged — tests can freely use BadgerDB for fixtures
// without having to register them.
func TestMatcher_SkipsTestFiles(t *testing.T) {
	dir := t.TempDir()
	writeSyntheticModule(t, dir, "example.com/m", map[string]string{
		"pkg/keep.go": `package pkg

// placeholder so the package has at least one non-test file.
var _ = 0
`,
		"pkg/fixture_test.go": `package pkg

import (
	"testing"

	badger "github.com/dgraph-io/badger/v4"
)

type TestFixture struct {
	db *badger.DB
}

func (f *TestFixture) Store() error { return nil }

func TestSomething(t *testing.T) {}
`,
	}, moduleRoot(t))

	pkgs := loadModule(t, dir)
	set := NewRegisteredSet()
	suspects, _, _ := findSuspectTypes(pkgs, set)
	if len(suspects) != 0 {
		t.Fatalf("types in _test.go must not be flagged: %+v", suspects)
	}
}

// TestCountWords is a quick unit test for the word counter used by D4.
func TestCountWords(t *testing.T) {
	cases := []struct {
		in   string
		want int
	}{
		{"", 0},
		{"one", 1},
		{"two words", 2},
		{"  two   words   ", 2},
		{"three short words", 3},
		{"\tone\ttab\tseparated", 3},
	}
	for _, c := range cases {
		if got := countWords(c.in); got != c.want {
			t.Errorf("countWords(%q): want %d, got %d", c.in, c.want, got)
		}
	}
}
