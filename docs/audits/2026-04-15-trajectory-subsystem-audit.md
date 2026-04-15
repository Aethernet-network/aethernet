# Trajectory Subsystem Audit — 2026-04-15

**Type**: Read-only audit. No code changes, no config changes, no testnet mutations beyond one `curl` probe of the public ALB. The only artifact produced is this report.
**Parent context**: `docs/plans/2026-04-12-reputation-and-consensus-integrity.md` §2.1 (the `ReputationEvidence` record) captures verdict-level data only, with no trajectory references. The founder reminded the architect that trajectory capture is load-bearing for the thesis; this audit determines whether the existing `internal/trajectory/` subsystem covers the gap, partially covers it, or leaves the thesis-critical dimensions essentially unbuilt.
**Trigger**: founder directive ahead of the reputation-workstream Step 4 (evidence-store implementation). Step 4 is currently paused pending the F3-B settlement-divergence fix; this audit determines whether it should also be paused on a dedicated trajectory workstream.

---

## Executive Summary

**`internal/trajectory/` is built and live — but only for the dimensions of the thesis that don't carry the load.** The package (7 files, ~1,320 lines) defines a `CheckpointBody` data model with path-style fields (`ApproachDescription`, `Parameters`, `EvidenceSnippet`, `ErrorDetail`, `IntermediateOutputHash`), an `Outcome` enum that explicitly names abandonment (`exploring / dead_end / pivot / converged`), parent-linked causal chaining via `ParentCommitID`, content-addressing via SHA-256 of canonically-serialized bodies, and two HTTP endpoints (`POST /v1/tasks/{id}/trajectory/commit` and `GET /v1/tasks/trajectories/{id}`) that are both wired into the production API server, both exposed by both Go and Python SDKs, and both **live and responsive on the testnet right now**. Agents can emit trajectory checkpoints during execution; observers can query the per-task commit tree. That half works. **The other half — the thesis-critical half — is not wired.** Checkpoint blobs do not propagate across nodes (the BlobSync extractor for `EventTypeTrajectoryCommit` is commented out at `internal/blobsync/extractors.go:17–18`), validators are blind to trajectory data in every analyzer family, the `TaskVerificationVote` / `TaskVerificationConsensus` / `TaskSettlement` payloads carry no trajectory reference, no recognition-fabric consumer is interested in trajectory events, and the `Evidence.ExplorationRoot` / `Evidence.ExplorationSample` fields that would anchor a per-task trajectory root into submission payloads **exist in the schema but are never populated by any production code path** — the Merkle functions that would compute them (`ComputeExplorationRoot`, `SampleExplorationCommits`) are test-only dead code. **Q5 verdict: (b) partially covered, with the specific gap that every thesis-critical dimension (structural independence, on-chain cross-node dataset, consensus bonding) is missing.** Q6 sequencing recommendation: F3-B proceeds immediately; reputation Step 4 can resume after F3-B with an `ExplorationRoot` reference field added to `ReputationEvidence` and the current state documented as a known gap; a dedicated trajectory-integration workstream should open between Step 4 and the challenge-path workstream to wire propagation, validator visibility, and evidence linkage before any workstream that depends on trajectory being load-bearing (challenge path, data ingestion) is opened.

---

## Q1 — What `internal/trajectory/` actually contains

### File inventory

| File | Lines | Primary types / functions | Persistence | Production callers (non-test) | Test coverage |
|---|---:|---|---|---|---|
| `body.go` | 100 | `CheckpointBody` struct (`body.go:19–38`), `CanonicalCheckpointBytes()` (`:57–88`), `ComputeCheckpointHash()` (`:93–99`), internal `checkpointCanonical` wire struct (`:40–48`) | No direct persistence; body bytes written to blobstore by `service.go:200` | `service.go:192` (canonical), `service.go:200` (hash) | `body_test.go` (106 lines) — canonical determinism, parameter-order independence, hash stability, hash divergence |
| `merkle.go` | 85 | `ComputeExplorationRoot(eventIDs) (string, error)` (`:30`), `SampleExplorationCommits(eventIDs, max) []string` (`:72`), `MaxExplorationSample = 10` (`:17`) | None | **NONE (dead code)** — only referenced from `merkle_test.go` and `invariants_test.go:380,388` | `merkle_test.go` (133 lines) — determinism, order-independence, bounded sampling |
| `service.go` | 389 | `Service` struct (`:72–86`), `NewService()` (`:94`), `EmitCommit()` (`:141`), `GetTrajectories()` (`:320`), `SetPublisher()` (`:303`), `TaskMgr()` (`:306`), `CommitCount()` (`:309`), `CommitRequest` / `CommitResponse` | BadgerDB DAG (via `dag.Add` / `publisher.Publish`) + Blobstore (via `s.blob.Put`) | `NewService` at `cmd/node/main.go:2243`; `SetPublisher` at `cmd/node/main.go:2247`; `EmitCommit` at `internal/api/trajectory_handler.go:65`; `GetTrajectories` at `internal/api/trajectory_handler.go:124` | Covered indirectly via `invariants_test.go` (437 lines) and `trajectory_handler_test.go` (separate file under `internal/api/`) |
| `view.go` | 70 | `CommitNode` (`:13–26`), `CommitWithBody` (`:29–36`), `TreeResponse` (`:39–43`), `CommitNodeFromEvent()` (`:48`), `MaxTrajectoryLimit = 500` (`:10`) | Stateless view layer | `service.go:333` (retrieval path), `internal/api/trajectory_handler.go:114` | Covered via handler + invariant tests |
| `body_test.go` | 106 | — | — | — | Covers `body.go` |
| `merkle_test.go` | 133 | — | — | — | Covers `merkle.go` (only consumer of those functions) |
| `invariants_test.go` | 437 | — | — | — | 11 architectural invariants: event-model compliance, canonical ID participation, DAG append-only, Merkle determinism, retrieval-order stability, etc. |

