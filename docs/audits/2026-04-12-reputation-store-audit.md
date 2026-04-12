# Reputation Store Audit — 2026-04-12

**Type**: Read-only audit. No code changes, no config changes, no testnet mutations. The only artifact produced is this report.
**Scope target**: validator consensus reputation — the `(validator, family, category)` agreement-rate store that the multi-validator consensus design names `ValidatorReputationStore`. Worker/agent completion reputation (`internal/reputation/`) and blob-serving reputation (`internal/blobsync/serving_reputation.go`) are treated as sibling systems — noted where relevant, not audited here.
**Trigger**: Handoff `docs/handoff-2026-04-11-blobsync-accept-path.md` item #7 — "no reputation log entries were visible after the accept verdict" on 2026-04-11T17:52:30Z (commit `dcc7c17`, task `52c5b97a555f8d83dbcee9751ea73d62`).

---

## Executive Summary

**Question 3 category: (c) — Did not fire. No reputation code path executed for the first accept verdict on any of the 5 testnet nodes.** The root cause is not a log-level issue, not a calibration gate, and not a stale deploy — it is that `ValidatorReputationStore.RecordVote()` and `RecordEquivocation()` have **zero production call sites**. The writers exist (`internal/taskverification/reputation.go:77–161`) and are exercised by unit tests, but no consumer, no settler, and no slashing applicator invokes them; the `TaskVerificationConsensusConsumer` terminates at settlement and a slashing-action *log line* (`internal/recognition/task_verification_consensus_consumer.go:113–123`), and the `SlashingAction.ReputationPenalty` field is never applied anywhere. On Node 1's BadgerDB (the submission origin, where a write would be most likely), the reputation key prefix `tvr:` holds zero keys after a round that finalized as accept with 4 agreeing votes across 2 validators and 3 analyzer families. The store is a designed-in-spec, partially-built primitive: writer methods exist, the store is wired for reads (Q-score lookup by the settler returns the 1.0 neutral fallback for every validator), but the write path was not completed.

---

## Q1 — What exists today

### Packages and primary types

| Package | File | Primary type | Domain |
|---|---|---|---|
| `internal/taskverification` | `reputation.go:42` | `ValidatorReputationStore` | **Core — validator vote agreement reputation. Audit target.** |
| `internal/reputation` | `reputation.go` | `ReputationManager`, `AgentReputation` | Worker/agent completion reputation. Different domain; not audited. |
| `internal/replay` | `reputation_signal.go:8` | `ReputationSignal` (data type only) | Replay outcome → signal converter. Zero production consumers. |
| `internal/blobsync` | `serving_reputation.go` | `BlobServingReputation` | Per-peer blob-serving success rate. Out of scope (different dimension). |

### Core store data model (`internal/taskverification/reputation.go:20–30`)

```go
type ValidatorReputation struct {
    ValidatorID        crypto.AgentID        // line 21
    Family             verification.FamilyID // line 22
    Category           string                // line 23
    TotalVotes         uint64                // line 24
    AgreeingVotes      uint64                // line 25
    DeviatingVotes     uint64                // line 26
    AbstainedVotes     uint64                // line 27
    EquivocationEvents uint64                // line 28
    LastUpdated        int64                 // line 29
}
```

**Granularity**: per `(validator, family, category)`. Every tuple is a distinct record. `verification.FamilyID` values are the bootstrap analyzers (`deterministic_heuristic`, `statistical_structural`, `embedding_similarity`, `llm_semantic`).

### Persistence

- **Backend**: BadgerDB, held via `db *badger.DB` (`reputation.go:43`); protected by `sync.Mutex` (`reputation.go:44`).
- **Key prefix**: `const prefixValidatorReputation = "tvr:"` (`reputation.go:15`).
- **Key schema**: `"tvr:" + validatorID + ":" + familyID + ":" + category` (`reputation.go:52–54`).
- **Value**: JSON-marshaled `ValidatorReputation` (`reputation.go:117–123`, `157–160`).
- **Read path**: `Get()` returns a zero-valued record if not found, no error (`reputation.go:58–74`).

