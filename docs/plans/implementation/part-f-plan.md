# Part F — Testnet cutover rehearsal + §10-style verification — implementation plan

**Branch**: `feat/canonical-distribution-integer-migration`
**Base commit**: `7d29ab5` (Part E — completion report)
**Plan reference**: `docs/plans/2026-04-20-canonical-distribution-integer-migration-v2.md` §4.5 (migration strategy), §6.4 (19 live-testnet criteria)
**Scope boundary**: new CLI subcommand + its supporting API endpoint + verification report. No protocol-level decisions. No modification to settlement, dispatch, event, protocolmath, or recognition code. **Fix the rehearsal, not the protocol.**

---

## 0. What this part delivers

A dry-run of the integer-migration cutover on the live 5-node testnet, end-to-end:

1. Deploy the Part-E binary (migration consumer registered, shadow mode still ON).
2. Drive a **shadow-observation corpus** of real settlements and collect `shadow_delta` log lines from all 5 nodes.
3. Use a new **`aet admin activate-integer-migration`** CLI subcommand to emit the canonical `EventTypeIntegerMigrationActivation` event. The 5 nodes' migration consumers pick it up, persist the state, flip shadow-mode flags.
4. Drive a **post-activation corpus** and verify the integer path is byte-identical across the 5 nodes.
5. Restart one node; verify startup-load restores integer-canonical mode.
6. Produce a §10-style verification report with PASS/FAIL for each of the 19 v2 criteria (§6.4) with inline evidence.

Deliverables committed to the branch at the end of Part F:

- **commit-f1**: `cmd/aet/admin.go` (new) — `aet admin activate-integer-migration` subcommand.
- **commit-f2**: `internal/api/server.go` (additive) — `POST /v1/admin/integer-migration/activate` handler + route registration.
- **commit-f3**: `docs/plans/implementation/part-f-completion-report.md` — §10-style verification report with evidence.

No other code touched.

---

## 1. CLI subcommand design

### 1.1 Signature

```
aet admin activate-integer-migration --reason "<observation-window-complete>" [--url URL] [--json]
```

- `--reason` required, free-form string copied verbatim into `ActivationReason`.
- `--url` inherits `defaultURL()` (`testnet.aethernet.network` fallback via `newFlags`).
- `--json` machine-readable output for the verification report.
- No `--agent`: always uses the active wallet (same pattern as `aet faucet`, `aet stake`).

### 1.2 Placement

New file `cmd/aet/admin.go`. Dispatcher entry added in `cmd/aet/main.go`:

```go
case "admin":
    if len(args) == 0 {
        fatal("usage: aet admin <activate-integer-migration>")
    }
    switch args[0] {
    case "activate-integer-migration":
        runAdminActivateIntegerMigration(args[1:])
    default:
        fatal("unknown admin subcommand: %s", args[0])
    }
```

Matches the existing `wallet` / `task` subcommand-group pattern (see `cmd/aet/main.go:67-112`). Help text added to `printUsage()` under a new `Admin:` header.

### 1.3 Signing mechanism

Uses existing `signedPost` (`cmd/aet/client.go:14`) + `unlockWallet` (`cmd/aet/wallet.go`) verbatim. The signer of the HTTP request is the operator's wallet. The node-side handler copies the signer's `AgentID` into `EmittingAgent`. This gives operator-level attribution without introducing a new signing envelope.

```go
func runAdminActivateIntegerMigration(args []string) {
    fs, url, jsonOut, _ := newFlags("admin activate-integer-migration")
    reason := fs.String("reason", "", "Activation reason (required)")
    _ = fs.Parse(args)
    if *reason == "" { fatal("--reason is required") }

    wf, err := getActiveWallet()
    if err != nil { fatal("no active wallet") }
    pk, err := unlockWallet(wf)
    if err != nil { fatal("unlock wallet: %v", err) }

    var result map[string]any
    if err := signedPost(*url, "/v1/admin/integer-migration/activate",
        map[string]any{"reason": *reason}, wf.AgentID, pk, &result); err != nil {
        fatal("activate: %v", err)
    }

    if *jsonOut { printJSON(result); return }
    evID, _ := result["event_id"].(string)
    ts, _ := result["emitted_at_unix"].(float64)
    emitter, _ := result["emitting_agent"].(string)
    printHeader("Integer Migration Activation")
    printRow("Event ID", evID)
    printRow("Emitting Agent", truncateID(emitter, 24))
    printRow("Emitted (unix)", strconv.FormatInt(int64(ts), 10))
    fmt.Println("\n  Activation event submitted to DAG. Settles through consensus.")
}
```

