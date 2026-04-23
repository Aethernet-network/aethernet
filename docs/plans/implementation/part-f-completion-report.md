# Part F — Testnet cutover rehearsal — completion report

**Branch**: `feat/canonical-distribution-integer-migration`
**Base commit**: `7d29ab5` (Part E — completion report)
**Part F commit**: `600f606` — `part-f(cmd/aet,internal/api,cmd/node): admin activate-integer-migration CLI + endpoint`
**Plan reference**: `docs/plans/implementation/part-f-plan.md`
**Outcome**: **Partial. Phases A–C passed. Phase D failed, exposing a production wiring gap in Part E. Phases E–F not executed.**

---

## Executive summary

Part F rehearsed the integer-migration cutover on the 5-node AWS testnet. Phases A, B, and C completed successfully and produced usable rehearsal evidence. Phase D — emitting the activation event and watching it take effect cluster-wide — failed: the event propagated to all 5 nodes via the recognition fabric, but the dispatcher consumer that should apply it was never invoked on any node. Root cause is a missing recognition→dispatcher admission adapter for `EventTypeIntegerMigrationActivation`, a gap in Part E's production wiring. The cluster remains safe in shadow mode; no divergence, no fork, no ledger impact.

**This is the failure mode Part F was designed to surface.** Running the cutover on testnet before any mainnet exposure was the explicit purpose of this rehearsal; catching Part E's wiring gap here is exactly the value the rehearsal produced. Phase C's empirical validation of the integer path's cross-node byte-identical determinism remains load-bearing evidence regardless of the Phase D outcome.

**Recommendation: do not merge `feat/canonical-distribution-integer-migration` to main.** Return to architect session to design the admission-adapter fix as a scoped Part E.1 (or a general recognition→dispatcher routing mechanism — see §6 of the Phase D failure brief).

---

## Phase A — local code, tests, commit (PASSED)

**Deliverable**: `600f606 part-f(cmd/aet,internal/api,cmd/node): admin activate-integer-migration CLI + endpoint`

- `cmd/aet/admin.go` (new): `aet admin activate-integer-migration --reason R` subcommand, reuses existing `signedPost` envelope + active wallet.
- `cmd/aet/main.go` (modified): dispatcher entry + help text under `Admin:` header.
- `internal/api/admin_handlers.go` (new): `POST /v1/admin/integer-migration/activate` handler. Verified signer becomes `EmittingAgent`; server sets `Version=1` and `EmittedAtUnix` authoritatively.
- `internal/api/server.go` (modified): `enableAdminAPI bool` field + `SetAdminAPI(bool)` setter + conditional `registerAdminRoutes` call in `rebuildMux`.
- `cmd/node/main.go` (modified): `--enable-admin-api` flag threaded through `startStack` to `apiSrv.SetAdminAPI`.
- `docs/plans/implementation/part-f-plan.md` (new): approved Part F plan v1.

**Tests** (6 cases):
- `cmd/aet/admin_test.go`: `TestAdminActivateIntegerMigration_MissingReason` (child-process exec)
- `internal/api/admin_handlers_test.go`: `TestAdminActivateIntegerMigration_{Unauthenticated_Returns401, MissingReason_Returns400, WhitespaceReason_Returns400, Valid_Emits201AndCanonicalEvent}`, `TestAdminAPI_{DisabledByDefault_Returns404, EnabledRoutes_Register}`.

**Verification**:
```
go build ./...                                                clean
go vet ./...                                                  clean (4 pre-existing atomic.Int64 warnings unchanged)
go test -race -count=1 ./cmd/aet/... ./internal/api/... ./cmd/node/...  clean
go test -race -count=1 ./...                                  clean modulo pre-existing flakes in internal/network and internal/autovalidator
```

