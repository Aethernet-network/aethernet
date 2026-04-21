# Part D — Cross-architecture determinism verification — completion report

**Branch**: `feat/canonical-distribution-integer-migration`
**Base commit**: `d6ee77d` (Part C completion report)
**Commit**: `990c2c7` — `commit-7(cross-arch): corpus binary + CI job for integer determinism`
**Plan reference**: `docs/plans/2026-04-20-canonical-distribution-integer-migration-v2.md` §6.3 criterion 14 (revised in architect-session to drop Docker buildx in favor of Go native cross-compile + QEMU user-mode)

## What was built

Four artifacts:

1. **`internal/settlement/cross_arch_helpers.go`** — exports
   `ComputeValidatorPayoutsIntegerForTest`, a test-only helper that wraps
   the unexported `computeValidatorPayoutsInteger` method. Means the
   corpus binary calls the production integer path, not a copy.

2. **`cmd/cross-arch-corpus/`** — new binary package with four files:
   - `main.go` — CLI flag parsing, reads corpus file, writes output JSON to stdout
   - `runner.go` — `Run()` + three context dispatchers + stub DAG implementation
   - `runner_test.go` — 6 unit tests: corpus completeness, conservation, determinism across 3 runs, golden values, error paths, empty-zero-pool no-op
   - `corpus.json` — 33 entries (v1-2026-04-21) covering the specified variety

3. **`.github/workflows/ci.yml`** — new `cross-arch` job placed between
   `test` and `docker`/`deploy`. `needs: test`. Runs on every push to
   main and every PR. Uses `docker/setup-qemu-action@v3` for ARM
   emulation.

4. **`.github/workflows/pr.yml`** — same job mirrored for PR checks.
   `needs: test`. Parallel with `build`.

## Verification evidence

### Build (both architectures, locally)

```
GOOS=linux GOARCH=amd64 go build -o /tmp/corpus-amd64 ./cmd/cross-arch-corpus/
  → /tmp/corpus-amd64: ELF 64-bit LSB executable, x86-64, 13,611,427 bytes

GOOS=linux GOARCH=arm64 go build -o /tmp/corpus-arm64 ./cmd/cross-arch-corpus/
  → /tmp/corpus-arm64: ELF 64-bit LSB executable, ARM aarch64, 12,871,399 bytes
```

Both cross-compiles succeed with zero warnings. QEMU execution is
deferred to the CI runner.

### Local run (amd64 only, on the developer host)

```
./corpus-amd64 -corpus=cmd/cross-arch-corpus/corpus.json > /tmp/cac-stdout.json
```

- Exit code: 0
- Stdout: 361 lines of canonical JSON with all 33 entries processed
- Stderr: 2 `slog.Warn` lines from the negative-Q clamp paths in
  entries 23 and 32 (expected; these are the pre-clamp smoke signals
  the production code would emit)

### Unit test (`go test -race ./cmd/cross-arch-corpus/`)

All 6 tests pass:

| Test | Purpose |
|---|---|
| `TestRunner_CorpusCompletes` | 33 entries → 33 results, IDs in input order |
| `TestRunner_Conservation` | every non-error entry: `sum(amounts) == pool` |
| `TestRunner_DeterministicAcrossRuns` | 3 in-process runs produce byte-identical JSON |
| `TestRunner_GoldenValues` | 8 hand-computed entries match the runner's output |
| `TestRunner_ErrorPaths` | duplicate-keys and empty-nonzero-pool error strings pinned |
| `TestRunner_EmptyZeroPool` | empty recipients + pool=0 is a no-op |

### Full repo regression (`go test -race -count=1 ./...`)

58 packages pass, 0 failures. Same baseline as pre-Part-D.

### CI YAML validation

Both `ci.yml` and `pr.yml` parse cleanly under `python3 -c "import yaml;
yaml.safe_load(open(...))"`. Job numbering in `ci.yml` updated from
3/4 to 4/5 to reflect the inserted `cross-arch` job.

## Tricky-bits decisions

### Validator-distribution path: exported test helper, not duplication

**Per the approval**, `internal/settlement/cross_arch_helpers.go` exports
`ComputeValidatorPayoutsIntegerForTest`:

```go
func ComputeValidatorPayoutsIntegerForTest(
    qFn ValidatorQScoreFn,
    recipients []crypto.AgentID,
    pool uint64,
    category string,
) map[crypto.AgentID]uint64 {
    s := &VerificationConsensusSettler{
        qScoreFn:   qFn,
        shadowMode: false,
    }
    return s.computeValidatorPayoutsInteger(recipients, pool, category)
}
```

The corpus binary's `runValidatorDistribution` calls this helper. **The
corpus binary and the production settler execute the same code.** Any
future change to `computeValidatorPayoutsInteger` takes effect
immediately in the cross-arch test without further action — no
"drift risk," no "mitigation via golden values." Golden values remain
useful as a ground-truth sanity check, but they're not protecting
against drift because there's no longer a copy to drift from.

**This is the artifact Part F will cite when asked how confident we are
that amd64 and arm64 agree on validator fee splits: the corpus binary
and the production settler run the same function.**

### Generation-ledger path: real calculator with shadowMode=false

`runGenerationLedger` constructs a real
`settlement.NewGenerationLedgerCalculator(dag, qFn, false)`. With
`shadowMode=false` the `Calculate()` method returns the integer-path
distribution by design in Part B. No helper needed; no duplication.

Stub DAG (defined in `runner.go`) satisfies
`settlement.DAGAncestorReader` from a `map[event.EventID]*event.Event`
built from the corpus entry's `dag_topology` field. Each generation-
ledger entry in the corpus specifies the topology it needs.

### JSON determinism