No `math`, no float coercion — the emitted timestamp is `int64` all the way through.

### 1.4 Response shape

```json
{
  "event_id": "<sha256 of canonical event>",
  "emitting_agent": "<hex-encoded operator public key>",
  "emitted_at_unix": 1729123456,
  "activation_reason": "observation-window-complete"
}
```

---

## 2. API endpoint design

### 2.1 Handler location

Added to `internal/api/server.go`, adjacent to existing handlers. Route registration in whichever `ServeMux` / router block the server wires (match existing pattern).

**Path**: `POST /v1/admin/integer-migration/activate`
**Auth**: existing signed-request middleware (the same envelope `handleFaucet`, `handleStake` use). The authenticated `agentID` becomes `EmittingAgent`.

### 2.2 Request schema

```go
type activateIntegerMigrationRequest struct {
    Reason string `json:"reason"`
}
```

Minimal. The `Version` field is pinned to `1` server-side (not accepted from the client). `EmittedAtUnix` is set from `time.Now().Unix()` server-side.

### 2.3 Handler sketch

```go
func (s *Server) handleAdminActivateIntegerMigration(w http.ResponseWriter, r *http.Request) {
    // Auth middleware has already populated the authenticated agent ID.
    signerAgent := /* extracted from existing auth context */

    var req activateIntegerMigrationRequest
    if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
        writeError(w, http.StatusBadRequest, "invalid request body")
        return
    }
    if strings.TrimSpace(req.Reason) == "" {
        writeError(w, http.StatusBadRequest, "reason is required")
        return
    }

    emittedAt := time.Now().Unix()
    payload := event.IntegerMigrationActivationPayload{
        Version:          1,
        EmittingAgent:    signerAgent,
        ActivationReason: req.Reason,
        EmittedAtUnix:    emittedAt,
    }

    // Activation has no semantic parent (root-like; self-contained per Part E
    // consumer's Prerequisites = nil).
    evID := s.emitDAGEvent(event.EventTypeIntegerMigrationActivation, payload, signerAgent)
    if evID == "" {
        writeError(w, http.StatusInternalServerError, "emit failed")
        return
    }

    writeJSON(w, http.StatusCreated, map[string]any{
        "event_id":          evID,
        "emitting_agent":    signerAgent,
        "emitted_at_unix":   emittedAt,
        "activation_reason": req.Reason,
    })
}
```

### 2.4 Authorization model

**Scope of Part F**: any wallet that successfully signs the request can activate. This matches the rehearsal context — on testnet the founder holds the only operator wallet, and activation is idempotent, one-way, and audit-visible (it emits a canonical DAG event that every node logs at Info). Production hardening (operator allowlist, multi-sig, DAO gate) is explicitly out of scope for Part F and belongs to a later workstream.

This is called out in the commit message and in the completion report's "Deviations / future hardening" section so a future reviewer does not mistake the permissive testnet gate for the production model.

---

## 3. Shadow-observation corpus

### 3.1 Corpus shape

**20 settlements**, driven by real autonomous workers against the testnet. We cannot directly stage Q-distributions because the settler receives Q from the reputation fabric, not from a testnet fixture. Instead we drive *task volume* and let the natural Q variance across the 5 validators exercise the shadow path:

- **20 tasks** posted via `aet task post`, each with a different `category` to exercise category-scoped Q lookups.
- Budgets varied: 10 tasks at **500 µAET** (small pool, sensitive to rounding), 10 tasks at **50,000 µAET** (medium pool, typical rounding magnitude).
- Recipient counts: determined by live validator set (5 active validators → each settlement samples 3–5 agreeing validators depending on who participated in the round).
- All tasks drive a full pipeline: `TaskPosted → TaskClaimed → TaskSubmitted → VerificationVote × N → TaskVerificationConsensus → Settlement`.

Each settlement triggers one `shadow_delta` line for validator distribution and (if the generation ledger fires for the task) one for generation ledger distribution. At 20 tasks with 5-node observation, we expect **~100–200 total `shadow_delta` lines** across all node logs.

### 3.2 Corpus driver

A shell script or Python harness (no new Go CLI — this is rehearsal-layer tooling):

