# Multi-emit bug-class audit — canonical event emission sites

**Companion to**: `docs/plans/implementation/selection-race-characterization.md` (the underlying bug class).
**Method**: `grep -rn "publisher\.Publish|emitDAGEvent|EmitConsensusEvent" internal/ cmd/ --include="*.go" | grep -v "_test.go"`, walked each non-eventbus production callsite.
**Scope**: every site that publishes a canonical DAG event (i.e., calls `localpub.Publisher.Publish` or `emitDAGEvent`). Internal `eventbus.Publish` calls (websocket fan-out, metrics) excluded — they are not canonical events.
**Output**: bug-class map. **No fix design.**

---

## 0. Definitions and risk taxonomy

**Multi-emit risk** describes the probability and consequence of multiple canonical events being created with the same logical-key payload field but different content (different hash, different effects). This is the precondition for the cluster-wide selection race characterized in `selection-race-characterization.md`.

**Risk levels**:

- **NONE — single-emitter, single-author by design.** Only one specific actor creates the event for a given logical key. No multi-emit possible.
- **LOW — multi-emit-possible but redundant.** Multiple actors can create events with the same logical key, but the payloads are byte-equal across emitters (or the consumer dedupes idempotently with no per-node selection effect).
- **MEDIUM — multi-emit-possible with per-node-distinct payloads but converged effect.** Different actors emit different events for the same logical key, but every node observes the same set and applies them uniformly (via consumer-internal merge logic).
- **HIGH — multi-emit-possible with per-node-distinct payloads AND per-node-distinct selection.** This is the selection-race bug class. Different events for the same logical key exist in the DAG; each node's local handler picks one based on local arrival order; per-node selection differs.

**Logical-key field**: the payload field that identifies "the thing being decided." If two events for the same logical-key value can produce different ledger effects, multi-emit is dangerous. Examples: `TaskID`, `RoundID`, `TargetEventID`, `AgentID`.

**Emitter identity**: who calls the emit code path. Per-node (every validator emits independently), per-role (only a specific node role emits, e.g. "the finalizing validator"), per-user (only the relevant agent emits via API).

---

## 1. Inventory of canonical event emission sites (production code)

### 1.1 Tier 1 — User-initiated events via API server

These flow through `Server.emitDAGEvent` at `internal/api/server.go:1407` or via `submitAndAdd` at `internal/api/server.go:2291`. Each is invoked when an HTTP API endpoint is called by a user/operator via `aet` CLI or external client.