### Rebuildable from the DAG? — **Flagged: principle-5 concern is latent**

The store is intended to be authoritative consensus-adjacent state: `ValidatorQScore` feeds settlement Q-weighting (see Q5). Per `docs/design-principles.md` principle 5 ("The protocol is the source of truth — application state is a projection of the DAG, never the other way around"), the reputation state must be rebuildable from DAG replay of `TaskVerificationConsensus` + `TaskVerificationVote` events.

- No replay path exists for validator reputation. `internal/replay/reputation_signal.go` is a *data type* (`ReputationSignal`, `OutcomeToReputationSignal`) for worker-reputation replay signals — unrelated to `ValidatorReputationStore` — and even that type has zero production call sites (all references outside its own file are in `internal/replay/resolver_test.go:198–251`).
- No startup rebuild: `cmd/node/main.go:1865` constructs the store against the existing BadgerDB handle. If the BadgerDB is wiped (as the standard redeploy does — `CLAUDE.md`: "Always wipe `/data/aethernet/aethernet.db` … on testnet redeploy"), the reputation state is irrecoverable.

Today this principle-5 concern is **latent, not violated**, because the store has no writers (Q3): the empty persistent state is trivially equivalent to the empty DAG-replayed state. The moment writers are added without a rebuild path, principle 5 is violated. Flagged in §"Follow-up."

---

## Q2 — What wires into it

### Recognition-fabric consumers

Full inventory of consumers registered in `cmd/node/main.go` on `commitBus`:

| Consumer | Event type consumed | Does it touch reputation? |
|---|---|---|
| `TaskConsumer` | `EventTypeTaskPosted` | no |
| `TaskVerificationRoundConsumer` | `EventTypeTaskSubmitted` | no |
| `TaskVerificationVoteConsumer` | `EventTypeTaskVerificationVote` | no |
| `TaskVerificationConsensusConsumer` | `EventTypeTaskVerificationConsensus` | **no — see below** |
| `SettlementConsumer` | `EventTypeSettlement` | no |
| `OCSSubmitConsumer` / `OCSVoteConsumer` | OCS events | no |
| `EvidenceConsumer` | evidence events | no |
| `BlobDemandConsumer` | DAG events with BlobRefs | no (touches blob-serving reputation, not validator reputation) |

**No consumer writes to `ValidatorReputationStore`.**

### The consensus consumer path — traced in full

`TaskVerificationConsensusConsumer.Consume()` (`internal/recognition/task_verification_consensus_consumer.go:49–127`) is the only handler for `EventTypeTaskVerificationConsensus`. It does three things:

1. Loads the round, applies terminal state if not already set, saves the round (`:56–88`).
2. Calls `c.settler.Settle(...)` and logs `task_verification_consensus: settlement applied` (`:91–108`).
3. Calls `c.slashing.EvaluateRound(...)` and logs each returned action (`:112–123`).

The slashing block is reproduced verbatim for precision:

```go
if c.slashing != nil {
    actions := c.slashing.EvaluateRound(context.Background(), round)
    for _, action := range actions {
        slog.Info("task_verification_consensus: slashing action",
            "round_id", payload.RoundID,
            "validator_id", action.ValidatorID,
            "type", action.Type,
            "reason", action.Reason,
            "stake_penalty_bp", action.StakePenaltyBP,
            "reputation_penalty", action.ReputationPenalty,  // logged, never applied
        )
    }
}
```

`SlashingAction` (`internal/taskverification/slashing.go:31–37`) carries `ReputationPenalty int`, but nothing in the for-loop body or any callee applies it to the store. The struct is a pure descriptor; no applicator exists.

### Writers of the store — grep across the entire repo

Searching `RecordVote\(|RecordEquivocation\(` under `internal/`, `cmd/`, `pkg/`:

| File | Line(s) | Context |
|---|---|---|
| `internal/taskverification/reputation_test.go` | 25, 41, 64, 65, 66, 78, 79, 103, 105 | unit tests |
| `internal/taskverification/slashing_test.go` | 98, 101 | unit tests |
| `docs/plans/multi-validator-consensus-prompt-09.md` | 14 | prior-plan intent ("Reduce reputation via `ReputationStore.RecordVote(agreed=false)`") — never implemented |