```bash
#!/bin/bash
# scripts/part-f-shadow-corpus.sh
set -e
for i in $(seq 1 10); do
    aet task post --title "shadow-small-$i" --category "shadow_small_$i" --budget 0.0005 \
        --description "Part F shadow corpus small pool" --json >> /tmp/part-f-tasks.json
done
for i in $(seq 1 10); do
    aet task post --title "shadow-med-$i" --category "shadow_med_$i" --budget 0.05 \
        --description "Part F shadow corpus medium pool" --json >> /tmp/part-f-tasks.json
done
```

Not committed to the repo — reproduced inline in the completion report so the commands can be replayed but the protocol code is untouched.

### 3.3 Observation window

Allow **10 minutes** after the last task post for the autonomous worker pipeline to drain. Observed via `aet task list --status completed` ≥ 20 tasks before proceeding.

---

## 4. Log collection

### 4.1 Source

The 5 ECS tasks write logs to CloudWatch log group `/ecs/aethernet` (confirmed by `scripts/check-testnet.sh:135`). Each container stream has its own log-stream name.

### 4.2 Extraction commands

For each of the 5 nodes (keyed by log stream):

```bash
aws logs filter-log-events \
    --log-group-name /ecs/aethernet \
    --log-stream-names "<stream-name>" \
    --filter-pattern "shadow_delta" \
    --start-time $(($(date +%s -d '30 minutes ago') * 1000)) \
    --region us-east-1 \
    --output json > /tmp/part-f-shadow-<node-idx>.json
```

One JSON file per node. The 30-minute window is generous — shadow corpus + observation lasts ~10 minutes. Retention on `/ecs/aethernet` is default CloudWatch retention (≥ 1 day) so re-pulls during report-writing are safe.

### 4.3 Parse step

A small inline Python snippet in the completion report (not committed code):

```python
import json, glob, collections
per_task = collections.defaultdict(dict)  # task_id -> { node_idx -> line }
for f in sorted(glob.glob("/tmp/part-f-shadow-*.json")):
    node_idx = f.split("-")[-1].split(".")[0]
    for ev in json.load(open(f))["events"]:
        msg = ev["message"]
        kv = dict(tok.split("=", 1) for tok in msg.split() if "=" in tok)
        if "task_id" in kv:
            per_task[kv["task_id"]][node_idx] = kv
# Equivalence check: for each task, int_sum identical across nodes.
for task_id, nodes in per_task.items():
    int_sums = {idx: kv["int_sum"] for idx, kv in nodes.items()}
    if len(set(int_sums.values())) != 1:
        print(f"DIVERGENT: {task_id} int_sum per node = {int_sums}")
```

---

## 5. Cross-node integer-path equivalence determination

### 5.1 What equivalence means here

The `shadow_delta` log line (format from `internal/settlement/verification_consensus_settler.go:595-604`):

```
shadow_delta context=validator_distribution task_id=T recipient_count=N
  float_sum=F int_sum=I sum_delta=D max_per_recipient_delta=M pool=P
```

**Primary equivalence test (cross-node):** for each `task_id` observed by ≥ 2 nodes, `int_sum` MUST be identical across all observers. This is the integer path's determinism claim. A mismatch is a critical failure and triggers Phase-C abort.

**Secondary signal (per-node):** `sum_delta == 0` on every line (conservation of float and integer paths within one node). A nonzero `sum_delta` is a conservation bug. Elevates to WARN in the log automatically (§Part-B §4.5 format).

**Tertiary signal (latent-non-determinism hypothesis):** for each `task_id` observed by ≥ 2 nodes, compare `max_per_recipient_delta` across nodes. Part B §"Prominent — latent float-path remainder-absorption non-determinism" predicts these may differ because the float path absorbs remainder at caller-slice-last and the slice-order varies by receive-order across nodes. If observed, it is evidence the bug was live in production and Part E's cutover fixes it. Logged in the report but does NOT abort (it is informational; the integer path is what becomes canonical post-cutover).

### 5.2 Decision matrix

| Observation | Classification | Action |
|---|---|---|
| All `int_sum` identical across nodes for every task | PASS integer determinism | Proceed to Phase D |
| Any `int_sum` differs across nodes for one task | FAIL | Abort; investigate |
| Any `sum_delta != 0` on any node | FAIL (conservation) | Abort; investigate |
| `max_per_recipient_delta` identical across nodes | Non-determinism was latent (float path converged by luck) | Log in report; proceed |
| `max_per_recipient_delta` differs across nodes | **Confirms the Part B hypothesis**: float-path latent non-determinism was live | Log prominently in report; cutover fixes it; proceed |

---

## 6. Phase sequence

### Phase A — Code commit (local, no testnet touch)