| # | Event type | File:line | Trigger | Emitter ID | Payload determinism | Logical key | Risk |
|---|---|---|---|---|---|---|---|
| 1 | `TaskPosted` | `internal/api/server.go:1535` (`handlePostTask`) | HTTP `POST /v1/tasks` | One node — whichever the ALB routes to. Author = `s.agentID` (the receiving node's identity). | Pure function of request body + `s.agentID` + clock at server. Deterministic for a given request. | `TaskID` (server-generated UUID; unique). | **NONE** — only one node receives the API call; only one event created per task. |
| 2 | `TaskClaimed` | `internal/api/server.go:1687` (`handleClaimTask`) | HTTP `POST /v1/tasks/{id}/claim` | One node — receiving the claimer's signed request. Author = receiving node's `s.agentID`. | Pure function of request body + signer identity + clock. | `TaskID`. | **NONE** — first valid claim wins; subsequent claims rejected at the application layer. |
| 3 | `TaskSubmitted` | `internal/api/server.go:1779` (`handleSubmitTask`) | HTTP `POST /v1/tasks/{id}/submit` | One node — receiving claimer's submission. Author = receiving node. | Function of request + result hash + signer. | `TaskID`. | **NONE** — application-layer single-claim invariant. |
| 4 | `TaskApproved` | `internal/api/server.go:1882` (`handleApproveTask`) | HTTP `POST /v1/tasks/{id}/approve` | One node — receiving approver's request. | Pure function of request + signer. | `TaskID`. | **NONE** — manual flow, single emit. |
| 5 | `TaskDisputed` | `internal/api/server.go:1977` (`handleDisputeTask`) | HTTP `POST /v1/tasks/{id}/dispute` | One node — receiving disputer's request. | Pure function of request + signer. | `TaskID`. | **NONE** — manual flow, single emit. |
| 6 | `Transfer` | `internal/api/server.go:2679` (`handleTransfer` via `submitAndAdd`) | HTTP `POST /v1/transfer` | One node — receiving the transfer request. Author = receiving node's `s.agentID`. | Pure function of request body + signer. | `(from, to, amount, reason, txid)` tuple via OCS engine — TxID dedupes. | **NONE** — TxID-deduped at the application layer. |
| 7 | `Generation` | `internal/api/server.go:2735` (`handleGeneration` via `submitAndAdd`) | HTTP `POST /v1/generation` | One node receiving generation request. | Pure function of request + evidence hash. | `(generating_agent, evidence_hash)` — evidence-hash discriminates. | **NONE**. |
| 8 | `IntegerMigrationActivation` | `internal/api/admin_handlers.go:71` (`handleAdminActivateIntegerMigration`) | HTTP `POST /v1/admin/integer-migration/activate` (Part F-introduced) | One node — receiving admin's signed request. Author = receiving node's `s.agentID`. | Pure function of request + clock. | None functionally — this is a one-shot global activation. | **LOW** — multiple admin emits possible if admin runs the CLI multiple times; consumer's early-idempotency check (`internal/dispatch/integer_migration_activation_consumer.go:103`) handles it. Confirmed working in Part F retry Phase D. |
| 9 | `ValidatorRecoveryKeySet` | `internal/api/server.go:3818` (`handleSetValidatorRecoveryKey`) | HTTP `POST /v1/validator/recovery-key/set` | One node — receiving operator's signed request. Author = receiving node. | Pure function of request + clock. | `(ValidatorID, action)`. | **LOW** — operator action, single emit per operator invocation; consumer should be idempotent on duplicate ValidatorID + same RecoveryPublicKey. Not audited in detail. |
| 10 | `TrajectoryCommit` | `internal/trajectory/service.go:259` (`Service.EmitCommit`) | HTTP `POST /v1/tasks/{id}/trajectory/commit` (worker mid-task checkpoint) | One node — receiving worker's request. Author = the worker's `actorID`. | Pure function of request + checkpoint hash. | `(TaskID, checkpoint_hash)`. | **NONE** — worker-driven checkpoint, single emit per checkpoint. |
| 11 | `ValidatorEmergencySuspend`, `ValidatorRecoveryRotate`, `ValidatorRecoveryRotateCancel` | `internal/api/server.go:3878` (`handleRecoveryEvent`) | HTTP `POST /v1/validator/recovery-event` (recovery-key-signed event submitted client-side) | One node — receiving the pre-signed event. Event is constructed and signed *client-side* by the recovery-key holder; node just publishes. | Payload is fully client-determined. | `(ValidatorID, action)`. | **LOW** — recovery-key holder constructs the event; multiple emits would be from the same key holder; reducer should reject duplicate or obsolete state transitions. Not audited in detail. |

### 1.2 Tier 2 — Bootstrap events at startup

| # | Event type | File:line | Trigger | Emitter ID | Payload determinism | Logical key | Risk |
|---|---|---|---|---|---|---|---|
| 12 | `Registration` (node self-registration) | `cmd/node/main.go:1409` (`startStack`) | Node startup, after stake-and-fund | One per node — every node emits a Registration for ITS OWN agent_id at startup. | Pure function of node's keypair + stake amount + reputation seed. | `AgentID`. | **LOW** — each node registers once per agent_id at startup. Restart re-emits, but the new event has the same `AgentID` payload field; the registry consumer should treat it as idempotent (a second Registration for an already-known agent is a no-op). 5 nodes × 1 own-agent Registration = 5 distinct events in the DAG, byte-distinct because they have different `AgentID` payloads. **No per-logical-key multi-emit.** |
| 13 | `Registration` (peer agent via API) | `internal/api/server.go:2465` (`handleRegisterAgent`) | HTTP `POST /v1/agents` | One node — receiving the user's registration request. | Pure function of request + signer. | `AgentID`. | **NONE** — user registers themselves once; subsequent re-registrations of the same `AgentID` from the same user are application-layer-rejected at `internal/identity/registry.go`. |
| 14 | `GenesisFunding` | `cmd/node/main.go:3250` (`emitGenesisFundingEvent`, called from `cmdGenesis` and auto-genesis) | Node startup with empty buckets | One per node — every node runs auto-genesis on first boot OR when buckets are empty. | Pure function of bucket constants + amount per `genesis.go`. | `(FromBucket, ToAgent, Amount, Reason)`. | **MEDIUM** — every node independently emits its own GenesisFunding events on first boot. Each event is canonically distinct by author. The ledger applier's idempotency on bucket transfers determines whether the cluster converges. **Observed in Part F retry Phase B-verify**: Registration + GenesisFunding event counts were stratified by deploy order (3-15 GenesisFunding per node, 1-5 Registration per node), but ledger state still converged (because TransferLedger.FundAgent / TransferFromBucket has bucket-level idempotency). Not currently a divergence problem in practice; would become one if a future change broke the bucket-side idempotency. |

### 1.3 Tier 3 — Per-validator votes (multi-emit by design, vote events are validator-distinct)

These represent each validator's independent contribution. Multi-emit is the protocol's intent — every validator votes. Per-event-author distinctness is structural (the validator's keypair signs).

| # | Event type | File:line | Trigger | Emitter ID | Payload determinism | Logical key | Risk |
|---|---|---|---|---|---|---|---|
| 15 | `VerificationVote` (legacy) | `internal/autovalidator/auto.go:1308` (`AutoValidator.emitVote`) | Autovalidator processes a Transfer/Generation event needing verification | One per validator — every validator independently emits its vote. Author = `validatorID`. | Verdict computed from local analyzer score; differs across validators by intent. | `(target_event_id, validator_id)` — per-validator-per-target uniqueness; one vote per validator per target. | **LOW** — multi-emit by design. Per-validator-per-target uniqueness means each vote is canonically unique by `(author=validatorID, target)`. Vote-counting is in OCS engine; cluster convergence depends on vote-aggregation logic (separate audit needed; see open question 4 in characterization doc). |
| 16 | `TaskVerificationVote` (per-family) | `internal/autovalidator/multi_voter.go:192` (`MultiVoter.processVote`) | Multi-voter runs analyzer family on a TaskSubmitted event | One per validator per analyzer family — every validator's multi-voter emits one vote per analyzer family it runs. Author = `validatorID`. | Verdict + score per analyzer family — analyzer output is deterministic per validator's local analyzer config (which may differ across validators). | `(round_id, validator_id, analyzer_family)` — uniqueness by validator+family. | **LOW** — multi-emit by design. Vote events from different validators or different families are canonically distinct. The selection race concern is downstream at the aggregation layer (see Tier 4). |
| 17 | `TaskSettlement` | `internal/autovalidator/auto.go:981` (`AutoValidator.settleTask`) | Autovalidator approves a task that hasn't yet been settled | One per validator — every validator's autovalidator settles independently after observing acceptance, emitting its OWN TaskSettlement event. Author = `validatorID`. | Pure function of task fields at settlement moment + score from analyzer. | `TaskID`. | **HIGH** — multiple validators each emit their own TaskSettlement event for the same `TaskID`. Each event has different content (different `validatorID` author, potentially different `ScoreBP`, potentially different `HoldGeneration` flag based on per-node replay-coordinator state). The downstream `recognition.SettlementConsumer` and the `cmd/node/main.go` finalization handler must select one. **Same shape as TVConsensus race.** Whether this currently manifests divergence on the live cluster depends on whether the SettlementConsumer or its admit path has cluster-uniform selection. Worth direct verification. |

### 1.4 Tier 4 — High-risk: consensus-representation events with selection-race shape

These are the events that exhibit the bug class characterized in the companion document.

| # | Event type | File:line | Trigger | Emitter ID | Payload determinism | Logical key | Risk |
|---|---|---|---|---|---|---|---|
| 18 | `TaskVerificationConsensus` (deadline path) | `internal/taskverification/deadline_checker.go:414` (`applyFinalization` calls `EmitConsensusEvent`) | Per-validator deadline checker fires when a round reaches a finalization deadline. | One per validator. Author = `validatorID`. | **NOT pure** — payload `FinalVerdict` and `FinalScoreBP` derived from `decision.Verdict`/`FinalScoreBP`, which is computed by `c.finalizer.Evaluate(round, totalWeight, now)` from `round.Votes`. `round.Votes` accumulates in fastpath-receive-order, which differs across nodes. **Different validators can compute different verdicts at the moment they finalize.** | `RoundID`. | **HIGH** — confirmed bug. See `selection-race-characterization.md` §2 for empirical evidence (Phase C-sanity tasks 77c2bdf1 and 4ba459c6). Multiple TVConsensus events per round in the DAG with conflicting verdicts; per-node first-event-past-the-mutex wins. |
| 19 | `TaskVerificationConsensus` (vote-consumer path) | `internal/recognition/task_verification_vote_consumer.go:210` (`Consume` calls `EmitConsensusEvent`) | Per-validator vote consumer detects supermajority on a vote arrival. | One per validator (each runs its own vote consumer). Author = `validatorID`. | Same as #18 — derived from local `round.Votes`, `decision.Verdict`. | `RoundID`. | **HIGH** — same bug as #18; this is the second emit path for the same event type. Compounds the multi-emit count: a single round can produce up to (5 deadline-checker emits) + (5 vote-consumer emits) = 10 distinct TVConsensus events. Empirically observed: tasks `77c2bdf1` had 6 events emitted, `4ba459c6` had 7 events emitted. |
| 20 | `Settlement` | `cmd/node/main.go:1770` (in `engine.SetFinalizationHandler` callback inside `startStack`) | OCS engine signals consensus finalization on a target event (Transfer, Generation, etc.). | One per validator — every node's OCS engine independently fires its finalization handler on local supermajority detection. Author = local node's `agentID`. | **NOT pure** — payload includes `Attestations` (sorted, but the SET differs across nodes because each node sees different vote orderings at the moment of finalization), `ConsensusRound`, `VerifiedValue`. **Different nodes can produce different attestation sets and different verdicts at the moment of finalization.** Comment at line 1782-1783 acknowledges this: `if err := pub.Publish(settlementEv); err != nil { return // duplicate — another node already created one }`. | `TargetEventID`. | **HIGH** — same shape as TVConsensus. Each node's finalization handler emits a Settlement event for the same `TargetEventID`; different nodes can produce different attestation sets, different verdicts, different timing. The downstream `recognition.SettlementConsumer` (registered at `internal/recognition/settlement_consumer.go:53`) handles applying them. Whether this currently produces cross-node divergence depends on the SettlementConsumer's selection logic — worth direct verification analogous to the TVConsensus diagnostic. **Strong suspect for Divergence (A) (the pre-existing 50 AET stake-state divergence)** because Transfer events going through OCS finalization and being settled via this path is the staking flow. |

### 1.5 Tier 5 — Edge events

| # | Event type | File:line | Trigger | Emitter ID | Payload determinism | Logical key | Risk |
|---|---|---|---|---|---|---|---|
| 21 | `PrerequisiteWithholding` | `internal/dispatch/deferral.go:159` (`emitWithholdingEvidence`) | Dispatcher detects an admission record stuck in `reserved-pending-prerequisites` past `DeferralComplaintThreshold` epochs. | One per validator — each node's dispatcher independently fires when its local record meets the threshold. Author = `"dispatcher"` (string literal — see line 163). | Mostly deterministic: `StuckEventID`, `StuckEventType`, `MissingPrerequisites`, `DeferredSinceEpoch`, `CurrentEpoch`. `CurrentEpoch` differs across nodes that fire at different moments. | `StuckEventID`. | **MEDIUM** — multi-emit possible (every node's dispatcher independently watches its own admission state). Payloads have differing `CurrentEpoch` values when nodes fire at different epochs — distinct events. The `EvidenceEmitted` flag on the admission record (line 179) prevents same-node double-emit, but **does not prevent cross-node multi-emit**. The downstream consumer (slashing? evidence aggregator?) must handle multiple `PrerequisiteWithholding` events for the same `StuckEventID` from different validators. Whether selection logic exists is not audited here. |

---

## 2. Pattern: HIGH-risk events all share these properties

Emission sites #17 (TaskSettlement), #18-19 (TaskVerificationConsensus), and #20 (Settlement) all have the same structural shape:

1. **Per-node emission**: every validator independently runs the emit code path. There is no single elected emitter.
2. **Same-logical-key field**: `TaskID` (settlement, task settlement) or `RoundID` (TVConsensus) or `TargetEventID` (Settlement). The downstream consumer uses this field to identify "the same thing being decided."
3. **Per-node-distinct payload content**: the verdict / final score / attestation set is derived from local state (`round.Votes`, `engine` state, autovalidator scoring) that varies across nodes due to fastpath arrival order, local analyzer execution timing, or local view of vote weights at finalization moment.
4. **Per-node first-event-past-the-mutex selection**: downstream consumers (`TVConsensusConsumer.Apply`, `SettlementConsumer`, `Applicator.Apply`) use task-terminal / target-already-applied / IsApplied pre-checks that short-circuit on the second-and-later events. The first event admitted on a given node wins that node's settlement; subsequent events for the same key short-circuit. **Per-node winner ≠ cluster winner.**

This is the bug class. It generalizes beyond TVConsensus.

---

## 3. Pattern: LOW/NONE-risk events all share these mitigating properties

Tier 1 (user-initiated) and Tier 2 (bootstrap) events have one or more of:

- **Single emitter by API design** — only the receiving node creates the event. No cross-node multi-emit possible.
- **Logical-key uniqueness enforced at emit time** — `TaskID` is server-generated UUID; `(from,to,amount,reason,txid)` Transfer is TxID-deduped at the OCS engine; first valid `TaskClaimed` wins at the application layer.
- **Idempotent application at consumer** — `RegistrationConsumer` is idempotent on `AgentID`; `IntegerMigrationActivationConsumer` short-circuits on store-state.
- **Bucket-level idempotency** — `GenesisFunding` cluster-wide convergence relies on `TransferLedger`'s bucket-level idempotency, which currently holds.

These are NOT structural protections — they're per-event-type ad-hoc properties. None of them are protocol-level invariants. A future change that breaks any of them (e.g., changes Registration's consumer to be non-idempotent, or removes TxID dedup from Transfer) would re-introduce the bug at that emission site.

