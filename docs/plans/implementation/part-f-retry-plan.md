# Part F retry — testnet cutover rehearsal plan (post-E.1)

**Branch**: `feat/canonical-distribution-integer-migration`
**Base commit**: `d6196ed` (Part E.1 — general dispatcher admission router)
**First-attempt plan**: `docs/plans/implementation/part-f-plan.md`
**First-attempt report**: `docs/plans/implementation/part-f-completion-report.md`
**Phase D failure brief**: `docs/plans/implementation/part-f-phase-d-failure-brief.md`
**E.1 bug-class closure**: `docs/plans/implementation/part-e1-completion-report.md`
**Scope**: rehearsal execution only. No code changes on the feature branch other than this plan doc and the retry completion report. Fix the rehearsal, not the protocol.

---

## 0. Pre-plan sanity check results (2026-04-22 UTC, at planning time)

| Node | Image | `2a513ac6` mentions | `integer_migration_activation: activated` logs |
|---|---|---:|---:|
| 1 | `integer-migration-part-f-600f606` | 2 | 0 |
| 2 | `integer-migration-part-f-600f606` | 5 | 0 |
| 3 | `integer-migration-part-f-600f606` | 2 | 0 |
| 4 | `integer-migration-part-f-600f606` | 2 | 0 |
| 5 | `integer-migration-part-f-600f606` | 2 | 0 |

ALB reports `peers=4 dag=1005 ocs_pending=0`. Cluster is in precisely the state Option A assumes: first-attempt binary still running, inert activation event in every DAG, shadow-mode flags not flipped, some DAG growth since first-attempt end-state (expected — nodes have been processing background task traffic for hours). No incident signal.

Option A preconditions met. Retry can proceed as planned.

---

## 1. Scope recap (what this retry does and does not do)

**Does:**
- Deploy `integer-migration-part-e1-d6196ed` to all 5 testnet nodes, **preserving DAG state** (do not wipe `/data/aethernet/aethernet.db` or `/data/aethernet/blobs`).
- Verify that when the Part E.1 admission router wakes, it admits the historical inert `2a513ac6` event during `dispatcher.Recover(...)` — flipping the settler + gen-ledger out of shadow mode via the existing consumer's Apply.
- Validate integer-canonical settlement math is deterministic across nodes post-flag-flip.
- Emit a fresh activation event to prove the consumer's early-idempotency check correctly short-circuits on an already-activated network.
- Drive a 10-task post-activation corpus; confirm byte-identical per-recipient amounts cluster-wide + zero `shadow_delta` lines + conservation.
- Restart Node 5; verify startup-load correctly restores integer-canonical mode from the persisted meta-store record.
- Produce a §10-style completion report mapping back to the original Part F plan's 19 criteria.

**Does not:**
- Modify Parts A–E.1 code.
- Wipe any node DB.
- Emit multiple activation events in Phase D (one fresh emit is sufficient for the idempotency property).
- Rework deploy tooling. `scripts/deploy-testnet.sh` ECS drift and non-build-node ECR auth are documented-but-unresolved; carried forward from first-attempt discoveries.

---

## 2. Phase structure (retry deltas from first-attempt Part F plan)

### Phase A — skipped

Complete in commit `600f606` (first-attempt Part F). The admin CLI + API endpoint are already in the branch tip at `d6196ed`. No code work.

### Phase B retry — deploy

**Image tag**: `integer-migration-part-e1-d6196ed`.

**Deploy mechanism**: manual SSH-based path identical to first-attempt Phase B, minus the DB wipe. Build on Node 1 (44.200.60.102, the build node), push to ECR as `integer-migration-part-e1-d6196ed`, then SSH-deploy sequentially to Nodes 2 → 3 → 4 → 5 → 1.

**Key delta: do NOT wipe `/data/aethernet/aethernet.db` or `/data/aethernet/blobs`.** Identity files (`node_keys/`, `validator-manifest.json`, `validator-analyzers.json`) preserved as always.

**ECR auth**: same workaround as first-attempt — fetch ECR password on my local workstation (which has AWS CLI creds), pipe through SSH to each target node's `docker login --password-stdin`. Deploy script hardened in first-attempt (`set -euo pipefail` + `docker image inspect` verification before container stop) is reused verbatim.