1. Write `cmd/aet/admin.go` with `runAdminActivateIntegerMigration`.
2. Wire dispatcher in `cmd/aet/main.go`; update `printUsage()`.
3. Write `POST /v1/admin/integer-migration/activate` handler in `internal/api/server.go`; register route.
4. Local unit tests:
   - `cmd/aet/admin_test.go` — table-driven argument parsing test (flag validation, missing `--reason`).
   - `internal/api/admin_integer_migration_test.go` — handler test using an in-process server + stub publisher, asserting: missing reason → 400; valid request → 201 + event_id + correct payload fields.
5. Build: `go build ./...` clean.
6. Vet: `go vet ./...` clean.
7. Race: `go test -race -count=1 ./cmd/aet/... ./internal/api/...` clean.
8. Full-repo regression: `go test -race -count=1 ./...` (expect 2 pre-existing flakes in `internal/canary`, `internal/network` documented in Part B report).
9. Commit `commit-f1` (CLI) and `commit-f2` (API) separately for clean bisect. Both compile and pass tests independently.

**Abort gate A:** any of steps 5–8 fail → stop, diagnose, do not proceed to testnet.

### Phase B — Deploy

1. Push branch to origin.
2. SSH to build node (44.200.60.102): `git fetch origin && git reset --hard origin/feat/canonical-distribution-integer-migration`.
3. Verify HEAD matches expected commit hash (`git log --oneline -1`).
4. Run `scripts/deploy-testnet.sh` — builds Docker image, pushes to ECR, force-deploys all 3 ECS services, waits 90s for rollout.
5. Verify via `scripts/check-testnet.sh`: all 5 nodes up, peers = 4 on each, DAG size advancing.
6. Sanity: `aet status --url https://testnet.aethernet.network` returns current version.

**Abort gate B:** any node fails to come up healthy → stop, diagnose, do not proceed to shadow phase.

### Phase C — Shadow observation

1. Fresh operator wallet: `aet wallet create --name part-f-operator`.
2. Faucet: `aet faucet` (grant settles in ~15s).
3. Drive shadow corpus (§3.2 script): post 20 tasks, wait for completion.
4. Pull `shadow_delta` logs from all 5 nodes (§4.2).
5. Parse and compute: (a) per-task `int_sum` equivalence across nodes, (b) per-node `sum_delta == 0` check, (c) per-task `max_per_recipient_delta` cross-node distribution.
6. Snapshot current DAG state, ledger balances, treasury.

**Abort gate C:** `int_sum` divergent across nodes for any task OR `sum_delta != 0` anywhere → **STOP. Do not emit activation event.** Return findings to founder. This is the most important abort gate — activation is one-way, and activating on a divergent-int-path network would corrupt the ledger the moment the flags flip.

### Phase D — Emit activation (founder-gated)

1. **Founder approval required here.** Present Phase C evidence to founder; founder explicitly confirms "proceed to activation."
2. Execute: `aet admin activate-integer-migration --reason "part-f-rehearsal-<date>-<phase-c-observation-digest>" --json`.
3. Record emitted `event_id`, `emitted_at_unix`, `emitting_agent` from response.
4. Wait 30s for consensus propagation.
5. Verify on all 5 nodes:
   - Log line `integer_migration_activation: activated event_id=<ID>` at INFO (from `internal/dispatch/integer_migration_activation_consumer.go:119`).
   - `aet status` shows the activation event in recent events.