**Zero non-test call sites.** Confirmed independently by two parallel investigators.

### Wiring in `cmd/node/main.go` — verbatim

- `cmd/node/main.go:1865` — `tvReputationStore := taskverification.NewValidatorReputationStore(stack.store.DB())`
- `cmd/node/main.go:1887–1892` — store wrapped as a **read-only** Q-score function for the settler:
  ```go
  validatorQFn := settlement.ValidatorQScoreFn(func(validatorID crypto.AgentID, family, category string) float64 {
      return tvReputationStore.ValidatorQScore(context.Background(), validatorID, verification.FamilyID(family), category)
  })
  ```
- `cmd/node/main.go:1901–1906` — store passed to `NewSlashingEvaluator` and the evaluator passed into `NewTaskVerificationConsensusConsumer`; consumer registered on `commitBus` at line 1907.

No `RecordVote`-adapter, no separate reputation consumer, no applicator for `SlashingAction.ReputationPenalty`.

### Blob-unavailability exclusion (`docs/blobsync-design.md` §6.4, line 485) — **not implemented**

Design rule: validators that abstain with `ReasonCodeBlobUnavailable` are excluded from the agreement-rate computation (blob unavailability is a network condition, not a quality judgment).

- `ReasonCodeBlobUnavailable` constant: `internal/roundprogress/types.go:113`.
- Emitted by autovalidator when blob fetch exhausts: `internal/autovalidator/auto.go:670, 693, 736`.
- In the reputation write path: **no check.** `RecordVote`'s only abstention handling is `if abstained { rep.AbstainedVotes++ }` (`reputation.go:108–109`), which **increments `TotalVotes` (line 107) regardless** — meaning an abstain counts toward the agreement-rate denominator without contributing to the numerator. This is the opposite of §6.4's intent. Because no production caller exists today, this is a latent design-code mismatch, not yet an observable harm; it becomes a harm the moment a writer is wired.
- `SlashingEvaluator.EvaluateRound` does correctly skip soft slashing for abstain verdicts (`slashing.go:112`: `!agreed && vote.Verdict != VerdictAbstain`), but that is a different code path and still does not write reputation.

### Locks and invariants

`TaskVerificationConsensusConsumer.Consume` runs on recognition-fabric worker goroutines (i.e., post-DAG-commit, *not* under `dag.mu`). It:
- does not send on the network,
- does not fetch blobs,
- does not take `TaskManager.mu` while doing any external I/O,
- takes the BadgerDB-internal lock via `SaveRound` and (indirectly via the settler) token-ledger locks.

No CLAUDE.md invariant violations today. If a `RecordVote` loop is added, the same analysis holds because `ValidatorReputationStore.mu` is a dedicated per-store mutex (`reputation.go:44`) with no outward calls inside the critical section — the lock is taken only around the BadgerDB read-modify-write (`reputation.go:86–123`).

### Replay path

`internal/replay/reputation_signal.go` is exclusively a data-type file for **worker** reputation (replay-match/mismatch/anomaly severity). It does not persist, does not subscribe, does not rebuild, and is unrelated to `ValidatorReputationStore`.

---

## Q3 — What fired on the first accept verdict

### Source: logs and BadgerDB inspection from all 5 testnet nodes, 2026-04-11T17:51:00Z–17:54:00Z.

### Phase A — consensus event landed on every node

| Node | IP | Local consensus event ID | Consumer invoked at | `settlement applied` count |
|---|---|---|---|---|
| 1 | 44.200.60.102 | `a517ceaf…` | 17:52:30Z | 2 |
| 2 | 3.87.68.158 | `73fdf5f7…` | 17:52:30Z | 0 (5× `settlement failed: insufficient balance`) |
| 3 | 100.27.227.231 | `106c7489…` | 17:52:30Z | 0 (4× `settlement failed`) |
| 4 | 3.232.95.111 | `fc0193b1…` | 17:52:30Z | 1 self-origin (+3 `settlement failed`) |
| 5 | 32.195.67.127 | `e2e4e3b2…` | 17:52:30Z | 1 self-origin (+3 `settlement failed`) |