---

## 4. Code-level walkthrough — TVConsensus emit-site mechanism (confirmed bug)

(Detailed in `selection-race-characterization.md` §3; reprised here briefly with file:line citations for audit completeness.)

### 4.1 Emit code

`internal/taskverification/deadline_checker.go:399-423`:

```go
func (d *DeadlineChecker) applyFinalization(ctx context.Context, round *TaskVerificationRound, decision FinalizationDecision, now int64) {
    targetState := VerdictToState(decision.Verdict)
    if err := round.Transition(targetState, now); err != nil { return }
    round.FinalVerdict = decision.Verdict     // ← per-node-local view
    round.FinalScoreBP = decision.FinalScoreBP
    if err := d.rounds.SaveRound(ctx, round); err != nil { ... return }
    EmitConsensusEvent(round, d.publisher, d.kp, d.validatorID, decision.Reason)
}
```

`internal/taskverification/deadline_checker.go:428-503` (`EmitConsensusEvent`):
- Author: `string(validatorID)` — local validator's identity.
- Payload: `TaskVerificationConsensusPayload{... FinalVerdict: round.FinalVerdict.String(), FinalScoreBP: round.FinalScoreBP, ...}` — derived from per-node-local `round` state.
- Calls `publisher.Publish(ev)` — local publish path → DAG → fastpath broadcast.