6. Verify consumer idempotency: re-invoke activation event via a second call (the consumer's early-idempotency pre-check should no-op it). Expected: second call returns a different event ID (it is a different event) but the consumer's Apply logs no "activated" line on the duplicate — the store already has a record, so `GetIntegerMigrationActivated() returns activated=true` short-circuits before persist/flip.

**Abort gate D:** activation event not observed on all 5 nodes within 60s → stop; investigate consensus path. (Note: if some nodes flip and others do not, the cluster is in a divergent consensus state — this would be a Part-E consumer bug, not a Part-F rehearsal issue. Part F would pause and return to architect session for diagnosis.)

### Phase E — Post-activation verification

1. Drive a **10-task post-activation corpus**: smaller than shadow corpus; the determinism claim has already been pre-validated by Phase C's integer path, so this phase is checking that the runtime cutover happened cleanly.
2. All 10 tasks: budget 10,000 µAET (medium pool); 10 distinct categories.
3. For each settled task, verify via per-node API:
   - Sum of per-recipient ledger deltas equals pool exactly (conservation).
   - Per-recipient amount identical across all 5 nodes (determinism).
4. Log check: NO `shadow_delta` lines should appear for post-activation settlements (the shadow-wrapper should take the non-shadow branch and return integer-result directly — §Part B commit 3).

**Abort gate E:** any post-activation settlement produces divergent per-recipient amounts → the Part E cutover is buggy. Stop; restart investigation.

### Phase F — Restart test

1. Pick Node 5 (least-central; safest to bounce).
2. `aws ecs update-service --cluster aethernet-testnet --service aethernet-node3 --force-new-deployment ...` (or SSH-level `docker restart`).
3. Wait 60s for Node 5 to rejoin.
4. Verify Node 5 startup log shows: `startup: integer migration already activated; running integer-canonical` (from `cmd/node/main.go:1990-1993`).
5. Post 2 more tasks; verify Node 5 settles them integer-canonically (no `shadow_delta` line; per-recipient amounts match Nodes 1–4).

**Abort gate F:** Node 5 comes up in shadow mode post-restart → the startup-load block is broken. Return to Part E debugging.

### Phase G — Write report

See §7 below. No further testnet action.

---

## 7. Verification report structure

### 7.1 File location

`docs/plans/implementation/part-f-completion-report.md`.

### 7.2 Top-level sections (mirrors F3-B §10 report)

1. **Header block** — branch, base commit, plan reference, Part-F commit hashes.
2. **Summary table** — 19 criteria from v2 plan §6.4, one row per criterion with:
   | # | Criterion | Status | Evidence |
3. **Per-phase narrative** — Phase A through Phase F, each with:
   - What was done (commands executed).
   - What was observed (log excerpts, API responses, timestamps).
   - Classification (PASS / FAIL / N-A / latent-finding).
4. **Shadow-delta analysis** — table of tasks × nodes showing `int_sum`, `sum_delta`, `max_per_recipient_delta`. Called out explicitly whether Part B's latent-non-determinism hypothesis was observed.
5. **Activation event details** — full event JSON, DAG event ID, per-node consumer-applied timestamps.
6. **Post-activation determinism table** — 10 tasks × 5 nodes × per-recipient amount matrix. Any cell that differs is called out.
7. **Restart test evidence** — Node 5 startup log excerpt showing integer-canonical mode.
8. **Deviations** — any divergence from this plan (e.g., corpus shape changed, task count adjusted).
9. **Discoveries for Part G** — unknowns / surprises surfaced during rehearsal. Feeds lessons.md in Part G.
10. **Future-hardening callouts** — production activation authorization model (currently permissive on testnet), operator-allowlist design sketch.

### 7.3 Pass threshold

**19 of 19 PASS.** One N-A is acceptable only if a specific v2 criterion (e.g., criterion 14 — heterogeneous hardware; already covered by Part D's QEMU CI job) is documented as previously discharged with reference to the discharging artifact.

FAIL on any criterion → report documents the failure, no Part-G merge, return to architect session.

### 7.4 19-criterion mapping (v2 §6.4 → Part F phase)

| # | v2 §6.4 criterion | Discharged by |
|---|---|---|
| 1 | `go test -race ./...` clean | Phase A step 8 |
| 2 | `go vet ./...` clean | Phase A step 6 |
| 3 | `go build ./...` clean | Phase A step 5 |
| 4 | Docker image built + pushed | Phase B step 4 |
| 5 | All 5 nodes deploy + startup clean | Phase B step 5 |
| 6 | Shadow-mode phase, delta bound | Phase C steps 3–5 |
| 7 | Cutover event propagated to all 5 | Phase D step 5 |
| 8 | Post-cutover byte-identical across nodes | Phase E step 3 |
| 9 | Q-weighted validator payouts byte-identical | Phase E (subset of step 3) |
| 10 | Generation ledger byte-identical | Phase E (subset of step 3, for tasks that trigger gen-ledger) |
| 11 | Canonical payload float lint passes | Part C (already discharged) — reference |
| 12 | Runtime assertion wrapper | Part C (already discharged — reflection test) — reference |
| 13 | Cross-run determinism | Part A (unit tests) — reference |
| 14 | Heterogeneous hardware (x86 + ARM) | Part D (QEMU CI) — reference |
| 15 | Historical replay byte-identical | Phase E extension — post-activation node replays shadow-phase tasks and verifies the ledger state it replays is the one that was observed at Phase C |
| 16 | Zero-Q fallback even-split | Part B unit test — reference + call out that Phase C's real corpus may not have exercised this (not feasible to stage zero-Q on live testnet without a new event type) |
| 17 | Negative-Q invariant rejection | Part B unit test + protocolmath — reference (live negative-Q is similarly not stageable) |
| 18 | Conservation: sum_payouts + treasury = budget | Phase E step 3(a) |
| 19 | No regression on F3-B §10 convergence / restart / replay | Phase E (settlement convergence) + Phase F (restart) |

Criteria 11, 12, 13, 14, 16, 17 reference prior discharges explicitly with commit hashes. This is the intended design — v2 §6.4 is the full verification set; Part F is the testnet-visible subset. Cross-referencing is the correct treatment, not a "skip."

---

## 8. Choice points for founder

Flag these so approval is explicit rather than inferred:

1. **Corpus size**: 20 tasks shadow + 10 tasks post-activation. Trade-off: larger corpus gives tighter bound on latent-non-determinism observation; smaller is faster. **Recommendation**: 20 + 10 as proposed. Runs in ~30 minutes wall clock.

2. **Permissive authorization for testnet activation endpoint**. Any valid wallet can activate. **Recommendation**: accept for Part F (testnet, rehearsal, one-way but auditable). Flag as future-hardening in the completion report. Alternative: hard-code an operator-allowlist in the handler keyed to a specific agent ID. Would require separate key provisioning for the rehearsal.

3. **Shadow-delta failure treatment for `max_per_recipient_delta`**. If Phase C shows per-recipient deltas differ across nodes (Part B's latent-non-determinism hypothesis confirmed), the integer path fixes it at cutover — but the observation itself is evidence of a live production bug. **Recommendation**: log prominently in report, do NOT abort. Alternative: treat as abort and escalate.

4. **Restart test target node**: Node 5 (least-central, safest). Alternative: rotate through all 5 nodes to exercise startup-load on each. **Recommendation**: Node 5 only — startup-load is deterministic and identical across nodes (same binary, same meta key). One node suffices.

5. **Commit sequencing**: split CLI + API into `commit-f1` and `commit-f2`. Alternative: one commit. **Recommendation**: split for clean bisect (mirrors Part B's 4-commit discipline).

---

## 9. What NOT to do (forward guardrails)

- **Do not modify any file under `internal/settlement`, `internal/dispatch`, `internal/protocolmath`, `internal/event`, `internal/recognition`.** The rehearsal verifies Parts A–E; it does not patch them.
- **Do not push the branch with a broken test.** Phase A's full-repo regression must be clean (modulo pre-existing flakes) before Phase B.
- **Do not emit the activation event in Phase C.** Shadow-observation is strictly pre-activation; if it triggers activation early the delta evidence is contaminated.
- **Do not skip Phase C abort-gate logic for time pressure.** Activation is one-way. A divergent int_sum at Phase C means activation will cause ledger divergence at Phase D.
- **Do not wipe testnet state to "start fresh."** The testnet has continuous identity (genesis validator set); wiping `aethernet.db` is Phase B standard, wiping `node_keys/` or `validator-manifest.json` is forbidden.
- **Do not interpret a successful rehearsal as approval to activate on mainnet.** Part F produces evidence that the cutover mechanism works on testnet. Mainnet is a separate operational decision with its own approval gate outside this workstream's scope.

---

## 10. Estimated time + resource cost

- Phase A (local code + tests + commits): ~60 minutes.
- Phase B (deploy): ~5 minutes active, 90s rollout.
- Phase C (shadow corpus + observation): ~20 minutes active driving, 10 minutes observation drain.
- Phase D (activation + verification): ~10 minutes.
- Phase E (post-activation corpus): ~15 minutes active + 10 minutes settlement drain.
- Phase F (restart + verify): ~10 minutes.
- Phase G (report writing): ~60–90 minutes.

**Total**: ~3–4 hours wall clock. Single founder-interaction gate at Phase D (~2 hours into the session).

AWS cost: negligible — one ECS redeploy, CloudWatch log queries, normal testnet traffic.

---

## 11. Approval gate

This plan is **v1 of Part F implementation**. Founder decision required: approve as written, approve with adjustments on the §8 choice points, or kick back with specific concerns.

On approval:

- Execute Phase A, then present commit-f1 and commit-f2 commit SHAs for review before Phase B.
- Execute Phases B–C; present Phase C shadow-delta evidence for Phase D approval gate.
- Execute Phases D–F.
- Produce Phase G report as commit-f3; push branch.
- Part G (lessons + workstream queue + merge) follows in a separate session.

---

**End of Part F plan v1.**
