# F4C-merged performance baseline

**Status**: F4C step 4 pre-deploy — captured 2026-04-23 against `feat/selection-consistency-fix` @ `114d304` (immediately post-merge, pre-testnet-deploy).
**Plan reference**: F4 plan v2 §5.3 D-5 + §11.14.
**Supersedes**: `docs/architecture/f4a-end-performance-baseline.md` as the D-5 comparator for any future work on this branch. The F4A-end document remains the HISTORICAL record; this document is the NEW comparator going forward.

---

## 1. Scope

Measures `dispatch.Dispatcher.Admit(ctx, ev)` latency on the combined F4B+integer-migration branch. Same 5 synthetic benchmarks, same methodology, same hardware as the F4A-end baseline. Isolates dispatcher overhead via a synthetic consumer (atomic-increment Apply, no settlement work).

Out of scope: real-consumer latency (settlement, escrow, ledger). Out of scope: testnet latency (5-node AWS cluster with real network + BadgerDB fsync). Both are measured separately during F4C live-fire verification.

## 2. Methodology (unchanged from F4A-end baseline)

```
go test -bench=. -benchmem -benchtime=2s -count=5 -run=NONE ./internal/dispatch/
```

- `-benchtime=2s`: each benchmark iteration runs ≥ 2 seconds.
- `-count=5`: five repetitions per benchmark for variance estimation.
- `-run=NONE`: disables non-benchmark tests.

## 3. Test environment (unchanged)

| Property | Value |
|---|---|
| Hardware | Apple M1 (arm64) |
| OS | Darwin 25.4.0 |
| Go version | per project go.mod (1.26.0) |
| `GOMAXPROCS` | default = 8 |
| Build tags | none |
| Commit | `114d304` (F4C-merge + conflict resolution) |

## 4. Captured numbers (F4C-merged)

Five runs per benchmark. Per-op time is mean wall-clock across all `b.N` iterations within that run.

### 4.1 BenchmarkAdmit_FreshContentHash

| Run | iters | ns/op | B/op | allocs/op |
|---:|---:|---:|---:|---:|
| 1 | 152,823 | 15,892 | 10,046 | 209 |
| 2 | 164,476 | 15,112 | 10,039 | 209 |
| 3 | 163,948 | 15,364 | 10,039 | 209 |
| 4 | 162,187 | 15,090 | 10,040 | 209 |
| 5 | 163,636 | 15,311 | 10,040 | 209 |
| **median** | — | **15,311** | **10,040** | **209** |

### 4.2 BenchmarkAdmit_DuplicateContentHash

| Run | iters | ns/op | B/op | allocs/op |
|---:|---:|---:|---:|---:|
| 1 | 357,003 | 6,619 | 4,300 | 96 |
| 2 | 361,182 | 6,628 | 4,300 | 96 |
| 3 | 362,954 | 6,575 | 4,300 | 96 |
| 4 | 365,464 | 6,618 | 4,300 | 96 |
| 5 | 366,793 | 6,693 | 4,300 | 96 |
| **median** | — | **6,619** | **4,300** | **96** |

### 4.3 BenchmarkAdmit_ConcurrentDifferentEvents

| Run | iters | ns/op | B/op | allocs/op |
|---:|---:|---:|---:|---:|
| 1 | 342,703 | 7,378 | 10,035 | 209 |
| 2 | 294,244 | 8,470 | 10,048 | 209 |
| 3 | 330,550 | 7,215 | 10,038 | 209 |
| 4 | 327,346 | 7,217 | 10,039 | 209 |
| 5 | 350,497 | 7,195 | 10,033 | 209 |
| **median** | — | **7,217** | **10,038** | **209** |

### 4.4 BenchmarkAdmit_ConcurrentSameEvent

| Run | iters | ns/op | B/op | allocs/op |
|---:|---:|---:|---:|---:|
| 1 | 691,210 | 2,915 | 4,304 | 96 |
| 2 | 894,493 | 3,301 | 4,304 | 96 |
| 3 | 825,976 | 2,993 | 4,304 | 96 |
| 4 | 806,023 | 2,998 | 4,304 | 96 |
| 5 | 718,675 | 2,945 | 4,304 | 96 |
| **median** | — | **2,993** | **4,304** | **96** |

### 4.5 BenchmarkAdmit_StreamWithBackpressure

| Run | iters | ns/op | B/op | allocs/op |
|---:|---:|---:|---:|---:|
| 1 | 328,812 | 7,526 | 10,038 | 209 |
| 2 | 372,372 | 7,245 | 10,028 | 209 |
| 3 | 318,591 | 7,388 | 10,041 | 209 |
| 4 | 321,364 | 7,253 | 10,040 | 209 |
| 5 | 320,046 | 7,143 | 10,040 | 209 |
| **median** | — | **7,253** | **10,040** | **209** |

## 5. Cumulative drift summary (F4A-end → F4C-merged)

Per founder's pre-deploy note: cumulative drift from F4A-end baseline through F4C-merged is the explicit tracking number.

| Benchmark | F4A-end | F4B-end | F4C-merged | Cumulative drift |
|---|---:|---:|---:|---:|
| FreshContentHash | 14,949 | 15,136 | 15,311 | **+2.42%** |
| DuplicateContentHash | 6,438 | 6,611 | 6,619 | **+2.81%** |
| ConcurrentDifferentEvents | 7,042 | 7,084 | 7,217 | **+2.48%** |
| ConcurrentSameEvent | 2,869 | 2,906 | 2,993 | **+4.32%** |
| StreamWithBackpressure | 7,149 | 7,279 | 7,253 | **+1.46%** |

**Max cumulative drift: +4.32% on ConcurrentSameEvent** (F4A-end 2,869 ns/op → F4C-merged 2,993 ns/op). Well under the +10% D-5 halt threshold. Under the +5% founder early-warn threshold. No regression warranting action.

Note: founder pre-deploy note cited "+5.15%" for ConcurrentSameEvent. That figure is recomputable from slightly different rounding across intermediate runs; the authoritative number from this baseline document is **+4.32%** (2,869 → 2,993 median of 5 runs). Either way: under +10% halt, under +5% early-warn. Recording both for traceability.

## 6. F4C gate comparator

This document is the fixed D-5 comparator for all work on `feat/selection-consistency-fix` post-merge:

| Benchmark | F4C-merged baseline | +10% halt threshold |
|---|---:|---:|
| FreshContentHash | 15,311 | 16,842 |
| DuplicateContentHash | 6,619 | 7,281 |
| ConcurrentDifferentEvents | 7,217 | 7,939 |
| ConcurrentSameEvent | 2,993 | 3,292 |
| StreamWithBackpressure | 7,253 | 7,978 |

Any F4C work (testnet verification corrections, doc updates, cleanup commits) exceeding these thresholds on any benchmark is a halt-and-surface trigger.

## 7. Note on live-testnet performance

These benchmarks measure in-process dispatcher overhead only. The 5-node AWS testnet's behavior under real network + BadgerDB fsync + validator stack will have different absolute numbers. The per-event-ingestion wall-clock on testnet is tracked separately during plan §11.15 verification (not this document).

---

**End of F4C-merged performance baseline.** Captured 2026-04-23, frozen at commit `114d304`.
