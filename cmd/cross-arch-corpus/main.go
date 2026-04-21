// Command cross-arch-corpus reads a canonical input corpus and writes
// deterministic outputs by running each corpus entry through the integer
// code paths introduced by the canonical-distribution-integer-migration
// workstream. The output is canonical JSON (sorted map keys, integer-only
// values) and must be byte-identical between amd64 and arm64 builds of
// this binary — that invariant is what the cross-architecture CI job at
// .github/workflows/ci.yml verifies under QEMU user-mode emulation.
//
// Usage:
//
//	cross-arch-corpus -corpus=path/to/corpus.json
//
// Exit codes:
//
//	0: all entries processed (per-entry errors are serialized in output)
//	1: I/O failure (can't read corpus file or write output)
//	2: malformed corpus JSON
//
// The binary is pure-deterministic: no time, no randomness, no environment
// variables, no network. Same input, same output, every run, on any CPU
// architecture Go cross-compiles to.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
)

func main() {
	corpusPath := flag.String("corpus", "", "path to corpus.json")
	flag.Parse()

	if *corpusPath == "" {
		fmt.Fprintln(os.Stderr, "usage: cross-arch-corpus -corpus=path/to/corpus.json")
		os.Exit(1)
	}

	data, err := os.ReadFile(*corpusPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "read corpus:", err)
		os.Exit(1)
	}

	var corpus Corpus
	if err := json.Unmarshal(data, &corpus); err != nil {
		fmt.Fprintln(os.Stderr, "parse corpus:", err)
		os.Exit(2)
	}

	out := Run(&corpus)

	// MarshalIndent for diff-friendly output. Go's encoding/json sorts
	// map keys when marshaling map values, so byte-identical output is
	// guaranteed given byte-identical input and integer-only arithmetic.
	encoded, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		fmt.Fprintln(os.Stderr, "marshal output:", err)
		os.Exit(1)
	}

	if _, err := os.Stdout.Write(encoded); err != nil {
		fmt.Fprintln(os.Stderr, "write output:", err)
		os.Exit(1)
	}
	if _, err := os.Stdout.Write([]byte("\n")); err != nil {
		fmt.Fprintln(os.Stderr, "write output:", err)
		os.Exit(1)
	}
}
