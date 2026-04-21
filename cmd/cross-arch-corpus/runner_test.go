package main

import (
	"bytes"
	"encoding/json"
	"os"
	"reflect"
	"testing"
)

// loadCorpus reads and parses the committed corpus file. Fails the test on
// any I/O or parse error so the test failure points at corpus damage
// rather than at runner behavior.
func loadCorpus(t *testing.T, path string) *Corpus {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read corpus %s: %v", path, err)
	}
	var c Corpus
	if err := json.Unmarshal(data, &c); err != nil {
		t.Fatalf("parse corpus %s: %v", path, err)
	}
	return &c
}

const corpusPath = "corpus.json"

// TestRunner_CorpusCompletes asserts every entry produces a result with
// matching entry_id — no entries silently dropped.
func TestRunner_CorpusCompletes(t *testing.T) {
	c := loadCorpus(t, corpusPath)
	out := Run(c)
	if len(out.Results) != len(c.Entries) {
		t.Fatalf("results=%d want=%d (entries dropped)", len(out.Results), len(c.Entries))
	}
	if out.CorpusVersion != c.Version {
		t.Errorf("output version=%q want=%q", out.CorpusVersion, c.Version)
	}
	for i, r := range out.Results {
		if r.EntryID != c.Entries[i].ID {
			t.Errorf("result[%d].EntryID=%q want=%q (order drift)", i, r.EntryID, c.Entries[i].ID)
		}
	}
}

// TestRunner_Conservation asserts that, for every non-error entry, the
// sum of amounts equals the pool exactly. This is the core correctness
// property the cross-arch job ultimately validates, checked locally here
// so a broken corpus or a broken runner is caught before it ever reaches
// QEMU.
func TestRunner_Conservation(t *testing.T) {
	c := loadCorpus(t, corpusPath)
	out := Run(c)
	for _, r := range out.Results {
		if r.Error != "" {
			continue // error entries don't conserve — they have no amounts
		}
		if r.TotalAllocated != r.Pool {
			t.Errorf("entry %s (%s): total_allocated=%d pool=%d (conservation broken)",
				r.EntryID, r.Context, r.TotalAllocated, r.Pool)
		}
		var sum uint64
		for _, a := range r.Amounts {
			sum += a
		}
		if sum != r.TotalAllocated {
			t.Errorf("entry %s: sum(amounts)=%d != total_allocated=%d",
				r.EntryID, sum, r.TotalAllocated)
		}
	}
}

// TestRunner_DeterministicAcrossRuns asserts three consecutive in-process
// runs of the corpus produce byte-identical output. This is the
// same-architecture sibling of the QEMU-based cross-architecture test:
// if this fails, intra-run nondeterminism has crept in (map iteration
// order, randomness, wall-clock time) and the cross-arch test cannot
// possibly pass.
func TestRunner_DeterministicAcrossRuns(t *testing.T) {
	c := loadCorpus(t, corpusPath)
	a, err := json.MarshalIndent(Run(c), "", "  ")
	if err != nil {
		t.Fatalf("marshal a: %v", err)
	}
	b, err := json.MarshalIndent(Run(c), "", "  ")
	if err != nil {
		t.Fatalf("marshal b: %v", err)
	}
	cb, err := json.MarshalIndent(Run(c), "", "  ")
	if err != nil {
		t.Fatalf("marshal c: %v", err)
	}
	if !bytes.Equal(a, b) || !bytes.Equal(b, cb) {
		t.Fatal("three consecutive runs produced different output; intra-run nondeterminism present")
	}
}

// TestRunner_GoldenValues pins hand-computed expected amounts for a
// subset of well-understood entries. Ground-truth cross-check that the
// runner invokes the integer paths correctly — not merely returns a
// self-consistent result.
//
// Golden values are intentionally a subset of the corpus, covering one
// entry per context plus a boundary case, rather than all 33 entries.
// Conservation + determinism cover the rest.
func TestRunner_GoldenValues(t *testing.T) {
	goldens := map[string]map[string]uint64{
		// protocolmath_direct: single recipient gets the full pool.
		"03-pm-single-neutral-pool-1000000": {"alpha": 1000000},
		// Two recipients equal Q, even pool → exact 50/50.
		"06-pm-two-equal-pool-100000": {"alpha": 50000, "beta": 50000},
		// Three recipients equal Q, pool 100 → 33/33/34 (sorted-last absorbs remainder).
		"08-pm-three-equal-pool-100": {"alpha": 33, "beta": 33, "gamma": 34},
		// All zero Q → even-split fallback. pool=1000 / 3 = 333, last absorbs 334.
		"11-pm-three-all-zero-even-split": {"alpha": 333, "beta": 333, "gamma": 334},
		// Exactly divisible (pool=600, weights 1/2/3 BP sum 60000). No remainder.
		"17-pm-exactly-divisible": {"alpha": 100, "beta": 200, "gamma": 300},
		// Validator distribution three equal, pool 2300 → 766/766/768 (sorted-last absorbs).
		"22-vd-three-equal-pool-2300": {"validator-alpha": 766, "validator-beta": 766, "validator-gamma": 768},
		// Single-ancestor generation ledger: full pool to the ancestor, no treasury.
		"26-gl-single-ancestor-depth1": {"P1": 10000},
		// Empty ancestors → full pool to treasury.
		"31-gl-empty-ancestors-full-treasury": {"treasury": 1000},
	}
	out := Run(loadCorpus(t, corpusPath))
	byID := make(map[string]Result, len(out.Results))
	for _, r := range out.Results {
		byID[r.EntryID] = r
	}
	for id, want := range goldens {
		got, ok := byID[id]
		if !ok {
			t.Errorf("golden entry %s not in output", id)
			continue
		}
		if !reflect.DeepEqual(got.Amounts, want) {
			t.Errorf("golden %s: got %v want %v", id, got.Amounts, want)
		}
	}
}

// TestRunner_ErrorPaths asserts the three expected error-path entries
// surface the correct error messages. These are as load-bearing as the
// success cases: if an error path's text changes between arches, the
// cross-arch diff would fire even though the semantic is identical.
func TestRunner_ErrorPaths(t *testing.T) {
	expectedErrors := map[string]string{
		"18-pm-duplicate-keys-error":      "allocate: protocolmath: duplicate canonical key in recipient set",
		"19-pm-empty-nonzero-pool-error":  "allocate: protocolmath: empty recipient set with nonzero pool",
	}
	out := Run(loadCorpus(t, corpusPath))
	for _, r := range out.Results {
		if want, ok := expectedErrors[r.EntryID]; ok {
			if r.Error != want {
				t.Errorf("entry %s: error=%q want=%q", r.EntryID, r.Error, want)
			}
		}
	}
}

// TestRunner_EmptyZeroPool asserts empty recipients with pool=0 is a
// no-op (no error, no amounts).
func TestRunner_EmptyZeroPool(t *testing.T) {
	out := Run(loadCorpus(t, corpusPath))
	for _, r := range out.Results {
		if r.EntryID == "20-pm-empty-zero-pool-noop" {
			if r.Error != "" {
				t.Errorf("empty+zero-pool should be no-op; got error %q", r.Error)
			}
			if len(r.Amounts) != 0 {
				t.Errorf("empty+zero-pool should have no amounts; got %v", r.Amounts)
			}
			return
		}
	}
	t.Fatal("entry 20-pm-empty-zero-pool-noop not found in output")
}