### `doc.go` absent

The package has no top-level documentation file. Intent and scope must be inferred from code and from `internal/event/trajectory.go` (which defines `EventTypeTrajectoryCommit` and the event payload). A trajectory concept reference for newcomers would sit in `docs/` or the package doc; neither exists today.

### Production-caller check (writer-without-caller pattern from the projection-registry workstream)

Applying the step-3 lint's heuristic to trajectory:

- `Service.EmitCommit` (service.go:141) → production caller `internal/api/trajectory_handler.go:65` ✓
- `Service.GetTrajectories` (service.go:320) → production caller `internal/api/trajectory_handler.go:124` ✓
- `Service.SetPublisher` (service.go:303) → production caller `cmd/node/main.go:2247` ✓
- `Service.TaskMgr`, `Service.CommitCount` → only test callers (out-of-band accessors) ✓ (acceptable — observability methods)
- **`ComputeExplorationRoot` (merkle.go:30) → NO production callers.** Only `merkle_test.go` and `invariants_test.go:380, 388` reference it. **Dead code from the protocol's perspective.**
- **`SampleExplorationCommits` (merkle.go:72) → NO production callers.** Same pattern. **Dead code.**

Two writer-without-caller instances confirmed. These are not store writers in the projection-registry sense — they are pure-function primitives — but they occupy the same "designed, implemented, tested, never invoked" semantic slot that the 2026-04-12 reputation audit flagged for `ValidatorReputationStore.RecordVote` and `CalibrationStore.Increment`. The designed-for caller is the submit handler (which would compute the Merkle root over trajectory commits and populate `Evidence.ExplorationRoot`). That caller does not exist.

### Wiring at startup

`cmd/node/main.go:2243–2248`:
```go
trajSvc := trajectory.NewService(
    trajectory.DefaultTrajectoryConfig(),
    stack.dag, blobStore, node, stack.taskMgr, stack.kp,
)
trajSvc.SetPublisher(pub)
apiSrv.SetTrajectoryService(trajSvc)
```

Construction happens only when `enableMarketplace` is true (the enclosing block, `cmd/node/main.go:2232+`). `DefaultTrajectoryConfig()` at `service.go:42–47` ships enabled by default: `Enabled: true`, per-task limit 100 commits, per-agent 10 commits/minute, body size 1 MiB, payload size 64 KiB. The API server's `SetTrajectoryService` stores the service in `s.trajService`; handlers 501 gracefully if the field is nil.

No recognition-fabric consumer is registered for `EventTypeTrajectoryCommit`. `cmd/node/main.go:1767–2258` registers: `OCSSubmitConsumer`, `OCSVoteConsumer`, `TaskLifecycleConsumer`, `EvidenceReadinessConsumer`, `TaskVerificationRoundConsumer`, `TaskVerificationVoteConsumer`, `TaskVerificationConsensusConsumer`, `SettlementConsumer`, `BlobDemandConsumer` — none of them are interested in trajectory commits. The events commit to the DAG and fire recognition dispatch with no downstream subscriber.

---

## Q2 — What the data model captures

### `CheckpointBody` (body.go:19–38)

```go
type CheckpointBody struct {
    ApproachDescription    string
    Parameters             map[string]string
    EvidenceSnippet        string
    ErrorDetail            string
    IntermediateOutputHash string
}
```

Field-by-field semantic:
- `ApproachDescription`: free-form narrative of the current approach being tried. **Path-adjacent**: captures what the agent is doing at this step.
- `Parameters`: key-value config / hyperparameters at this checkpoint. Captures state of the attempt.
- `EvidenceSnippet`: a sample of the work output at this step. Partial work product.
- `ErrorDetail`: free-form error text. The only explicit negative-knowledge field, but unstructured.
- `IntermediateOutputHash`: content hash of an intermediate artifact (partial draft, partial code). Can be used to anchor sub-artifacts cross-checkpoint.

### `TrajectoryCommitPayload` (`internal/event/trajectory.go:41–74`)

```go
type TrajectoryCommitPayload struct {
    Version         uint8
    TaskID          string
    ParentCommitID  string        // EventID of parent commit — forms causal chain
    Outcome         TrajectoryOutcome // exploring | dead_end | pivot | converged
    CheckpointHash  string        // SHA-256 hex of CheckpointBody canonical bytes
    CheckpointSize  int64         // byte size of canonical body
    ComputeCost     uint64        // self-reported micro-AET
    QualityScoreBP  uint32        // self-assessed quality [0, 10000]
    CategoryHint    string
    BranchID        string        // exploration branch identifier (for tree-style branching)
}
```