Representative Node 1 log lines (verbatim):
```
2026/04/11 17:52:30 INFO task_verification_vote: round finalized by vote round_id=411ae4aaf8f9c62dd4cbe6c6da86352f5c329225a90de8248be49c8b378e0ab2 task_id=52c5b97a555f8d83dbcee9751ea73d62 verdict=pass reason=accept_supermajority score_bp=6742
2026/04/11 17:52:30 INFO task_verification_consensus: settlement applied task_id=52c5b97a555f8d83dbcee9751ea73d62 verdict=pass worker_payout=73000 poster_refund=0 treasury=2000 total_distributed=100000
```

The `settlement failed: insufficient balance` entries on Nodes 2/3/5 are orthogonal to this audit (ledger-divergence issue; pre-existing handoff item #1-adjacent). They confirm the consumer's `Consume()` entered, proving the recognition-fabric dispatch fired on every node.

### Phase B — reputation code path on every node

Queries (per node) covering 17:51:00Z–17:54:00Z and the full container uptime since 2026-04-11T16:38Z deploy, patterns `reputation|Reputation|AgreementRate|QScore|q_score|ValidatorReputationStore|tvr:|RecordVote|RecordEquivocation|calibration|slashing action`:

| Node | reputation lines (window) | reputation lines (day) | `slashing action` lines | `calibration` lines |
|---|---|---|---|---|
| 1 | 0 | 0 | 0 | 0 |
| 2 | 0 | 0 | 0 | 0 |
| 3 | 0 | 0 | 0 | 0 |
| 4 | 0 | 0 | 0 | 0 |
| 5 | 0 | 0 | 0 | 0 |

Log level is not the issue: `task_verification_consensus: settlement applied` is an `slog.Info` line (`:99`) and was emitted; a hypothetical `slog.Info("task_verification_consensus: slashing action", ...)` (`:115`) at the same logger level would have been emitted. It was not, because `EvaluateRound` returned an empty `actions` slice — consistent with a round where all votes agreed with consensus (`verdict=pass`, 4 pass votes across 2 validators × 3 families: d839… emitted deterministic_heuristic+statistical_structural; 741225… emitted deterministic_heuristic+embedding_similarity).

The `SlashingEvaluator` by design produces actions only for `!agreed` or equivocation (`slashing.go:108–157`). Even when actions *are* produced, the handler only logs them — it never writes to `ValidatorReputationStore`.

### Phase C — BadgerDB state

Node 1 (submission origin; most likely site of any write):
- `/data/aethernet/aethernet.db` copied to `/tmp` for read-only inspection, then removed.
- Opened read-only using the repo's own `github.com/dgraph-io/badger/v4` dependency, iterated every key.
- Total keys: **165**. Prefix breakdown: `evt:74`, `txf:41`, `meta:22`, `idn:8`, `val:6`, `stk:5`, `tv:4` (round objects, not reputation), `rp:2`, `esc:1`, `tsk:1`, `key:1`.
- **`tvr:` (reputation) keys: 0. `cal:` (calibration) keys: 0.**

Nodes 2–5: `/data/aethernet/aethernet.db/` intact, sizes 140K–200K, `.mem` memtable mod-time 17:52 (consistent with round-object + settlement-journal writes), `.vlog` unchanged since startup. By symmetry of code path and consumer wiring, and given Node 1's empirical zero, Nodes 2–5 are highly likely at zero `tvr:` keys as well. Deep inspection not repeated on Nodes 2–5 to avoid unnecessary data handling.

### Phase D — root cause (why (c), not (b))

Not silent. Not gated. The code path literally does not exist. Evidence:

1. **Grep of production source** for `RecordVote\(|RecordEquivocation\(` returns only `*_test.go` files (listed in Q2).
2. **Consumer inspection** (`task_verification_consensus_consumer.go:49–127`) shows no reputation write call. The slashing block (`:112–123`) terminates at `slog.Info`.
3. **Applicator check**: `SlashingAction.ReputationPenalty` is never read anywhere in production (grep `ReputationPenalty` under `internal/`, `cmd/`: only the field definition at `slashing.go:36` and its population sites at `slashing.go:117` — no reader).
4. **Q-score fallback confirms**: Settlement on the first accept verdict distributed `validator_pool=23000` across 2 agreeing validators. Because every validator's `TotalVotes==0`, `ValidatorQScore` returns `1.0` (neutral) per `reputation.go:186–188`. With equal Q, Q-weighting degenerates to even split: 11,500 µAET per agreeing validator — consistent with observed settlement output.

**Category (c): did not fire. Root cause: writer methods implemented, never called in production.** The prior-plan intent at `docs/plans/multi-validator-consensus-prompt-09.md:14` ("Reduce reputation via `ReputationStore.RecordVote(agreed=false)`") was not translated into consumer code.

---

## Q4 — What is observable today

### HTTP API

- `GET /v1/agents/{id}/reputation` (`internal/api/server.go:443`, handler `:2053–2065`) — returns `AgentReputation` (worker completion), **not** `ValidatorReputation`.
- `GET /v1/reputation/rankings` (`:444`, handler `:2067–2084`) — agent leaderboard, **not** validators.
- **No endpoint exposes `ValidatorReputationStore` state.** No `/v1/validators/…/reputation`, no Q-score query, no tvr-prefix dump.

### CLI

- `cmd/aet/main.go:723–757` exposes `aet agents --limit N` which queries `/v1/agents/leaderboard?sort=reputation` — worker reputation only.
- **No CLI command reads `ValidatorReputationStore`.**

### On-disk inspection

- Possible only by copying the BadgerDB file and iterating the `tvr:` prefix — requires direct disk access and a Go program (or equivalent Badger tool). No bundled inspector, no JSON export, no documented operator path.

### Log output

- `slashing: systematic divergence detected` (`slashing.go:133–140`) surfaces `agreement_rate` — but only if the 30% threshold is breached *and* ≥50 votes exist (`:124`), i.e., effectively never today (TotalVotes==0 for every validator).
- `task_verification_consensus: slashing action` (`:115`) would log `reputation_penalty` — but only on deviation/equivocation, which did not occur on the accept verdict.
- **No log line surfaces current Q-score or current agreement rate under normal operation.**

### Verdict

Answer to all four observability questions: **no.** A consensus-affecting signal (validator Q in fee distribution) is computed from state that cannot be inspected by any API, any CLI, any log, or any bundled tool. Flagged under principle 15 in §"Follow-up."

---

## Q5 — What the reputation model is actually modeling

### What is tracked

- **Verdict agreement only.** `AgreementRate = AgreeingVotes / TotalVotes` (`reputation.go:34–39`). No score-distance tracking, no latency tracking, no cross-validator correlation, no contributor dimension.
- **Separate counters** for abstain (`AbstainedVotes`, `:27`) and equivocation (`EquivocationEvents`, `:28`) are maintained but do not feed the Q-score formula.
- **Comment acknowledges scope** (`reputation.go:167–175`): only α₄ (Consistency) of the paper v4.1 Q formula is implemented; α₁ (CVD_norm), α₂ (ChallengeSurvival), α₃ (ReplicationRate) are explicitly deferred.

### Decay / window

- **Cumulative forever.** No decay function, no rolling window, no half-life. `LastUpdated` (`:29`, `:115`, `:155`) is written but never read.

### Abstention handling bug (design-code mismatch)

`RecordVote` (`reputation.go:107–114`):
```go
rep.TotalVotes++
if abstained {
    rep.AbstainedVotes++
} else if agreed {
    rep.AgreeingVotes++
} else {
    rep.DeviatingVotes++
}
```

Every call increments `TotalVotes`. An abstained vote inflates the denominator without adding to the numerator — directly reducing `AgreementRate`. `docs/blobsync-design.md` §6.4 requires the opposite for `ReasonCodeBlobUnavailable` abstains. Because there is no production caller today, this does not currently produce wrong Q-scores; it is a latent defect surfaced only when the write path is wired.

### All readers

| Call site | File:line | Consensus-affecting decision |
|---|---|---|
| Settler Q-weighted distribution | `internal/settlement/verification_consensus_settler.go:330–394`, invoked for accept at `:160`, for reject at `:227` | 23% validator pool distribution. Today degenerate to even split because all Q == 1.0. |
| Slashing systematic-divergence check | `internal/taskverification/slashing.go:122–143` | Would trigger 10% stake slash if `rate < 0.30 && TotalVotes >= 50`. Inert today (all TotalVotes==0). |
| `cmd/node/main.go:1887–1891` `validatorQFn` closure | `cmd/node/main.go:1887` | Wraps `ValidatorQScore` for the settler; not itself a decision point. |

**No other readers.** `ValidatorQScore` is not consulted for round opening, vote weighting, committee selection, or any other consensus-affecting path. Today the store functions as a **stub: writer methods implemented but unreachable; read path reaches only a 1.0-neutral fallback.**

### Sibling systems noted (out of scope for this audit)

- `internal/reputation/ReputationManager` — worker/agent completion reputation. Separate store, separate persistence (`LoadFromStore` at `cmd/node/main.go:939`). Not part of validator Q-score.
- `internal/blobsync/serving_reputation.go` — per-peer blob-serving success rate. Per `docs/blobsync-design.md` §7 (line 521–524), intended as a new dimension on `ValidatorReputationStore`, but implemented as a separate package per the Prompt-02 plan (`docs/plans/blobsync-prompt-02.md:21`). Status not audited here — flagged in §"Related" for a follow-up pass.

---

## Q6 — What independence-weighted verification would need

The data-ingestion workstream's independence-weighted verification needs, at round-open time, to answer a query of roughly this shape for a given validator V and contributor C:

> *"Over the last N rounds in analyzer family F and category CAT, how correlated has V's verdicts been with other validators in F on tasks submitted by contributors related to C (by affiliation, by repeat engagement, by shared manifest policy)?"*

That query requires:

1. **Per-round verdict history** keyed by `(round, validator, family)` — not cumulative counters.
2. **Pairwise agreement tracking** between validator pairs (to compute correlation, not individual agreement rate).
3. **A time window or decay function** so "recent" and "historical" can be distinguished.
4. **A contributor dimension** so V's agreement conditional on C's submissions can be isolated from V's agreement overall.

The current store answers only: *"What is V's cumulative agreement rate in (F, CAT)?"* That is insufficient on every axis above.

**Missing from the current model**:
- No per-round records — only cumulative counts.
- No pair-keyed records — only single-validator records.
- No rolling window or decay — cumulative forever; `LastUpdated` written but unread.
- No contributor/poster dimension — key schema is `(validator, family, category)` only.

This section intentionally does not propose a redesign. The gap is named; the shape of the fix is a subsequent design decision.

---

## Flagged for follow-up

1. **Writer absence — primary finding** (`internal/recognition/task_verification_consensus_consumer.go:112–123`, `internal/taskverification/slashing.go:31–37`, `cmd/node/main.go:1901–1906`). `ValidatorReputationStore.RecordVote` and `RecordEquivocation` have zero production call sites; `SlashingAction` is a pure descriptor with no applicator. The consumer logs and drops. Consequence today: Q-weighted validator fee distribution is degenerate (all Q=1.0, even split) and systematic-divergence slashing is inert (all `TotalVotes==0`).

2. **Principle 5 latency — rebuild path absent** (`internal/replay/reputation_signal.go`, `cmd/node/main.go:1865`). The store is designed as authoritative state but has no DAG-replay rebuild. If BadgerDB is wiped (standard redeploy procedure), all reputation state is lost. Violation is latent only because the store is empty today; becomes active the moment writers are wired. Per `docs/design-principles.md` principle 5: *"application state is a projection of the DAG, never the other way around."*

3. **Principle 15 — reputation state is unobservable** (`internal/api/server.go`, `cmd/aet/main.go`, `cmd/node/main.go`). No HTTP endpoint, no CLI command, no log line surfaces current validator Q-score or agreement rate under normal operation. Per `docs/design-principles.md` principle 15: *"any protocol decision that depends on validator state must be based on observable evidence."* Today, Q is used in settlement (23% distribution) but cannot be externally observed. Any future fix that wires writers must also wire observability.

4. **Blob-unavailability exclusion not implemented** (`internal/taskverification/reputation.go:77–124`, `docs/blobsync-design.md:485`). `RecordVote` increments `TotalVotes` for abstained votes, directly penalizing `AgreementRate`. §6.4 requires `ReasonCodeBlobUnavailable` abstains to be excluded from the rate entirely. The current `RecordVote` signature has no `reasonCode` parameter; design-code mismatch. Latent today (no writers); activates when writers wire.

5. **Model is insufficient for independence-weighted verification** (Q6). Missing per-round history, pairwise tracking, time-window/decay, and contributor dimension. The data-ingestion workstream cannot build on this store as-shaped.

6. **No decay / no rolling window** (`reputation.go:115, 155`). `LastUpdated` is written but never read. Cumulative-forever scoring means a validator's past never ages out, which interacts badly with analyzer-version churn and with any future onboarding where fresh committees build fresh baselines.

## Related subsystems worth a separate audit

- **Calibration store** (`internal/taskverification/calibration.go`, `cmd/node/main.go:1868–1871`). `CalibrationStore.Increment` has the same property as `RecordVote`: zero production call sites (test-only). The `cal:` prefix on Node 1's BadgerDB was also empty. Calibration gating in the slashing evaluator (`slashing.go:100–106`) currently fails-safe (if `IsCalibrated` returns false or error, skip slashing) — which, combined with no writers, means calibration is permanently pre-calibration across all `(category, family)` pairs, which silently disables the hard-slash path. Worth a standalone audit pass.
- **`BlobServingReputation`** (`internal/blobsync/serving_reputation.go`). Per the BlobSync design, peer serving reputation should feed Q-score. Not audited here; check whether it has the same "writers exist, no production callers" shape.
- **Generation-ledger ancestor Q** (`cmd/node/main.go:1878–1881`). Hardcoded 1.0 neutral with a TODO; not reputation per se but the same degenerate-Q condition.

## Evidence inventory

**Code citations** (all paths relative to `/Users/michaelschreiber/aethernet/`):
- `internal/taskverification/reputation.go:15, 20–30, 34–39, 42–44, 52–54, 77–124, 127–161, 163–194`
- `internal/taskverification/slashing.go:31–37, 40–63, 87–161`
- `internal/recognition/task_verification_consensus_consumer.go:18–22, 49–127`
- `internal/replay/reputation_signal.go:1–71`
- `internal/settlement/verification_consensus_settler.go:27–49, 160, 227, 330–394`
- `internal/api/server.go:443–444, 2053–2084`
- `cmd/node/main.go:1865, 1868–1871, 1887–1892, 1901–1907`
- `docs/plans/multi-validator-consensus-prompt-09.md:14`

**Testnet citations** (all nodes inspected 2026-04-12; log window covers verdict at 2026-04-11T17:52:30Z):
- Node 1 (44.200.60.102) — `docker logs aethernet` window 17:51–17:54Z: consensus finalized 17:52:30Z; 0 reputation lines; BadgerDB `/data/aethernet/aethernet.db` copied to `/tmp/db_audit`, iterated, `tvr:` key count 0 of 165 total, copy removed.
- Node 2 (3.87.68.158) — consensus recognized 17:52:30Z (local event `73fdf5f7…`); 0 reputation lines day-wide.
- Node 3 (100.27.227.231) — consensus recognized 17:52:30Z (local event `106c7489…`); 0 reputation lines day-wide.
- Node 4 (3.232.95.111) — consensus recognized 17:52:30Z (local event `fc0193b1…`); 0 reputation lines day-wide.
- Node 5 (32.195.67.127) — consensus recognized 17:52:30Z (local event `e2e4e3b2…`); 0 reputation lines day-wide.

No testnet state was modified during the audit. DB copies were read-only and removed post-inspection.