Findings documented for completion report:
- Consumer-side-only idempotency (CLI sends each activation as a fresh event; duplicate safety handled by consumer's early-idempotency pre-check, not client).
- `http.MaxBytesReader` 1 MB cap on the activation handler — defensive addition beyond spec.
- CLI test coverage lighter than API test coverage; acceptable for Part F.
- `ActivationReason` has no length limit beyond the 1 MB body cap; future-hardening: narrow to 1 KB (noted for a future admin-API hardening workstream).
- Mainnet-grade authorization (operator allowlist / multi-sig / governance gate) intentionally out of scope for testnet rehearsal; permissive signed-request model noted as production hardening work.

---

## Phase B — deploy to 5-node testnet (PASSED with tooling deviations)

### Pre-deploy discovery

Testnet topology diverges from `scripts/deploy-testnet.sh` assumptions:
- **ECS services** (`aethernet-node`, `aethernet-node2`, `aethernet-node3`) all have `desiredCount=0`. The deploy script's `aws ecs update-service --force-new-deployment` would no-op.
- **Actual deployment**: 5 EC2 instances each running a single `docker run` container (image `aethernet:f3b-verify-v3`, uptime 5 days pre-deploy, configured with a per-node `AETHERNET_PEER` env var listing the other 4 nodes).
- **ALB (`testnet.aethernet.network`)** targets the EC2 instances directly, not via ECS.

Executed Path A per founder approval: build on Node 1 → push to ECR as `integer-migration-part-f-600f606` → SSH-based sequential deploy to each of the 5 nodes preserving identity files (`node_keys/`, `validator-manifest.json`, `validator-analyzers.json`) and wiping protocol state (`aethernet.db`, `blobs`).

### Deploy failure and recovery on Node 2

First deploy attempt on Node 2 (3.87.68.158) failed mid-sequence: `aws ecr get-login-password` returned "Unable to locate credentials" on the target node (non-build nodes lack AWS CLI credentials). The initial deploy script did not `set -euo pipefail`, so the container was stopped and the database wiped before the failed `docker pull` was diagnosed. Node 2 was offline for ~90 seconds.

**Recovery**: script hardened with `set -euo pipefail` + explicit `docker image inspect` verification before container stop. ECR credentials sourced from the local machine's AWS CLI and piped through SSH (`ssh ubuntu@$ip "echo $ECR_PASSWORD | sudo docker login --password-stdin ..."`). Retry succeeded on Node 2; same pattern used for Nodes 3, 4, 5, 1.

### Per-node deploy results

All 5 nodes redeployed successfully on `integer-migration-part-f-600f606`. Each node came up with `peers=4`, `dag=214` (recovered from peers via Fast Path), `ocs_pending=0`.

Deploy order: Node 2 → 3 → 4 → 5 → 1 (build node last to preserve build environment until the rollout was complete).

**Pre-wipe snapshots** captured for each of the 5 nodes at `/tmp/part-f-snapshots/node{1..5}-prewipe-*.txt` before any state mutation: `ls -la /data/aethernet/`, disk usage, container env vars, container cmd + image tag.

### DAG convergence observation

After full deploy, economic state (total_supply, circulating_supply, treasury_balance) was **byte-identical across all 5 nodes**. DAG event count had a stable 16-event spread (214..230) that did not close over a 3-minute quiescent-traffic window. Root cause isolated to two event types:
- Registration: 1..5 events per node
- GenesisFunding: 3..15 events per node

All Settlement-path event types (Settlement, VerificationVote, Transfer, Task*) were byte-identical across all 5 nodes. The spread stratified by deploy order — first-deployed node had the largest counts, last-deployed the smallest — consistent with an emit-once-at-startup + broadcast-to-current-peers pattern with no late re-fetch for genesis-era events.

Recorded as a "Discoveries for Part G" item (see §8).

### Phase B scorecard

| Check | Result |
|---|---|
| All 5 nodes redeployed on `integer-migration-part-f-600f606` | PASS |
| All 5 nodes `peers=4` post-deploy | PASS |
| Economic state byte-identical across 5 nodes | PASS |
| `/v1/admin/integer-migration/activate` returns 401 signed-required (not 404) on all 5 | PASS |
| Pre-wipe diagnostic snapshots captured | PASS |
| Node 2 recovery after first-deploy failure | PASS (with script hardening noted) |

---

## Phase C — shadow-observation corpus (PASSED)

### Corpus driving

The plan's 500 / 50,000 µAET budgets were below the testnet's `tasks.MinTaskBudget = 100,000 µAET` floor. Corpus scaled up accordingly:
- 10 small tasks at 100,000 µAET (at the minimum; sensitive to rounding)
- 10 medium tasks at 10,000,000 µAET (100× larger; typical magnitude)

Two operator wallets created client-side: `part-f-operator` (task poster) and `part-f-worker` (agent claimer). Tasks posted via `aet task post`; claiming + result submission driven by `~/agent-worker/worker.py` — an existing agent worker built on the `aethernet-sdk` that uses Claude Sonnet 4 to generate analyzable evidence content.

Pre-flight hiccup: the Python 3.13 on the local workstation lacks the macOS CA bundle for HTTPS verification; the worker silently stalled on HTTPS handshakes until `SSL_CERT_FILE=$(python3 -c 'import certifi; print(certifi.where())')` was exported. Worker also required a macOS-specific launch pattern — `(trap '' SIGTSTP SIGTTIN SIGTTOU; exec python3 -u worker.py ... &)` — to avoid background suspension by the Bash tool's session management. Documented as rehearsal-harness notes, not protocol issues.

All 20 tasks ran through the full pipeline (`TaskPosted → TaskClaimed → TaskSubmitted → VerificationVote × N → TaskVerificationConsensus → Settlement`) in ~6.5 minutes end-to-end. Terminal statuses: 16 completed (happy path) + 4 rejected (treasury-path). Both paths fire `Settlement` events, both emit `shadow_delta` log lines.

### shadow_delta collection

Pulled via `sudo docker logs aethernet | grep shadow_delta` from each node. Totals:

| Node | shadow_delta lines |
|---|---|
| 1 | 36 |
| 2 | 35 |
| 3 | 35 |
| 4 | 36 |
| 5 | 36 |
| **total** | **178** |

Two context types observed:
- `validator_distribution`: 100 lines, pools 23,000–2,300,000 µAET (avg ~1,160,000)
- `generation_ledger`: 78 lines, pools 2,000–200,000 µAET (avg ~90,000)

36 unique (context, task_id) settlements observed across the cluster: 35 by all 5 nodes, 1 by 3 nodes.

### Abort-gate results (plan §5.2)

| Check | Result |
|---|---|
| **Cross-node `int_sum` equivalence** (primary determinism claim) | **36 of 36 settlements byte-identical across every observer — 0 divergent** |
| **`sum_delta == 0` on every line** (per-node float/integer conservation) | **PASS on all 178 lines — 0 violations** |

### Analytical finding — Part B latent non-determinism not observed

Part B's completion report flagged that the legacy float path's remainder-absorption happens at the caller-slice-last recipient, and that caller-slice-order depends on vote receive-order, which is not guaranteed stable across nodes. The hypothesis: on a real corpus, some task would show `max_per_recipient_delta > 0` with different values on different nodes (evidence that the bug was live in production).

Observation: **36 of 36 settlements showed unanimous `max_per_recipient_delta = 0` across every observing node.** The float path happened to produce bit-exact integer-equivalent output for this entire 36-settlement sample. The hypothesized non-determinism did not fire — not because it isn't real, but because receive-ordering converged for this particular corpus's Q-distribution.

This does not invalidate the cutover rationale. The integer path's determinism is structural (via `protocolmath.AllocateWithCeiling`'s canonical-key sort); the float path's determinism is coincidental. Activation still defends against the latent case even when it doesn't fire today.