`TrajectoryOutcome` enum at `internal/event/trajectory.go:9–20`: `exploring`, `dead_end`, `pivot`, `converged`. The `dead_end` and `pivot` values are direct negative-knowledge markers — the agent self-declares that the current approach is abandoned or being switched.

### Thesis dimension check

| Dimension | Schema support | Present in reality |
|---|---|---|
| **Path (sequence of steps)** | ✓ `ParentCommitID` forms a per-task linked list; retrieval via `GetTrajectories` returns them in deterministic order (`service.go:320–379`). | ✓ Structural support present. |
| **Branching (exploration tree)** | ✓ `BranchID` lets an agent fork exploration, maintaining separate chains. Multiple children per parent is possible via the DAG's append-only semantics (not enforced at schema level; any agent could emit two children with the same `ParentCommitID`). | ⚠️ Present as a string field. No enforcement that branches are consistent; "tree" is implicit in the pattern of parent references. |
| **Attempts (positive steps)** | ✓ Each checkpoint IS an attempt. `ApproachDescription` + `Parameters` + `EvidenceSnippet` document the attempt state. | ✓ |
| **Failures / abandoned approaches** | ✓ `outcome = dead_end` or `pivot` is a first-class signal. `ErrorDetail` gives free-form error text. | ⚠️ Present but unstructured. The agent must populate — no automatic capture of execution failures, no enforced taxonomy of failure modes. "Abandoned" is declaratively self-asserted, not structurally proved. |
| **Reasoning / discarded logic** | ✗ No reasoning-trace field. `ApproachDescription` is a summary narrative; it is not a structured reasoning chain. `ErrorDetail` is failure text, not rejected-reasoning text. | ✗ Effectively absent. Workers can stuff reasoning into `ApproachDescription` as prose, but the schema treats it as an unstructured string, so downstream tools cannot query "what reasoning was discarded on this task" in any disciplined way. |
| **Content addressing** | ✓ SHA-256 of canonical bytes (`ComputeCheckpointHash` at `body.go:93–99`). | ✓ Full content-addressing on the body, content-reference in the payload. |
| **Integer-canonical / deterministic (principle 11)** | ✓ All numeric fields are integers (`uint64`, `uint32`, `int64`, `uint8`). No `float64`, no `time.Time` in the canonical payload. | ✓ |
| **JCS serialization** | ✗ Not JCS. `body.go:40–48` + `:57–88` use a hand-rolled `checkpointCanonical` struct with alphabetical field order + sorted parameter keys + `json.Marshal`. Functionally deterministic for this schema, but not the protocol's standard JCS (RFC 8785) path used in, e.g., `internal/taskverification/round.go:306–310`. | ⚠️ Deterministic by construction, inconsistent with the rest of the protocol's canonicalization pattern. Flagged under related observations. |

### Verdict at the data-model level

The schema can store path, attempts, and abandonment declarations. It can anchor content. It runs integer-only canonical. It does **not** capture structured reasoning, does **not** enforce attempt taxonomies, does **not** enforce branch consistency. Workers can emit well-formed trajectory data that describes their path in English; they can also emit trajectory that records nothing useful. The protocol does not differentiate at the schema level.

**Summary**: the data model is thin but **real** — sufficient to carry the thesis's "path + attempts + failures" dimensions if agents populate it thoughtfully; insufficient to carry "reasoning" as a first-class field. The thesis-critical gap at the data-model layer is reasoning; the thesis-critical gap at the integration layer is every dimension below (Q3, Q4).

---

## Q3 — Where trajectory capture happens in the protocol flow

The flow traces as a sequence of capture points and the verdict for each.

### Stage 1 — Agent execution (pre-submission)

- **Capture mechanism**: `POST /v1/tasks/{id}/trajectory/commit` (`internal/api/trajectory_handler.go:31–95` → `service.EmitCommit` at `service.go:141–300`).
- **Worker-side SDK**: Python SDK `emit_trajectory_commit(...)` at `sdk/python/aethernet/client.py:1174–1231`. Go SDK `EmitTrajectoryCommit(...)` at `pkg/sdk/client.go:1000–1010`. Both accept the full request body.
- **What gets stored**: the canonical `CheckpointBody` bytes go into the local blobstore (`service.go:200`); a lean `EventTypeTrajectoryCommit` DAG event carries only the hash + metadata (`service.go:206–217`); event is signed, submitted to OCS, published via the local publisher (`service.go:259–283`).
- **Streaming**: incremental — an agent can call `EmitCommit` repeatedly during execution. Each commit chains to the previous via `ParentCommitID`.

**Verdict**: streaming capture during execution ✓ works. This is the one stage where trajectory is actually wired.

### Stage 2 — Submission

- **Submit handler**: `POST /v1/tasks/{id}/submit` at `internal/api/server.go:1676–1775` (handler `handleSubmitTask`).
- **Request body**: `submitTaskRequest` (`server.go:1338–1346`). Contains `Evidence *evidence.Evidence` as an optional field.
- **`Evidence` struct** (`internal/evidence/evidence.go:16–49`) has two trajectory-reference fields:
  - `ExplorationRoot string` (`:36`) — intended to hold a Merkle root over all trajectory commit EventIDs for the task.
  - `ExplorationSample []string` (`:41`) — intended to hold a bounded sample (≤ `MaxExplorationSample = 10`) of commit EventIDs for quick inspection.