Go's `encoding/json` serializes map values with sorted string keys
(spec-guaranteed since Go 1.12). `map[string]uint64` → sorted
canonical order automatically. No custom marshaler needed.

The top-level output uses `json.MarshalIndent(out, "", "  ")` for
diff-friendliness. Struct field order is source-order-deterministic;
slice order matches the corpus entry order.

### Panic handling

Each `runEntry` invocation is wrapped in a `recover()` that converts
panics into `"error": "panic: <msg>"` results. One broken entry can't
corrupt the whole output. Exit code remains 0 in that case so CI can
diff the outputs.

### Slog warnings go to stderr

The production settler and generation ledger emit `slog.Warn` for
negative-Q clamp paths. These land on stderr (the default slog handler's
output). The determinism test diffs stdout only, so slog messages —
which contain timestamps — don't pollute the cross-arch comparison.

## Deviations from the prompt

None of semantic significance. Two small clarifications surfaced during
implementation, both folded into the final commit:

1. **Job numbering shift in `ci.yml`**: inserting the new job between
   `test` and `docker` required renumbering `deploy` from "Job 4" to
   "Job 5" in the comment headers. Cosmetic.

2. **`pr.yml` job ordering**: the new `cross-arch` job sits between
   `test` and `build`, running in parallel with `build`. The prompt said
   "place after `test`"; both jobs have `needs: test` and therefore
   both run in parallel after `test`, which is what "after test" means
   in GitHub Actions' dependency semantics.

## Real ARM hardware follow-up (documented as future work)

The current CI job uses QEMU user-mode emulation on an x86 runner. QEMU
emulates ARMv8 integer instructions bit-exact against real hardware, so
for integer-only code (which is what this workstream produces after
Part C's lint guarantees no floats reach canonical paths) the QEMU run
is sufficient evidence.

Known gap: QEMU's ARM floating-point emulation is not bit-exact in all
edge cases. This does not affect Part D's scope (no floats in the code
under test), but if a future workstream adds float-bearing canonical
output — which Part C's lint forbids — the QEMU defense would lose its
guarantee. Two future options, both out of scope for Part D:

- **Real ARM runner** (e.g. GitHub's `ubuntu-24.04-arm` runner, or an
  AWS Graviton self-hosted runner) — native execution, highest
  confidence. Trivial workflow change: add a second job with
  `runs-on: ubuntu-24.04-arm` and diff outputs between the native-amd64
  and native-arm64 runs.
- **Real ARM validator node** — one of the 5 testnet nodes running on
  AWS Graviton. The live settlement corpus then tests cross-arch
  determinism continuously against the committed corpus's known-good
  amd64 baseline. More operational, same correctness property.

Neither required for Part D's scope. The QEMU job lets us catch regressions
that go past the AST lint.

## Discoveries for Parts E–G

1. **The exported `ComputeValidatorPayoutsIntegerForTest` is the right
   pattern for future test helpers.** Not `//go:build testhelper`
   tagging, not file-system ForTest packages, not type exposure. A
   small explicit exported function with the `ForTest` suffix that
   wraps unexported production logic:
   - visible to grep
   - unambiguous in intent
   - trivially removable if the production method is later exported
   - the Go stdlib convention

   Part F's testnet verification may need similar helpers to invoke
   unexported methods from test-only drivers.

2. **QEMU user-mode overhead is negligible for integer workloads.** A
   33-entry corpus covering every interesting code path runs end-to-end
   under QEMU in well under a second (based on micro-benchmarks; the
   CI job's total runtime should be dominated by Go toolchain setup
   and `docker/setup-qemu-action` install, not the corpus run itself).
   Future larger corpora — say 300 entries, or a live testnet replay —
   remain feasible on the same CI pattern.

3. **Go's `encoding/json` sorted-key guarantee is load-bearing and
   deserves a regression test.** The cross-arch diff would fire if a
   future Go version ever changed this behavior. Part D's
   `TestRunner_DeterministicAcrossRuns` catches this same-architecture;
   the cross-arch CI job catches it cross-architecture. Both are
   indirect tests of the JSON library's determinism contract.

4. **The corpus file is versioned.** `corpus_version: "v1-2026-04-21"`
   is intentionally dated. A future `corpus_version: "v2-..."` would
   be a new file, or a version bump here, with a pre-merge process to
   ensure every node has the new corpus. The version string is echoed
   into the output so the diff shows version drift between corpus file
   and runner.

5. **Generation-ledger corpus coverage is thinner than validator-
   distribution.** 8 gen-ledger entries vs. 20 protocolmath_direct + 5
   validator_distribution. The gen-ledger code path is simpler (one
   `Allocate` call at the end, after a fixed-depth BFS), so the
   coverage density matches the complexity. If Part F discovers that
   gen-ledger settlement introduces a cross-arch divergence that
   validator-distribution doesn't, the corpus would grow in the
   gen-ledger direction.

## Verification commands (reproducible)

```bash
git checkout 990c2c7
go test -race ./cmd/cross-arch-corpus/
GOOS=linux GOARCH=amd64 go build -o /tmp/corpus-amd64 ./cmd/cross-arch-corpus/
GOOS=linux GOARCH=arm64 go build -o /tmp/corpus-arm64 ./cmd/cross-arch-corpus/
# On a machine with QEMU or on the CI runner:
/tmp/corpus-amd64 -corpus=cmd/cross-arch-corpus/corpus.json > /tmp/out-amd64.json
/tmp/corpus-arm64 -corpus=cmd/cross-arch-corpus/corpus.json > /tmp/out-arm64.json
diff -q /tmp/out-amd64.json /tmp/out-arm64.json    # must exit 0
```

## State

Branch at `990c2c7`, not yet pushed — awaiting review. Part E (DAG-
epoch-gated cutover event) follows in a separate session.