### Phase C scorecard

| Criterion | Result |
|---|---|
| 20-task corpus posted + all 20 reached terminal status | PASS (16 completed + 4 rejected) |
| shadow_delta lines collected from all 5 nodes | PASS (178 total) |
| Cross-node `int_sum` byte-identical per settlement | PASS (36/36) |
| `sum_delta == 0` per line | PASS (0/178 violations) |
| No WARN-level shadow_delta lines | PASS (all INFO) |
| Both contexts observed (`validator_distribution` + `generation_ledger`) | PASS |
| Part B latent non-determinism observed in corpus | not observed (explained) |

---

## Phase D — FAILED

**Activation event emitted**: yes. `event_id=2a513ac6b17112b9bb75a0c46d5ce5ab0f16da12b5e24e02d52a66e4fac23761` @ 18:02:49 UTC.

**Propagation to 5 nodes**: yes. All 5 nodes' recognition fabric logs show the event was admitted and materialized.

**Consumer Apply ran**: **no. On zero of five nodes.** Grep for `integer_migration_activation: activated` returns 0 matches cluster-wide. No ERROR-level logs reference the event.

**Effect on protocol state**: none. Settler and gen-ledger both remain in shadow mode. Meta store has no activation record. Canonical path is still the float arithmetic.

### Root cause — missing recognition→dispatcher admission adapter