The vote consumer at `internal/recognition/task_verification_vote_consumer.go:210` calls the same `EmitConsensusEvent` from a parallel code path (supermajority detected on vote-arrival rather than on deadline).

### 4.2 Selection code

`internal/dispatch/tv_consensus_consumer.go:130-182` (`TVConsensusConsumer.Apply`):
- Per-task mutex via `c.taskMu(payload.TaskID)` (line 136) — serializes concurrent Apply calls for the same task.
- Pre-check 1 at line 144-149: short-circuit if task is in terminal status. **First event past the mutex sets the task to terminal; subsequent events for the same task return nil here without applying.**
- Pre-check 2 at line 156-158: short-circuit if escrow has settlement started. Similar function.
- Settlement runs with **this event's payload** (line 174: `c.settler.Settle(ctx, &payload, round)`). Different events for the same task have different `payload.FinalVerdict` and `payload.FinalScoreBP`.

The dispatcher itself (`internal/dispatch/dispatcher.go:99-158`) has no cross-event reconciliation. Its admission key is per-event content-hash (line 100: `key, err := AdmissionKey(ev)`), not per-(RoundID). Two events for the same RoundID with different payloads get different admission keys and are admitted independently.

### 4.3 Settlement event emit + selection (parallel pattern)

`cmd/node/main.go:1717-1792` (the `engine.SetFinalizationHandler` callback) follows the same pattern for Settlement events:
- Every node's OCS engine fires the handler independently when local supermajority is detected.
- Payload built from local `votingRound.GetRecord(targetID)` — `Attestations` set varies per-node.
- `pub.Publish(settlementEv)` at line 1782 with the inline acknowledgment `// duplicate — another node already created one` indicating awareness that multi-emit happens.