- **Population**: **neither field is ever populated by any production code**. `handleSubmitTask` accepts whatever `Evidence` the caller supplies and stores it as-is. The handler does not query trajectory commits; it does not call `trajectory.ComputeExplorationRoot`; it does not call `SampleExplorationCommits`. No SDK call is documented to populate these fields either — agents would have to manually compute a Merkle root client-side, know the EventIDs of their own commits, and fill the structure themselves.
- **`TaskSubmittedPayload`** (`internal/event/event.go:702–712`): no trajectory field. The DAG event for submission carries `EvidenceBodyHash` pointing at the `Evidence` blob; the Evidence blob, if parsed, carries the unpopulated trajectory fields.

**Verdict**: submission infrastructure has the shape for trajectory reference but the workflow does not fill it. A worker who calls the SDK's `SubmitTaskResult` without manually building an Evidence object with `ExplorationRoot` populated will produce an Evidence blob where the trajectory-linkage fields are empty.

### Stage 3 — Verification

- **Analyzer families** at `internal/verification/families/` (`deterministic_heuristic`, `embedding_similarity`, `llm_semantic`, `statistical_structural`): grep for `trajectory` / `Trajectory` / `ExplorationRoot` returns **zero matches** across the entire `families/` tree.
- **`MultiVoter`** at `internal/autovalidator/auto.go`: consumes the evidence content via `Evidence.ResolveContent()` and passes it to analyzers. Does not read trajectory fields.
- **`TaskVerificationVotePayload`** (`internal/event/event.go:730–746`): no trajectory reference.
- **`TaskVerificationRound`** (`internal/taskverification/round.go`): the round struct stores vote records and aggregation state. No trajectory field.
- **`TaskVerificationConsensusPayload`** (`internal/event/event.go:748–767`): no trajectory reference.

**Verdict**: validators are blind to trajectory at every level. Verification verdicts are reached without inspecting the path the agent took. This is a direct violation of the thesis's "structurally independent verification of trajectory" dimension — nothing about trajectory is being structurally verified.

### Stage 4 — Cross-node propagation

- **Lean DAG events propagate**: `EventTypeTrajectoryCommit` events ride the existing Fast-Path body plane and the peer-sync channel. Every node receives every trajectory event.
- **Checkpoint bodies do NOT propagate**: `internal/blobsync/extractors.go:17–18`:
  ```go
  // TrajectoryCommit carries the checkpoint blob hash.
  // NOTE: not wired in prompt 01; noted for future registration.
  // _ = registry.Register(string(event.EventTypeTrajectoryCommit), "", extractTrajectoryCommitBlobs)
  ```
  The extractor is explicitly commented out. BlobSync does not generate demand for trajectory blobs on peer nodes. The body content exists only on the node where the agent submitted the commit.

**Verdict**: the protocol has a one-way information leak. DAG events tell every node *what* was committed (outcome, hash, size, parent); checkpoint bodies tell only the originating node *what the checkpoint actually said*. A remote node cannot retrieve the prose of `ApproachDescription` for any commit it did not itself receive the body for. This is a direct violation of the thesis's "on-chain dataset" dimension — the dataset, as structured today, is node-local, not network-held.

### Stage 5 — Settlement

- **`TaskSettlementPayload`** (`internal/settlement/verdict.go:66–77`): no trajectory field. Carries `EvidenceHash` only (which points at the unpopulated-`ExplorationRoot` Evidence blob).
- **`settlement.Applicator`**: grep for trajectory / exploration returns zero matches in `internal/settlement/`.
- **Economic path**: worker payout (v4.1: 73/23/2/2 on escrow) is computed without any trajectory input. Generation-ledger ancestors, Q-score weights, fee splits — nothing conditions on trajectory.

**Verdict**: settlement has no coupling to trajectory. Economic outcomes are fully decoupled from whether trajectory data was captured, what the outcome enum said, or whether `Evidence.ExplorationRoot` was populated. The thesis's "economically bonded" trajectory dimension is absent.

### Stage 6 — Post-settlement (archival / dataset)

- **Per-task query**: `GET /v1/tasks/trajectories/{id}` works. Returns commit tree with optional body inclusion. Limited server-side to `MaxTrajectoryLimit = 500` (`view.go:10`).
- **Bulk export**: none. No `/v1/trajectories` index. No CSV/archive dump. No mechanism to "give me every trajectory commit on the network" short of DAG replay + per-event filtering.
- **`aet` CLI**: grep for trajectory in `cmd/aet/` returns zero matches. No operator tooling.
- **Indexed query**: none beyond per-task.
- **Status endpoint**: `GET /v1/status` does not include trajectory counts, recent-commit metadata, or health signals.

**Verdict**: a dataset is NOT assembled by the protocol. Per-task queries work only on nodes that received the bodies (Stage 4 gap). The "commercially valuable dataset" framing of the thesis is not served by what exists today; you could not hand a researcher an export of AetherNet trajectory data, because no such export exists and the data is fragmented per-node.

### Summary flow diagram (prose)