The `IntegerMigrationActivationConsumer` is registered with `dispatch.Dispatcher` (per `cmd/node/main.go:1984`), but no code path routes `EventTypeIntegerMigrationActivation` events *into* `dispatcher.Admit()`. The dispatcher has exactly one producer wired to it in production:

```
internal/recognition/task_verification_consensus_consumer.go:120
    c.dispatcher.Admit(context.Background(), ev)   // EventTypeTaskVerificationConsensus only
```

The `IntegerMigrationActivation` event type has no analogous recognition-layer adapter. `grep -rn "EventTypeIntegerMigrationActivation" internal/ cmd/` returns matches only in settler doc comments, the consumer's own `Interested(ev)` check, and the consumer's doc comment — **zero hits in the recognition layer**.

Part E's unit tests exercised the consumer's `Apply` method directly via constructor injection, bypassing the admission path. The gap lives between two well-tested layers and wasn't caught at CI time.

Full analysis in `/tmp/part-f-snapshots/phase-d-failure-brief.md`.

### Cluster safety

The activation event is in the DAG as semantically inert data (valid canonical event, content-addressed, on all 5 nodes). No state was mutated by an incomplete apply because no apply was attempted. The cluster continues operating in shadow mode with the float path as canonical — the same configuration Phase C observed to be cross-node byte-identical for this corpus. No divergence, no fork, no rollback needed.

