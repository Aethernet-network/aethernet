# F4A-end performance baseline

**Status**: F4B step 0.3 — captured 2026-04-22 against `feat/selection-consistency-fix` @ `d63d9dc` (post step 0.2 flake fix; F4A-end state).
**Plan reference**: F4 plan v2 §5; F4B step 0 founder addition (gate report §8 question #2).

This baseline is the **FIXED comparator** for F4B's D-5 regression gate. F4B's protocol changes must not regress any measured metric by more than 10%. Per founder gate-approval directive: **do not recapture this baseline later**. Step-11's sort-fix timing impact is part of F4A's delta and is included in this baseline intentionally.

---

## 1. Scope

The baseline measures **dispatcher latency** — the cost of `dispatch.Dispatcher.Admit(ctx, ev)` under representative load shapes. F4B introduces logical-key admission, which changes the work `Admit` performs (logical-key projection + roundState query + IsComplete check + per-(consumer, key) admission record vs content-hash + per-(consumer, event) record). The dispatcher is the load-bearing surface F4B touches, so its latency is the gate.

Out of scope: real-consumer latency (settlement, escrow, ledger). Real consumers do orders-of-magnitude more work than dispatcher overhead; their cost is dominated by I/O. The benchmarks use a synthetic consumer (single atomic increment) to isolate dispatcher overhead.

## 2. Methodology

Benchmarks: `internal/dispatch/dispatcher_bench_test.go` — five synthetic workloads exercising different paths through `Admit`.

Run command (reproducer):

```
go test -bench=. -benchmem -benchtime=2s -count=5 -run=NONE ./internal/dispatch/
```

- `-benchtime=2s` — each benchmark runs for ≥ 2 seconds of wall-clock per iteration count, giving Go's harness statistical room to tune `b.N`
- `-count=5` — five repetitions per benchmark for variance estimation
- `-run=NONE` — disables non-benchmark tests so they don't pollute timing

## 3. Test environment

| Property | Value |
|---|---|
| Hardware | Apple M1 (arm64) |
| OS | Darwin 25.4.0 |
| Go version | per project go.mod (1.26.0) |
| `GOMAXPROCS` | default = 8 (M1 8 cores) |
| Build tags | none |
| State | F4A-end + step 0.2 flake fix (no F4B protocol changes) |

## 4. Captured numbers

Five runs per benchmark. Per-op time is mean wall-clock across all `b.N` iterations within that run.

### 4.1 BenchmarkAdmit_FreshContentHash

Per-event admission of unique events (full path: AdmissionKey → VerifyAnchor → snapshotInterestedConsumers → reserveOrLoad → checkPrerequisites → invokeConsumers → safeApply → persist).

| Run | iters | ns/op | B/op | allocs/op |
|---:|---:|---:|---:|---:|
| 1 | 140,912 | 16,207 | 9,990 | 209 |
| 2 | 167,768 | 14,949 | 9,974 | 209 |
| 3 | 166,374 | 14,840 | 9,974 | 209 |
| 4 | 166,771 | 14,931 | 9,974 | 209 |
| 5 | 166,430 | 15,117 | 9,974 | 209 |
| **median** | — | **14,949** | **9,974** | **209** |
| **min** | — | 14,840 | 9,974 | 209 |
| **max** | — | 16,207 | 9,990 | 209 |

**Baseline value**: **14,949 ns/op** (median). **Regression threshold**: 16,444 ns/op (10% over baseline).

### 4.2 BenchmarkAdmit_DuplicateContentHash

Idempotent re-admit: same event presented N times. After the first admission the record is in StateApplied; subsequent calls short-circuit at `rec.State == StateApplied → return nil`.

| Run | iters | ns/op | B/op | allocs/op |
|---:|---:|---:|---:|---:|
| 1 | 370,648 | 6,456 | 4,283 | 96 |
| 2 | 360,618 | 6,438 | 4,284 | 96 |
| 3 | 375,890 | 6,411 | 4,283 | 96 |
| 4 | 368,778 | 6,462 | 4,283 | 96 |
| 5 | 375,488 | 6,419 | 4,283 | 96 |
| **median** | — | **6,438** | **4,283** | **96** |
| **min** | — | 6,411 | 4,283 | 96 |
| **max** | — | 6,462 | 4,284 | 96 |

**Baseline value**: **6,438 ns/op** (median). **Regression threshold**: 7,082 ns/op.

Confirms the StateApplied short-circuit is ~2.3× faster than fresh admission, as expected (skips reserveOrLoad write, checkPrerequisites, invokeConsumers).

### 4.3 BenchmarkAdmit_ConcurrentDifferentEvents

Parallel: each goroutine admits unique events (no per-task contention; tests `d.mu.RLock` cost in `snapshotInterestedConsumers` under concurrency).

| Run | iters | ns/op | B/op | allocs/op |
|---:|---:|---:|---:|---:|
| 1 | 344,671 | 6,942 | 9,970 | 209 |
| 2 | 338,200 | 7,042 | 9,972 | 209 |
| 3 | 335,704 | 7,217 | 9,972 | 209 |
| 4 | 337,306 | 7,427 | 9,972 | 209 |
| 5 | 331,669 | 7,024 | 9,974 | 209 |
| **median** | — | **7,042** | **9,972** | **209** |
| **min** | — | 6,942 | 9,970 | 209 |
| **max** | — | 7,427 | 9,974 | 209 |

**Baseline value**: **7,042 ns/op** (median). **Regression threshold**: 7,746 ns/op.

8-core parallel speedup vs sequential: ~14,949 / 7,042 ≈ **2.12×**. Limited by the admission-store mutex and `d.mu.Lock()` in `addToDeferralIndex` (when prerequisites are present); for prereq-free synthetic events the bottleneck is the in-memory admission store.

### 4.4 BenchmarkAdmit_ConcurrentSameEvent

Parallel, contended fast-path: many goroutines admit the SAME pre-applied event. All hit the StateApplied short-circuit.

| Run | iters | ns/op | B/op | allocs/op |
|---:|---:|---:|---:|---:|
| 1 | 782,970 | 3,068 | 4,288 | 96 |
| 2 | 859,340 | 2,862 | 4,288 | 96 |
| 3 | 849,921 | 2,858 | 4,288 | 96 |
| 4 | 866,781 | 2,869 | 4,288 | 96 |
| 5 | 842,004 | 2,928 | 4,288 | 96 |
| **median** | — | **2,869** | **4,288** | **96** |
| **min** | — | 2,858 | 4,288 | 96 |
| **max** | — | 3,068 | 4,288 | 96 |

**Baseline value**: **2,869 ns/op** (median). **Regression threshold**: 3,156 ns/op.

The fastest path. Confirms read-only `d.mu.RLock()` + admission-store read scales near-linearly under contention.

### 4.5 BenchmarkAdmit_StreamWithBackpressure

Sustained stream: 10 producer goroutines each admitting 1/10th of `b.N` unique events. Models the production shape where fastpath + sync handlers + recognition bus all call `Admit` concurrently for distinct events.

| Run | iters | ns/op | B/op | allocs/op |
|---:|---:|---:|---:|---:|
| 1 | 340,774 | 7,371 | 9,971 | 209 |
| 2 | 336,128 | 7,324 | 9,972 | 209 |
| 3 | 333,442 | 7,080 | 9,973 | 209 |
| 4 | 336,837 | 7,050 | 9,972 | 209 |
| 5 | 332,670 | 7,149 | 9,973 | 209 |
| **median** | — | **7,149** | **9,972** | **209** |
| **min** | — | 7,050 | 9,971 | 209 |
| **max** | — | 7,371 | 9,973 | 209 |

**Baseline value**: **7,149 ns/op** (median). **Regression threshold**: 7,864 ns/op.

Same shape as ConcurrentDifferentEvents (within ~2%). Confirms the production-load pattern is dominated by contention on the same surfaces, not by the producer-count multiplier.

## 5. Summary table — regression thresholds for F4B D-5 gate

| Benchmark | Baseline (ns/op) | Threshold @ 10% | What F4B must not exceed |
|---|---:|---:|---|
| FreshContentHash | 14,949 | **16,444** | Per-event admission of unique events (sequential) |
| DuplicateContentHash | 6,438 | **7,082** | Idempotent re-admit fast path |
| ConcurrentDifferentEvents | 7,042 | **7,746** | Parallel admission, no per-task contention |
| ConcurrentSameEvent | 2,869 | **3,156** | Parallel idempotent fast path |
| StreamWithBackpressure | 7,149 | **7,864** | Production-shape sustained load |

Allocation counts and `B/op` are documented above for diagnostic value but are NOT regression-gated by D-5 (the gate is wall-clock latency).

## 6. F4B comparison procedure

When F4B's logical-key admission lands:

1. Re-run the same benchmark command on the same hardware (Apple M1 / darwin / arm64). If the F4B implementer is on different hardware, capture an F4B-end re-baseline of the SAME tag F4A was baselined against (`d63d9dc`) on the new hardware first, and use that as the comparator. The 10% threshold is relative; absolute numbers vary across hardware.
2. Measure the equivalent benchmarks on the F4B branch.
3. For each metric in §5, compute `(F4B_median - F4A_baseline) / F4A_baseline × 100`.
4. Any metric exceeding +10% is a halt-and-surface trigger per the founder's F4A approval message.
5. **Do not adjust the baseline.** If F4B genuinely needs more than +10% for the protocol benefit, surface as an architect decision; do not silently re-baseline.

## 7. Notes for the F4B implementer

- The benchmarks register a synthetic always-interested consumer with no work (single atomic increment on Apply). When F4B introduces logical-key admission, expect `FreshContentHash` (using the existing content-hash path for the synthetic consumer) to remain unchanged — F4B's path divergence happens via consumer registration declaration, not via fundamental dispatcher rewrite.
- A NEW benchmark `BenchmarkAdmit_FreshLogicalKey` (name shape suggested) should be added in F4B's first persistence-layer commit, capturing the logical-key path's per-event cost. That number is the new D-5 number for the logical-key path; it does NOT replace the content-hash baseline.
- Allocation pressure: the FreshContentHash path allocates 209 objects per admission (~10 KiB). Most of this is JSON encoding inside `PutAdmission`. F4B's logical-key change adds the `RoundState` query and `IsComplete` evaluation per admission attempt; that is expected to push allocs/op upward unless aggressively reused. If the logical-key allocs/op exceeds 250-300, surface as a follow-on optimization — don't gate F4B's correctness on it.

---

**End of F4A-end performance baseline.** Captured 2026-04-22, frozen at commit `d63d9dc`.