```
Agent execution
  ↓
  [Trajectory capture point 1: EmitCommit] → DAG event + local blob
  ↓ events propagate, bodies stay local
Task submission
  ↓
  [Trajectory capture point 2: populate Evidence.ExplorationRoot] ← THIS STEP NEVER HAPPENS
  ↓
DAG commit of TaskSubmitted
  ↓ events propagate
Validator verification
  ↓
  [Trajectory capture point 3: validators inspect trajectory] ← THIS STEP DOES NOT EXIST
  ↓
TaskVerificationVote → Round → TaskVerificationConsensus
  ↓
  [Trajectory capture point 4: consensus record cites trajectory] ← THIS STEP DOES NOT EXIST
  ↓
Settlement
  ↓
  [Trajectory capture point 5: settlement references trajectory] ← THIS STEP DOES NOT EXIST
  ↓
Archival
  ↓
  [Trajectory capture point 6: dataset accessible] ← NODE-LOCAL, PER-TASK ONLY
```

Of six natural capture / reference points, one (stage 1) is wired end-to-end. Five are either designed-but-unused or structurally absent.

---

## Q4 — Relationship to the reputation evidence record

### Identifier stability

A stable identifier that could be referenced from `ReputationEvidence` exists in principle:

- **Per-commit**: each trajectory commit has a DAG `EventID` — stable, content-addressed, network-replicated. A `ReputationEvidence` field referencing "latest trajectory commit for the round's submission" is trivially definable.
- **Per-task Merkle root**: `ComputeExplorationRoot(eventIDs)` at `merkle.go:30` produces a stable 64-hex-character identifier over the set of commit EventIDs for a task. It is deterministic and order-independent (sorts before hashing). **However, this function is never called in production.** A caller that would populate `Evidence.ExplorationRoot` at submission time does not exist. Until it does, there is no stable per-task trajectory identifier anywhere in the protocol — just a potential one.
- **Per-consensus**: not defined. `TaskVerificationConsensusPayload` has no trajectory field.

### Queryable linkage