**Per-node pre-deploy snapshot**: rename artifact directory from first-attempt's `part-f-snapshots` to `part-f-retry-snapshots` so the two sessions are keep-trail separable. Capture `ls -la /data/aethernet/`, container env, container cmd, agent_id, dag_size before stopping each node. These snapshots are "predeploy" (not "prewipe") because no wipe happens in Option A.

**Per-node verification before moving to next**:
- `sudo docker ps` shows container on new image.
- `curl http://localhost:8338/v1/status` returns `peers=4 dag_size=<non-zero, non-decreasing>`.
- Container startup log shows admission router registration line (`recognition: consumer registered consumer=dispatcher_admission_router`).
- Container startup log shows no ERROR-level lines.

**Deploy order**: Node 2 → 3 → 4 → 5 → 1 (Node 1 last to preserve build environment). Same as first-attempt.

**Abort criteria**: any node fails to come up on new image → STOP, diagnose, return to architect.

### Phase B-verify — recovery-driven activation (Sub-scenario A confirmed)

**Mechanism confirmed via code read** (resolved with founder during planning):

`cmd/node/main.go:2089-2091`:
```go
stack.dag.SetOnCommit(func(ev *event.Event, replay bool) {
    recognition.EmitCommit(commitBus, ev, recognition.SourceLocal, replay)
})
```

`internal/dag/dag.go:143`: "The replay parameter is true for events loaded from persistence (addFromStore)."

On each node's startup, the DAG's `addFromStore` path walks the persistent event log and invokes the `SetOnCommit` hook for every historical event with `replay=true`. The hook emits to the recognition bus via `EmitCommit`. Every registered bus consumer receives these replayed commits, gated per-consumer by the Recognition Index's `MarkRecognizedOnce(name, event_id)`.