`internal/recognition/settlement_consumer.go:53` is the consumer for `EventTypeSettlement`. It would face the same selection race when multiple Settlement events for the same `TargetEventID` arrive.

---

## 5. Open questions surfaced during the audit

These are NOT in scope for the audit document but flagged for architect-session attention.

1. **Settlement consumer selection logic not yet audited at the same level of rigor as TVConsensus.** The audit table shows `EventTypeSettlement` as HIGH risk by structural shape, but I haven't traced a specific divergence to it on the live cluster. Worth a Diagnostic-1-shape investigation: pick a recent Settlement target, query each node's view of the resulting ledger mutation, see if Node 2 (or any node) shows divergent application similar to TVConsensus. **Strong hypothesis: this mechanism causes Divergence (A)**, the pre-existing 50 AET stake-state divergence.
2. **TaskSettlement emission (#17) is structurally identical to TVConsensus.** Every validator's autovalidator emits its own TaskSettlement event for the same TaskID. The downstream consumer (`recognition.SettlementConsumer` and the related applicator path) must handle the selection. Did F3-B's per-task-mutex protection cover this path, or only TVConsensus? Code review needed.
3. **`PrerequisiteWithholding` (#21) author is the string literal `"dispatcher"`.** Multiple nodes' dispatchers all use the same author string, which could collide at the canonicalization layer (depending on whether the canonical event ID includes timestamp/clock). If collisions occur, only one node's PrerequisiteWithholding event would be in the DAG; if collisions don't occur (different `CurrentEpoch` field discriminates), multiple events exist and the same selection race applies to whichever consumer reads them.
4. **Vote events (#15, #16) are by-design multi-emit** — VOTING is the protocol's cluster consensus mechanism. The selection bug doesn't apply to votes themselves (each vote is `(validator_id, target)`-keyed and structurally per-validator-distinct). But the AGGREGATION of votes into a verdict is where the bug surfaces (in TVConsensus and Settlement emits driven by aggregation results). Vote ingestion order varying per node is the upstream root cause that turns into the downstream selection race.
5. **GenesisFunding (#14) ledger convergence relies on bucket-level idempotency.** This currently works (Part F retry Phase B-verify confirmed economic state identical across nodes) but is an unstated invariant. Worth documenting explicitly.
6. **Registration multi-emit (#12, #13)** is currently safe because the registry is idempotent on `AgentID`. Same unstated-invariant concern.

---

## 6. Bug-class scope summary

Of 21 distinct emission sites enumerated:

- **NONE** risk: 7 sites (events 1-7).
- **LOW** risk: 7 sites (events 8-13, 15, 16) — multi-emit possible but mitigated by per-event-type ad-hoc idempotency.
- **MEDIUM** risk: 2 sites (events 14, 21) — multi-emit possible, mitigation depends on consumer-side merge logic that's not audited deeply.
- **HIGH** risk: **5 sites (events 17, 18, 19, 20, plus the Settlement-consumer selection)** — same structural shape as the confirmed bug.

The HIGH-risk sites all converge on three event types: `TaskSettlement`, `TaskVerificationConsensus` (two emit paths), and `Settlement`. They share the structural shape: per-validator emission + payload derived from local-arrival-order-dependent state + downstream first-event-wins selection. **Any architectural fix that addresses TVConsensus alone leaves the other 4 HIGH-risk sites uncovered.** A class-level fix is needed.

---

## 7. What this audit does not provide

- **No fix design.** Architect session selects from `selection-race-characterization.md` §7 fix space.
- **No claim that LOW/MEDIUM/NONE risks are permanently safe.** They depend on per-event-type ad-hoc idempotency that future code changes could break. A class-level fix would also harden these against regression.
- **No live-divergence trace for the suspected-but-unverified HIGH sites (#17, #20, Settlement consumer).** The cluster's frozen state remains available for further evidence-gathering.
- **No cross-correlation with existing F3-B verification artifacts** — the F3-B-supposed-fix's per-task mutex covered TVConsensus admission (event 18-19) but not necessarily the other HIGH sites; verifying coverage is a follow-up investigation.

---

**End of multi-emit bug-class audit.**

Architect-session input: this audit, plus `selection-race-characterization.md`, plus the frozen testnet state. Output: scoped fix-shape decision that addresses the class, not the instance.