- **From `TaskID` → trajectory**: ✓ `GetTrajectories(taskID)` at `service.go:320` works. The linkage is scan-based (iterate DAG events, filter by payload's `TaskID`).
- **From `RoundID` → trajectory**: ✗ no direct link. You'd have to resolve `RoundID` → `TaskID` via the round store, then `GetTrajectories(taskID)`.
- **From `ConsensusEventID` → trajectory**: ✗ no direct link. Same indirect path via `TaskID`.
- **From `ReputationEvidence` → trajectory**: ✗ no linkage. The `ReputationEvidence` struct per plan §2.1 has `RoundID`, `ValidatorID`, `Family`, etc., but no `TrajectoryRootHash` or `TrajectoryCommitID` field.

### Linkage to consensus-affecting subsystems

- **Settlement**: none. `TaskSettlementPayload` has no trajectory reference.
- **Slashing**: none. `SlashingEvaluator` reads vote records and reputation; it does not read trajectory.
- **Fee distribution**: none. Q-score is over agreement rate, not over trajectory quality.
- **`ReputationEvidence` record (plan §2.1)**: none.

### What it would take to add a reference cleanly

The minimum integration shape is well-defined:

1. **Extend `ReputationEvidence`** to carry a `TrajectoryRoot string` field (SHA-256 hex of trajectory Merkle root) — derived at evidence-write time from the submission's `Evidence.ExplorationRoot`.
2. **Populate `Evidence.ExplorationRoot`** during submission handling. This requires the submit handler (or an intermediary) to call `GetTrajectories(taskID)` → extract EventIDs → `ComputeExplorationRoot(eventIDs)` → write the root into the Evidence blob before it hashes into the DAG.
3. **Enable BlobSync for trajectory bodies**: uncomment `internal/blobsync/extractors.go:17–18`, so that peer nodes can actually retrieve the content they already have the hash for.
4. **Optionally**: add a `TrajectoryCommitCount uint32` or similar scalar on `TaskVerificationConsensusPayload` so that consensus records carry an observable signal of how much trajectory was captured.

These are not the fix design — just the linkage description. Doing any of this is a design decision for a subsequent workstream.

---

## Q5 — Coverage verdict

### Verdict: (b) — partially covered.

The subsystem is real, architected, tested, and live on the testnet. Agents can emit trajectory during execution; researchers can query per-task commit trees via the API. The data model handles path, attempts, abandonment declarations, content addressing, and integer-canonical payload — all principle-11-compliant.

**But the specific dimensions the thesis requires are missing**:

- **Structural independence** (validators verify the agent's work trajectory as part of consensus): **absent.** Analyzers don't read trajectory; `TaskVerificationVote` doesn't reference it; no recognition consumer fires on trajectory events. The only "verifier" of trajectory data is the agent themselves.
- **On-chain dataset** (network-held, not node-local): **absent.** BlobSync extractor is commented out; checkpoint bodies do not propagate. The dataset, as structured today, is a per-node local archive of what passed through that node's submit API.
- **Economically bonded** (trajectory quality affects payout or slashing): **absent.** Settlement, fee distribution, and slashing have zero trajectory inputs.
- **Linked to reputation evidence** (plan §2.1): **absent.** `ReputationEvidence` has no trajectory field; `Evidence.ExplorationRoot` is schematized but never populated.
- **Reasoning capture** (path + reasoning, not just path + approach): **absent.** The schema has no structured reasoning field; `ApproachDescription` is prose.

The (b) verdict reflects: the primitive is real and useful for a narrow "can agents publish trajectory markers" purpose, but the commercial/thesis-critical dimensions — the ones that would make the dataset "uniquely valuable" per the founder's framing — are not present. Naming this (a) would be wrong because the integration gap is large. Naming this (c) would be wrong because the subsystem is not stubbed; it has a real data model, real API, real SDK, and is handling requests on the live testnet today.

**Specific gaps to name in any follow-up design work**:
1. Merkle-root computation is dead code (`merkle.go:30, 72` — never invoked in production).
2. `Evidence.ExplorationRoot` / `Evidence.ExplorationSample` fields exist but are never populated at submit time.
3. `internal/blobsync/extractors.go:17–18` extractor is commented out; checkpoint bodies never propagate.
4. No analyzer family, recognition consumer, settlement path, slashing path, or fee-distribution path reads trajectory.
5. No consensus record references trajectory.
6. No dataset / bulk-export tooling exists.
7. The `CheckpointBody.CanonicalCheckpointBytes` path uses a hand-rolled ordering rather than the protocol's standard JCS canonicalization (principle 12 "beauty is correctness" signal).
8. No structured reasoning field on `CheckpointBody`.

---

## Q6 — Workstream sequencing implications

The open workstream queue and the audit's effect on each:

### Immediate (not gated by this audit)

**F3-B settlement-divergence fix** — proceeds immediately and unchanged. Settlement integrity is a principle-5 consensus-correctness issue with no dependency on trajectory. Do not delay.

### Gated by this audit

**Reputation Step 4 (evidence-store implementation)** — currently paused pending F3-B. This audit's verdict (b) changes the shape of Step 4's unpause, not whether Step 4 unpauses:

- **If the verdict had been (a)**: Step 4 could resume after F3-B with a small reference-field addition (add `TrajectoryRoot` to `ReputationEvidence`, wire it to existing populated-`Evidence.ExplorationRoot`).
- **Actual verdict (b)**: Step 4 can resume after F3-B, but the plan document (plan §2.1) should be amended to document the trajectory gap explicitly. Two options for Step 4's scope:
  - **Option 4-narrow**: Step 4 ships `ReputationEvidence` without a trajectory reference, noting the gap. A subsequent workstream adds the reference once trajectory integration is built. Cost: a schema migration or a new evidence version when trajectory integration lands.
  - **Option 4-wide**: Step 4 adds a `TrajectoryRoot string` field to `ReputationEvidence` from the start, documenting that it will remain empty until the trajectory-integration workstream ships. Cost: a field on the canonical record that is populated with an empty string for every task for an unknown duration. Favoured because retrofitting a schema after live data exists is the pattern CLAUDE.md warns against.
- **Recommendation**: Step 4 ships Option 4-wide — include the `TrajectoryRoot` field on Day 1, populate it from `Evidence.ExplorationRoot`, document that both are currently always empty, and let the trajectory-integration workstream flip them on later without schema churn.

### New workstream implied by this audit

**Trajectory-integration workstream** — does not exist yet but should be opened between Step 4 and the next thesis-critical workstream (challenge path). Scope:
- Wire `ComputeExplorationRoot` into the submit handler so `Evidence.ExplorationRoot` is populated for every task submission.
- Uncomment and wire the BlobSync extractor for `EventTypeTrajectoryCommit` so bodies propagate to every node.
- Add at least one recognition consumer for trajectory events (even if initially advisory — "count trajectory commits per task" is enough to surface coverage).
- Add a `TrajectoryRoot` observable to consensus records and/or to the reputation evidence record.
- Consider whether the `CheckpointBody` schema needs a structured reasoning field (founder design decision — not in audit scope).
- Consider whether validator analyzer families should begin reading trajectory (thesis says yes; design decision on timing).

### Gated by both this audit and trajectory integration

**Challenge path workstream** — the founder's workstream plan (`docs/plans/2026-04-12-reputation-and-consensus-integrity.md` §17 point 14, "non-deprioritization commitment") names challenge path as the immediate successor to the reputation workstream. The challenge path's adjudication mechanism adjudicates disputes about whether a validator's vote was honest. **Adjudication without access to the agent's trajectory is adjudicating a shadow**: adjudicators see verdicts and evidence content but not the path-of-work. For the challenge path to adjudicate well, trajectory must be cross-node-available and validator-inspected. Recommend: open the challenge-path workstream AFTER trajectory integration, not before.

**Data ingestion workstream** (§17 Workstream 1 in the handoff) — the founder has framed this as the next priority after the reputation workstream completes. Data ingestion is where the thesis's "uniquely valuable dataset" framing cashes out — if the dataset is not real, data ingestion has nothing to monetize or serve. Strict dependency on trajectory integration being complete.

### Recommended ordering (reasoning, not a lock)

1. **F3-B settlement-divergence fix** — proceeds now regardless.
2. **Reputation workstream Step 4 (evidence store)** — resume after F3-B, ship Option 4-wide (include `TrajectoryRoot` field on Day 1, populated-empty until integration lands).
3. **Trajectory-integration workstream** — open after Step 4. Wire the four integration points (submit-handler population, BlobSync extractor, evidence-record reference, validator visibility if design agrees). Keep scope tight; may require its own multi-AI review because it touches validator semantics.
4. **Challenge-path workstream** — after trajectory integration. Adjudication needs trajectory to be load-bearing.
5. **Data ingestion workstream** — after challenge path. Dataset monetization needs trajectory to be a real network-held dataset, not a per-node archive.

The sequencing is a recommendation, not a lock. The founder may choose to overlap trajectory integration with Step 4 (they don't touch the same code surface) or to split the trajectory workstream into "propagation + linkage" (narrow, 1 week) and "validator semantics" (broader, multi-AI review required). Both are viable.

---

## Founder decision required

Ordered by priority.

### Decision 1 — Step 4 scope

Ship `ReputationEvidence` in Step 4 with the `TrajectoryRoot` field present (Option 4-wide) or without (Option 4-narrow). Recommendation above favors wide.

### Decision 2 — Trajectory-integration workstream: open now or later?

Timing options:
- (a) Open trajectory integration as Step 3.5 before Step 4. Bundles the evidence-record schema and trajectory integration into a single coherent change.
- (b) Open trajectory integration as a separate workstream after Step 4. Keeps Step 4 tight; lets trajectory integration be its own focused piece with multi-AI review where needed.
- (c) Defer trajectory integration entirely until after the challenge-path workstream is specified, reasoning backwards from what challenge-path needs.

Recommendation (b). The audit's evidence shows trajectory is a distinct subsystem with its own design surface (especially on validator semantics); bundling with Step 4 makes Step 4 hard to review.

### Decision 3 — Validator visibility of trajectory

Should validators read trajectory data as part of verification? The thesis says yes. The current code says no. Building this changes the analyzer-family contract and requires design on:
- Which families should consume trajectory (all? a new `trajectory_structural` family? an auxiliary signal fed into the existing families?).
- How trajectory availability affects verification when a peer node didn't receive a body (which it currently never does).
- Whether trajectory reading creates new attack surfaces (can an adversary poison trajectory to manipulate validator behavior?).

This is a design decision for the trajectory-integration workstream; flag it here so it enters the queue with the right scope.

### Decision 4 — Reasoning field on `CheckpointBody`

The audit found the schema has no structured reasoning field; reasoning goes into `ApproachDescription` as prose. The thesis explicitly names "reasoning" as a captured dimension. Decide whether to:
- (a) Accept prose reasoning as sufficient (thesis language is loose; unstructured is fine for v1).
- (b) Add a structured reasoning field (`Reasoning []ReasoningStep` or similar) in the trajectory-integration workstream.
- (c) Defer to a future revision of `CheckpointBody` under a new schema version.

Not gating Step 4; gating the trajectory-integration workstream's scope.

### Decision 5 — Write-offs

Two small write-offs the audit surfaced:
- `CheckpointBody` canonicalization is hand-rolled, not JCS. Inconsistent with the protocol's other canonical paths. Decision: tolerate (it's functionally deterministic) or convert to JCS in the trajectory-integration workstream (cleaner, matches principle 12).
- The `handleGetTrajectories` handler comment at `trajectory_handler.go:97` documents a different route than what's actually registered. One-character documentation fix; doesn't need a workstream.

---

## Related observations

### A. Trajectory-submission has no atomicity with task submission

Workers must call `EmitTrajectoryCommit` and `SubmitTaskResult` separately. No protocol-level guarantee that a worker who submits a result also submitted trajectory, or vice versa. A worker can:
- Submit 0 trajectory commits + a result → task settles with empty trajectory archive.
- Submit many commits + no result → trajectory accumulates forever, task never settles.

The protocol does not mandate trajectory submission. Decision point for the trajectory-integration workstream: require it, make it optional, or gate certain task categories on trajectory being present.

### B. Per-agent rate limit has a clock-rollover edge case

`internal/trajectory/service.go:90` tracks commits per Unix-minute. At minute boundaries, a worker could emit 10 commits at 59:59 and 10 more at 00:00, getting 20 commits in under 2 seconds. Not a critical bug, but inconsistent with the stated "10/minute" limit. Flagged for future hardening.

### C. No query-time authorization on `GET /v1/tasks/trajectories/{id}`

Any caller can query any task's trajectory. This is probably intentional (on-chain data is public) but worth naming — if the founder intends private-by-default for the data ingestion workstream, this surface is open-by-default.

### D. `MaxTrajectoryLimit = 500` is compile-time

`internal/trajectory/view.go:10`. Operators can't tune without recompilation. Low priority; mentioned for future tunability workstream.

### E. The Python SDK has `emit_trajectory_commit` with sensible defaults (`quality_score_bp=5000`) — worker-adoption surface is in good shape

Workers using `sdk/python/aethernet/client.py` can call the method with minimal ceremony. The SDK is not the bottleneck for adoption; the protocol-level integration is.

### F. Trajectory service is gated on `enableMarketplace`

`cmd/node/main.go:2232` onward. A non-marketplace-enabled node does not construct `trajSvc`. This means trajectory is an opt-in subsystem at the node level today. For the thesis's "universal dataset" framing, this gate would need to be removed — trajectory would be a default-on subsystem. Decision for the trajectory-integration workstream.

### G. Other subsystems worth spot-checking for the same writer-without-caller pattern

The audit surfaced two dead-code functions in `merkle.go`. Given the prior 2026-04-12 reputation audit found the same pattern in `ValidatorReputationStore.RecordVote` and `CalibrationStore.Increment`, and the step-3 projection-registry lint now catches these at CI, it may be worth running the step-3 lint's heuristic across the rest of the repo to surface any other "designed, implemented, never invoked" primitives before they bite the next workstream. Out of scope here; flagged for future tooling.

---

## Evidence inventory

### Code citations (all paths relative to `/Users/michaelschreiber/aethernet/`)

**Package internals**:
- `internal/trajectory/body.go:19–38` — `CheckpointBody` struct.
- `internal/trajectory/body.go:57–88` — `CanonicalCheckpointBytes()` (hand-rolled canonical serialization).
- `internal/trajectory/body.go:93–99` — `ComputeCheckpointHash()`.
- `internal/trajectory/merkle.go:17` — `MaxExplorationSample = 10`.
- `internal/trajectory/merkle.go:30` — `ComputeExplorationRoot()` (dead code).
- `internal/trajectory/merkle.go:72` — `SampleExplorationCommits()` (dead code).
- `internal/trajectory/service.go:42–47` — `DefaultTrajectoryConfig()`.
- `internal/trajectory/service.go:72–86` — `Service` struct.
- `internal/trajectory/service.go:94` — `NewService()`.
- `internal/trajectory/service.go:141–300` — `EmitCommit()`.
- `internal/trajectory/service.go:200` — `blob.Put` for canonical body.
- `internal/trajectory/service.go:259–283` — event publish path.
- `internal/trajectory/service.go:303` — `SetPublisher`.
- `internal/trajectory/service.go:320–379` — `GetTrajectories()`.
- `internal/trajectory/view.go:10` — `MaxTrajectoryLimit = 500`.
- `internal/trajectory/view.go:48` — `CommitNodeFromEvent`.

**Event model**:
- `internal/event/trajectory.go:9–20` — `TrajectoryOutcome` enum.
- `internal/event/trajectory.go:41–74` — `TrajectoryCommitPayload`.
- `internal/event/event.go:86–90` — `EventTypeTrajectoryCommit`.
- `internal/event/event.go:702–712` — `TaskSubmittedPayload` (no trajectory fields).
- `internal/event/event.go:730–746` — `TaskVerificationVotePayload` (no trajectory fields).
- `internal/event/event.go:748–767` — `TaskVerificationConsensusPayload` (no trajectory fields).

**Evidence model with unpopulated trajectory fields**:
- `internal/evidence/evidence.go:33–41` — `ExplorationRoot` and `ExplorationSample` fields (never populated in production).

**API layer**:
- `internal/api/server.go:486–487` — route registrations.
- `internal/api/server.go:564–566` — `SetTrajectoryService`.
- `internal/api/server.go:1338–1346` — `submitTaskRequest` (no trajectory field).
- `internal/api/server.go:1676–1775` — `handleSubmitTask` (does not populate `Evidence.ExplorationRoot`).
- `internal/api/trajectory_handler.go:31–95` — `handleTrajectoryCommit`.
- `internal/api/trajectory_handler.go:97` — route-documentation mismatch.
- `internal/api/trajectory_handler.go:100–131` — `handleGetTrajectories`.

**Wiring**:
- `cmd/node/main.go:2232` — `enableMarketplace` gate.
- `cmd/node/main.go:2243–2248` — trajectory service construction and wiring.

**Related subsystems' non-integration**:
- `internal/blobsync/extractors.go:12–19` — trajectory extractor commented out.
- `internal/settlement/verdict.go:66–77` — `TaskSettlementPayload` (no trajectory field).
- `internal/recognition/` — no consumer for `EventTypeTrajectoryCommit`.
- `internal/verification/families/` — no trajectory references (grep returns zero).

**SDK**:
- `pkg/sdk/client.go:951–995` — trajectory types.
- `pkg/sdk/client.go:1000–1010` — `EmitTrajectoryCommit`.
- `pkg/sdk/client.go:1016–1037` — `GetTrajectories`.
- `sdk/python/aethernet/client.py:1174–1231` — `emit_trajectory_commit`.
- `sdk/python/aethernet/client.py:1233–1262` — `get_trajectories`.

### Testnet evidence

- `curl -s 'https://testnet.aethernet.network/v1/tasks/trajectories/test-task-id'` returned `{"task_id":"test-task-id","count":0,"commits":null}` at 2026-04-15 (HTTP 200). Confirms `GET` handler is live.
- `curl -s -X POST '…/v1/tasks/nonexistent-task/trajectory/commit' …` returned `{"error":"trajectory: tasks: task not found: nonexistent-task"}` at 2026-04-15. Confirms `POST` handler is live; handler reaches service-layer task validation before failing.

No testnet state was modified by this audit beyond the two read-only curl probes and the agent-registration / task-post activity from the prior settlement-divergence audit (which left the testnet in its current contaminated state, unchanged by this audit).