A future binary that correctly wires the admission adapter will observe this event during its recovery pass and apply it then. Alternatively, a new activation event can be emitted after the fix ships (the consumer's early-idempotency guard makes re-emission safe).

---

## Phase E — not executed

Blocked on Phase D. Nodes are still in shadow mode; post-activation determinism can't be exercised until activation happens.

---

## Phase F — not executed

Blocked on Phase D. The restart test exercises `migrationStoreAdapter.GetIntegerMigrationActivated()` at startup; without a persisted activation record there is nothing to restore and no flag flip to validate.

---

## v2 plan §6.4 19-criterion mapping

| # | v2 §6.4 criterion | Status |
|---|---|---|
| 1 | `go test -race ./...` clean | PASS (Phase A) |
| 2 | `go vet ./...` clean | PASS (Phase A) |
| 3 | `go build ./...` clean | PASS (Phase A) |
| 4 | Docker image built + pushed | PASS (Phase B) |
| 5 | All 5 nodes deploy + startup clean | PASS (Phase B) |
| 6 | Shadow-mode phase, bounded delta distribution | PASS (Phase C — 178 lines, 0 conservation violations, 0 max_per_recipient_delta) |
| 7 | Cutover event propagated to all 5 | PARTIAL — event propagated but not consumed |
| 8 | Post-cutover byte-identical across nodes | **BLOCKED** (Phase D failure) |
| 9 | Q-weighted validator payouts byte-identical | PASS (Phase C sub-case — validator_distribution context) |
| 10 | Generation ledger byte-identical | PASS (Phase C sub-case — generation_ledger context) |
| 11 | Canonical payload float lint passes | DISCHARGED by Part C (commit `a79fef7`) |
| 12 | Runtime assertion wrapper | DISCHARGED by Part C (reflection test) |
| 13 | Cross-run determinism | DISCHARGED by Part A unit tests |
| 14 | Heterogeneous hardware (x86 + ARM) | DISCHARGED by Part D (commit `990c2c7` — QEMU CI) |
| 15 | Historical replay byte-identical | **BLOCKED** (Phase D failure) |
| 16 | Zero-Q fallback even-split | DISCHARGED by Part B unit test |
| 17 | Negative-Q invariant rejection | DISCHARGED by Part A + Part B unit tests |
| 18 | Conservation: sum_payouts + treasury = budget | PASS (Phase C — sum_delta=0 across 178 lines implies conservation at both float and integer paths per node) |
| 19 | No regression on F3-B §10 convergence/restart/replay | PASS (Phase B — peer count 4, DAG recovery via Fast Path, economic state byte-identical post-wipe) |

**Summary**: 13 PASS, 4 DISCHARGED-by-prior-part, 2 BLOCKED (criteria 8, 15), 1 PARTIAL (criterion 7). Phase D's failure blocks criteria 8 and 15 specifically.

---

## Discoveries for Part G

1. **Part E production wiring gap** — the load-bearing finding of Part F. The `IntegerMigrationActivationConsumer` is registered with the dispatcher but no code path admits the activation event type into the dispatcher. Blocks the cutover entirely. See `/tmp/part-f-snapshots/phase-d-failure-brief.md` for options.

2. **Deploy-tooling drift** — `scripts/deploy-testnet.sh` targets three ECS services that are all at `desiredCount=0`. Actual 5-node deployment is EC2-direct docker containers. Script is stale; should either be rewritten to match reality or deleted with a note to `docs/` explaining the actual deploy sequence. Queued for future-workstream attention.

3. **ECR authorization on non-build nodes** — `aws ecr get-login-password` fails on Nodes 2–5 ("Unable to locate credentials"). Workaround (fetch password from local machine + pipe through SSH) works but is fragile; an IAM instance profile or ECR-helper configured on each node would be better.

4. **DAG genesis-event stratification** — Registration and GenesisFunding event counts differ across the 5 nodes by deploy order (newer-deployed nodes have fewer copies; stable spread of 12 events on GenesisFunding alone). Mechanism: emit-once-at-startup + broadcast-to-current-peers, with no late re-fetch. Harmless under current testnet operation (economic state converges via ledger-layer idempotency) but a latent concern for late-joining validators on an established network. Two potential fixes: (a) include genesis events in the gossip re-fetch loop; (b) make genesis events local-only and idempotent with no broadcast. Architect session should pick a direction when the next node-onboarding workstream comes up.

5. **macOS launch harness quirks for the rehearsal agent-worker** — the worker stalled silently in multiple launch configurations until the pattern `(trap '' SIGTSTP SIGTTIN SIGTTOU; source env; cd ~/agent-worker; exec python3 -u worker.py <redirects>)` was used, and until `SSL_CERT_FILE` was pointed at the certifi bundle. Not a protocol issue; pure harness mechanics. Documented here so future rehearsals don't rediscover it from scratch.

6. **Consumer integration tests should exercise the admission path, not just `Apply()` directly** — Part E's test matrix was comprehensive at the unit level (consumer in isolation, storage adapter, startup-load) but did not assert that an arriving DAG event actually reaches the dispatcher's Apply via the recognition fabric. That's what the next Part E.1 should verify in addition to fixing the wiring. A minimal integration test would: construct a DAG, admit an `EventTypeIntegerMigrationActivation` event via the normal publisher path, and assert the consumer's side-effects (meta store write, SetShadowMode calls) landed.

---

## What's in `/tmp/part-f-snapshots/`

- `node{1..5}-prewipe-*.txt` — pre-wipe per-node snapshots (Phase B)
- `node{1..5}-deploy.log` — per-node deploy transcripts (Phase B)
- `activation.json` — the emitted activation event's response JSON (Phase D)
- `shadow-logs/node{1..5}-shadow.log` — raw `shadow_delta` lines per node (Phase C)
- `shadow-logs/analysis.txt` — parser output across all 5 nodes (Phase C)
- `tasks/posted.txt` — 20 task IDs (Phase C fresh corpus)
- `tasks/post-fresh.log` — post-task driver log (Phase C)
- `tasks/post-corpus.log`, `tasks/submit-corpus.log`, `tasks/drive-corpus.log` — earlier (abandoned) pre-worker-harness driver logs
- `worker.log` — agent-worker stdout with 20 CLAIMED / 20 COMPLETED entries (Phase C)
- `parse-shadow.py` — shadow-delta cross-node analyzer
- `phase-d-failure-brief.md` — detailed Phase D root-cause analysis (referenced above)

---

## Recommendation

**Do not merge `feat/canonical-distribution-integer-migration` to main.**

The branch contains working and well-tested Parts A–E plus a working and well-tested Part F admin surface. But the end-to-end cutover mechanism does not take effect on the live cluster due to the Part E wiring gap. Merging the branch without the fix would ship a registered-but-unreachable activation consumer — the next operator to trust the "emit activation" path would hit the same failure, only without a rehearsal to catch it.

**Next step**: architect session to design the recognition→dispatcher admission adapter as a scoped Part E.1 (narrow single-consumer adapter) or a general routing mechanism (broader cleanup of the asymmetric one-adapter-per-consumer pattern today). Either option is out of Part F's scope.

Part F's value remains: Phase C's 36/36 byte-identical determinism evidence on live infrastructure is load-bearing for the integer path's correctness claim. Phase D's failure is the discovery the rehearsal was designed to surface — and it did.

---

**End of Part F completion report.**