The E.1-binary admission router (`name="dispatcher_admission_router"`) has ZERO index entries from the first-attempt run — it didn't exist then. So `firstTime=true` for every historical event on E.1 startup, and the router's `Consume` fires on every one of them. Most events the dispatcher filters out via `Interested()` (no registered consumer cares → dispatcher's `snapshotInterestedConsumers` returns empty → `Admit` returns nil early at `dispatcher.go:113-115`). For historical `TaskVerificationConsensus` events, first-attempt's TV consumer already called `dispatcher.Admit(ev)` inline, so those admissions are `StateApplied` in the persisted `AdmissionStore` → dispatcher returns nil at `dispatcher.go:122-124`, no re-settlement.

For the `2a513ac6` activation event specifically: DAG replay → hook → EmitCommit → bus → router → `dispatcher.Admit` → `Interested()` matches `IntegerMigrationActivationConsumer` → Apply runs → `store.GetIntegerMigrationActivated()` returns `activated=false` (store key `integer_migration:activated` was never written first time) → persist activation record → `settler.SetShadowMode(false)` + `genLedger.SetShadowMode(false)` → `slog.Info("integer_migration_activation: activated", ...)`.

**Locked expectation**: on each of the 5 nodes, after startup completes, there will be exactly one `integer_migration_activation: activated` log line referencing `event_id=2a513ac6...`. This is Evidence 1 of the two-evidence structure.

**Per-node checks**:

```
ssh ubuntu@<node-ip> "sudo docker logs aethernet 2>&1 | grep 'integer_migration_activation: activated'"
```

Capture output to `/tmp/part-f-retry-snapshots/phase-b-verify-activation-node{1..5}.txt`.

Also verify on each node:
- Admission router registered: `grep 'consumer=dispatcher_admission_router'` returns a line from startup.
- No ERROR during admission or Apply: `grep -iE 'ERROR.*integer_migration|ERROR.*dispatcher_admission'` returns nothing.
- Startup-load log line `startup: integer migration already activated; running integer-canonical` should NOT appear on the first-boot of the new binary (the store is empty at startup — the flags flip via the replay-driven Apply AFTER startup, not via the startup-load block). This is an important subtle property: first boot of E.1 binary shows *dynamic* activation via replay; subsequent boots (e.g., Phase F's restart test) show *static* activation via startup-load. Both are Evidence 1-adjacent properties.

**Abort criteria**:
- ERROR-level log during consumer Apply or admission → STOP.
- Any node fails to emit the `integer_migration_activation: activated` line within 60s of container start → investigate (possible causes: bus queue full on replay storm, dispatcher Admit erroring silently somewhere). STOP if not diagnosed.
- Cluster inconsistent state: some nodes activated, others not. ALL 5 must activate cleanly.

### Phase C-sanity — confirm integer-canonical math after activation

**Runs only if activation took effect** (Sub-scenario A from B-verify, OR Phase D retry completes successfully flipping flags).

Corpus: **3 tasks** — 2 accept-path small (100,000 µAET) + 1 that could go either way (10,000,000 µAET). Distinct categories: `pf_sanity_1`, `pf_sanity_2`, `pf_sanity_3`. Worker run: restart with `AGENT_KEY_NAME=part-f-retry-worker` and these 3 categories. API key provided out-of-band by operator.

**Per settled task, verify**:
- Zero `shadow_delta` log lines (the wrapper is no-op when `shadowMode=false`).
- Ledger state for this task's recipients is byte-identical across all 5 nodes (compare via `curl http://<node>:8338/v1/agents/<recipient_id>/balance` on each of 5 nodes; all must return the same integer balance).

**Worker handling**:
- Launch via the first-attempt pattern: `(trap '' SIGTSTP SIGTTIN SIGTTOU; source env; cd ~/agent-worker; exec python3 -u worker.py <redirects>)`.
- Stop immediately after all 3 sanity tasks reach terminal status.

**Abort criteria**:
- Any `shadow_delta` log line for a post-activation task → activation didn't take effect on some node, or the flag-read path is broken. STOP.
- Any per-recipient divergence across 5 nodes → integer path is non-deterministic in a way Part D's QEMU corpus didn't surface. STOP.

### Phase D retry — fresh emission (idempotency test on already-activated network)

**Command**:
```
aet admin activate-integer-migration --reason "Part F retry: idempotency verification on already-activated network" --url https://testnet.aethernet.network --json
```

Capture response fields: `event_id`, `emitting_agent`, `emitted_at_unix`, `activation_reason`.

Wait 30–60s for propagation.

**Expected behavior** (Sub-scenario A confirmed at plan time — Phase B-verify will have already flipped flags via the historical event):

- Fresh emit fires the live admission path: CLI → ALB → admin handler → `emitDAGEvent` → DAG commit → recognition bus (live commit, replay=false) → admission router → `dispatcher.Admit`.
- Dispatcher's `Interested()` filter routes it to the activation consumer.
- Consumer's Apply starts. **Early-idempotency check fires**: `c.store.GetIntegerMigrationActivated()` returns `activated=true` (the historical event's Apply persisted the record in Phase B-verify).
- Apply returns nil without re-persisting or re-flipping flags. **No second "activated" log line on any node.**
- The new fresh event exists in the DAG (it's a valid canonical event) but is semantically inert.

**Verification per node**:
- The fresh event's `event_id` appears in the DAG (grep `recognition: commit emitted event_id=<fresh>` or `network: V1 event received event_id=<fresh>`).
- NO additional `integer_migration_activation: activated` log line beyond the one Phase B-verify produced. Each node's count of "activated" lines remains at exactly 1 before and after this phase.
- No ERROR-level logs during the fresh admission path.

**Abort criteria**:
- Fresh emit does not propagate to all 5 nodes within 60s → STOP.
- Admin endpoint returns non-201 → STOP, diagnose.
- Consumer Apply emits ERROR logs → STOP.
- Any node's "activated" log count increases to 2 — idempotency check is broken. STOP.

This is Evidence 2 of the two-evidence structure: live-path admission on an already-activated network short-circuits correctly. Combined with Phase B-verify's Evidence 1, the retry validates both the recovery-driven activation pathway and the fresh-emit idempotency pathway.

### Phase E — post-activation 10-task corpus

10 tasks at 10,000,000 µAET each. Categories `pf_retry_postactivation_1..10`. Worker configured with those categories; otherwise identical to first-attempt Phase C worker setup.

For each of 10 settled tasks, verify:
- Zero `shadow_delta` lines (reconfirming Phase C-sanity at scale).
- Per-recipient amounts byte-identical across all 5 nodes (compare ledger balances per recipient post-settlement).
- Conservation: `sum(per-recipient payouts) + treasury_share == pool`. Sample at least 3 of the 10 tasks with full conservation check.

Worker stops immediately after all 10 reach terminal status. Per the prompt: "no further driving, no additional tasks."

**Abort criteria**:
- Any divergent per-recipient amount across 5 nodes → STOP.
- Any `shadow_delta` for post-activation → STOP.
- Any conservation failure → STOP.

### Phase F — restart test

Restart Node 5 (`32.195.67.127`) only. Pattern:
```
ssh ubuntu@32.195.67.127 "sudo docker stop aethernet && sudo docker start aethernet"
```

Wait 60s for container re-registration + DAG sync.

**Verify**:
- Node 5 startup log contains `startup: integer migration already activated; running integer-canonical` (from `cmd/node/main.go` per Part E wiring).
- Node 5 container is up: `docker ps` shows running.
- Node 5 `peers=4` re-established.
- Post-restart: drive 2 additional tasks via worker (`pf_retry_restart_1`, `pf_retry_restart_2`), verify Node 5's ledger state for those tasks is byte-identical to the other 4 nodes' ledger state.

**Abort criteria**:
- Node 5 doesn't show the startup-load log line → startup meta-store read path is broken. STOP.
- Node 5's post-restart settlements diverge from peers → STOP.

### Phase G — verification report

File: `docs/plans/implementation/part-f-retry-completion-report.md`. Structure per the prompt's §Phase G. Single commit including this plan doc + the completion report. Branch pushed to origin on approval.

---

## 3. v2 §6.4 19-criterion mapping — expected delta vs. first-attempt report

| # | Criterion | First-attempt | Retry target |
|---|---|---|---|
| 1 | `go test -race ./...` clean | PASS | **unchanged PASS** (Part E.1 preserves this) |
| 2 | `go vet ./...` clean | PASS | **unchanged PASS** |
| 3 | `go build ./...` clean | PASS | **unchanged PASS** |
| 4 | Docker image built + pushed | PASS | **new PASS** with `integer-migration-part-e1-d6196ed` tag |
| 5 | All 5 nodes deploy + startup clean | PASS | **new PASS** via Phase B retry |
| 6 | Shadow-mode phase, bounded delta distribution | PASS | **carried forward from first-attempt** (36/36 settlements already validated) |
| 7 | Cutover event propagated to all 5 | PARTIAL | **PASS** via Phase B-verify OR Phase D retry |
| 8 | Post-cutover byte-identical across nodes | BLOCKED | **PASS** via Phase E |
| 9 | Q-weighted validator payouts byte-identical | PASS | **reconfirmed PASS** (via Phase E validator_distribution) |
| 10 | Generation ledger byte-identical | PASS | **reconfirmed PASS** (via Phase E generation_ledger) |
| 11 | Canonical payload float lint | DISCHARGED | **unchanged DISCHARGED** (Part C commit `a79fef7`) |
| 12 | Runtime assertion wrapper | DISCHARGED | **unchanged DISCHARGED** (Part C reflection test) |
| 13 | Cross-run determinism | DISCHARGED | **unchanged DISCHARGED** (Part A unit tests) |
| 14 | Heterogeneous hardware (x86 + ARM) | DISCHARGED | **unchanged DISCHARGED** (Part D QEMU CI) |
| 15 | Historical replay byte-identical | BLOCKED | **PASS** via preserved-DAG retry itself (Option A is the test) |
| 16 | Zero-Q fallback even-split | DISCHARGED | **unchanged DISCHARGED** (Part B unit test) |
| 17 | Negative-Q invariant rejection | DISCHARGED | **unchanged DISCHARGED** (Parts A+B unit tests) |
| 18 | Conservation: sum_payouts + treasury = budget | PASS | **reconfirmed PASS** (via Phase E sample-3 conservation check) |
| 19 | No regression on F3-B §10 convergence/restart/replay | PASS | **reconfirmed PASS** (Phase B retry preserves DAG; Phase F tests restart) |

Expected retry outcome: **19/19 PASS or DISCHARGED** (3 previously PARTIAL/BLOCKED/BLOCKED now PASS; 12 unchanged).

---

## 4. Artifacts path / bookkeeping

New directory for retry artifacts: `/tmp/part-f-retry-snapshots/` (separate from first-attempt's `/tmp/part-f-snapshots/`).

- `node{1..5}-predeploy-*.txt` — per-node pre-deploy snapshots.
- `node{1..5}-deploy.log` — per-node deploy transcript.
- `phase-b-verify-activation-node{1..5}.txt` — per-node activation log check.
- `phase-c-sanity/` — the 3-task sanity corpus outputs.
- `phase-d-activation.json` — the fresh-emit response.
- `phase-e/` — the 10-task post-activation corpus outputs.
- `phase-f-restart-node5.log` — restart transcript + verification.
- `worker.log` — agent-worker output for all phases.
- `shadow-logs/` — expected to be EMPTY (no shadow_delta post-activation); presence of files here is an abort signal.

---

## 5. Design resolution — Sub-scenario A confirmed

The planning-time question of whether historical DAG events would replay through the new admission router was resolved by reading `cmd/node/main.go:2089-2091` and `internal/dag/dag.go:143`:

```go
stack.dag.SetOnCommit(func(ev *event.Event, replay bool) {
    recognition.EmitCommit(commitBus, ev, recognition.SourceLocal, replay)
})
```

> "The replay parameter is true for events loaded from persistence (addFromStore)."

The DAG's `addFromStore` path fires the post-commit hook for every historical event on startup. The hook emits to the recognition bus. The admission router (a new bus consumer on E.1) has no prior index entries, so `MarkRecognizedOnce` returns `firstTime=true` for every event. The router's `Consume` fires on every historical commit.

**The `2a513ac6` activation event will be admitted to the dispatcher during startup replay** → consumer's Apply runs → activation state persists → flags flip.

This is Sub-scenario A, and it is a real property that Phase B-verify will test directly. The two-evidence structure in the prompt is achievable as originally framed:

- **Evidence 1** (Phase B-verify): recovery-driven activation from the historical inert event.
- **Evidence 2** (Phase D retry): fresh-emit on already-activated network short-circuits via the consumer's idempotency guard.

No open choice points. Proceed to execution on approval.

---

## 6. Completion criteria (unchanged from prompt)

1. Phases B, B-verify, C-sanity, D, E, F execute without triggering abort criteria.
2. Verification report at `docs/plans/implementation/part-f-retry-completion-report.md`.
3. Single commit on `feat/canonical-distribution-integer-migration` (this plan doc + the report).
4. All original Part F v2 §6.4 criteria that were PARTIAL/FAIL in first-attempt now PASS.
5. Explicit "merge" recommendation.

If any phase aborts: halt, document findings in the completion report, return to architect. No retry-mode "try again" without architect approval.

---

## 7. Estimated time

- Phase B retry (build + push + 5-node deploy): ~20 min active (~4 min per node × 5 + build time).
- Phase B-verify: ~5 min (parallel log-grep across 5 nodes).
- Phase C-sanity (3 tasks): worker startup ~30s + drain ~5 min.
- Phase D retry (fresh emit): ~3 min (emit + 60s propagation + per-node log checks).
- Phase E (10 tasks): worker startup + drain ~15 min at ~1.5 min per task with real Claude calls.
- Phase F restart (Node 5 bounce + 2 verification tasks): ~5 min.
- Phase G report writing: ~45 min.
- Commit + push: ~3 min.

**Total**: ~1.5–2 hours wall clock if everything goes smoothly. Roughly 1 API-key-dependent phase (Phase C-sanity + Phase E + Phase F both use the worker).

---

## 8. What NOT to do (reinforcement of prompt §What NOT to do)

- No code changes on the branch other than this plan doc + the retry completion report.
- No node DB wipe.
- No multiple activation emits beyond the one in Phase D.
- No indefinite observation-window extension — abort-criteria timings apply.
- No retry-on-abort without architect approval.
- No assuming first-attempt's 36/36 shadow-corpus determinism transfers to post-activation without the Phase C-sanity check.

---

## 9. Approval gate

Plan approved as-is + founder confirms Sub-scenario B interpretation → execute as planned.

Plan kicked back → revise on the specific points raised.

---

**End of Part F retry plan v1.**
