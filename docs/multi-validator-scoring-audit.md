# Multi-Validator Scoring Audit

**Date:** 2026-04-07
**Scope:** Read-only investigation of task verification and settlement architecture
**Codebase:** AetherNet protocol at commit `9318f5e`

---

## 1. Current Architecture for Task Verification

### The complete path from submission to settlement

| Step | Component | File:Line | What happens |
|------|-----------|-----------|--------------|
| 1 | API handler | `api/server.go:1629` | Worker POSTs evidence body + result content |
| 2 | BlobStore | `api/server.go:1694` | Evidence blob stored locally (content-addressed) |
| 3 | Publisher | `api/server.go:1715` | TaskSubmitted DAG event emitted (contains blob hash, not content) |
| 4 | Fast Path | `network/` | TaskSubmitted event propagates to all peers |
| 5 | Recognition | `recognition/task_consumer.go` | Each node's TaskManager applies TaskSubmitted state |
| 6 | BlobStore sync | `tasks/tasks.go:1209` | Remote nodes fetch evidence blob via `fetchEvidenceBlob` |
| 7 | **Scoring** | `autovalidator/auto.go:407` | **ONE** node's autovalidator scores the evidence |
| 8 | settleTask | `autovalidator/auto.go:770` | Scorer calls `ApproveTask` (local status → Completed) |
| 9 | TaskSettlement | `autovalidator/auto.go:804` | TaskSettlement DAG event created with score and escrow inputs |
| 10 | OCS pending | `ocs/engine.go:578` | TaskSettlement enters OCS pending queue on each node |
| 11 | **Voting** | `autovalidator/auto.go:1018` | Each node's autovalidator votes on the TaskSettlement — **auto-approves** |
| 12 | BFT consensus | `ocs/engine.go:351-367` | VotingRound accumulates votes; supermajority triggers finalization |
| 13 | Settlement event | `cmd/node/main.go:1578` | Finalization handler creates canonical Settlement DAG event |
| 14 | Applicator | `settlement/applicator.go:188` | `applyTaskSettlement` calls task settler function |
| 15 | Escrow release | `cmd/node/main.go:1546` | `ReleaseNet` distributes funds: worker, validator, treasury |

---

## 2. Do Multiple Validators Independently Score the Same Submission?

**No.** Only one validator scores each task submission. Here is why:

### The scoring gate

`processSubmittedTasks` at `auto.go:407-565` searches for tasks in `TaskStatusSubmitted`. The first autovalidator to process the task calls `settleTask` at line 565, which calls `ApproveTask` at line 772:

```go
// auto.go:772
if err := av.taskMgr.ApproveTask(task.ID, av.validatorID); err != nil {
    return
}
```

`ApproveTask` at `tasks.go:922-938` changes the task status from `Submitted` to `Completed`. Once this happens locally, no other invocation of `processSubmittedTasks` on the same node will find the task (it's no longer in `TaskStatusSubmitted`).

### Cross-node race window

On remote nodes, the task status change propagates via the **TaskSettlement DAG event**, not a TaskApproved event. The syncHandler does NOT have a case for `EventTypeTaskSettlement` that would change task status. This means:

- **Node A** (first to score): Scores the task, calls `ApproveTask` (local status → Completed), emits TaskSettlement event.
- **Nodes B-E**: Task remains in `TaskStatusSubmitted` until one of them also calls `processSubmittedTasks`. If evidence is ready and the staleness threshold has passed, **they will also score and settle the same task**, producing **additional independent TaskSettlement events**.

However, in practice this race is limited by:
1. The `taskStaleness` threshold (typically 5 seconds) — scoring doesn't happen immediately
2. Node A's TaskSettlement event propagates quickly, entering OCS pending on Nodes B-E. The OCS event doesn't change task status, but the Settlement event that follows consensus DOES (via the task settler function at `main.go:1517`)

**Net effect**: On the current 5-node testnet, typically 1-2 nodes score the task before the Settlement event propagates and is applied. The escrow manager's `ReleaseNet` at `main.go:1546` returns an error on the second attempt (escrow already released), preventing double-spend. But this is a safety net, not a design guarantee.

---

## 3. Are Scores Aggregated?

**No.** There is no score aggregation across validators. The architecture is:

1. One validator scores the evidence and decides pass/fail
2. That validator creates a TaskSettlement event carrying its score (`ScoreBP` field at `settlement/verdict.go:73`)
3. The TaskSettlement event enters BFT consensus
4. Other validators vote on the TaskSettlement — **but they do not independently score the evidence**
5. The vote at `auto.go:1018-1021` is:

```go
func (av *AutoValidator) verifyTaskSettlement(item *ocs.PendingItem) (bool, uint64, string) {
    // TaskSettlement events are created by the autovalidator itself after
    // evidence scoring. They carry the already-computed score. Approve them.
    return true, item.Amount, ""
}
```

This is an unconditional approval. Every validator rubber-stamps the TaskSettlement. The BFT consensus on the TaskSettlement event is purely a liveness/propagation check — it confirms that enough validators have *seen* the event, not that they *agree with the score*.

### What this means

The scoring verdict for any task is determined by exactly **one** validator — whichever node's autovalidator processes the task first. The other 4 validators contribute only liveness votes, not independent quality assessments. The BFT consensus on TaskSettlement is not a multi-validator quality consensus. It is a confirmation that the settlement event has propagated.

---

## 4. Exact Path from Submission to Settlement (or Rejection)

### Approval path

```
Worker submits evidence (API POST /v1/tasks/{id}/submit)
    → BlobStore stores evidence body locally
    → TaskSubmitted DAG event emitted + propagated
    → [wait taskStaleness + evidence readiness]
    → AutoValidator.processSubmittedTasks()
        → verifyEvidence() scores with category-specific verifier
        → score.Overall >= threshold?
            YES → settleTask()
                → ApproveTask() changes status → Completed (local)
                → Creates TaskSettlement DAG event
                → TaskSettlement enters OCS pending on all nodes
                → Each node's autovalidator votes YES (unconditionally)
                → BFT supermajority reached
                → Settlement event created
                → SettlementApplicator.applyTaskSettlement()
                → taskSettlerFn → escrowMgr.ReleaseNet()
                → Funds distributed: worker, validator, treasury
```

### Rejection path

```
Worker submits evidence
    → [same steps until scoring]
    → score.Overall < threshold?
        YES → RejectSubmission()
            → Task reopened (status → Open) for another agent
            → If max rejections reached → escrow refunded to poster
            → Reputation penalty for worker
            → NO DAG event emitted for rejection
            → NO BFT consensus involved
```

### Key asymmetry

Approval goes through BFT consensus (TaskSettlement → votes → Settlement). Rejection does NOT — it is a local state mutation by a single autovalidator with no consensus, no DAG record, and no cross-node propagation of the rejection decision. Other nodes learn about the reopening only if the task state change propagates via some other mechanism.

---

## 5. Single-Validator vs Compound Verification

### Current state: effectively single-validator

The current design is **effectively single-validator** for task quality assessment:

- **Scoring**: One validator scores. No aggregation.
- **BFT on TaskSettlement**: Rubber-stamp votes (unconditional approval). Not a quality check.
- **Rejection**: Single-validator decision with no consensus and no DAG record.
- **Dispute**: Poster can dispute, but resolution is again single-validator (`processDisputedTasks` at `auto.go:625`).

### Where the design deviates from compound verification

The CLAUDE.md states: "Compound verification requires structural independence — each verification is structurally independent of the previous one." The current task verification does not satisfy this:

1. There is only one verification, not multiple
2. BFT votes on TaskSettlement are not independent verifications — they are unconditional approvals
3. No validator emits a scored VerificationVote or TaskVerification event with its own quality assessment
4. The `ScoreBP` in the TaskSettlement payload is the scoring validator's score, not a consensus score

### Contrast with Transfer/Generation events

Transfer and Generation events have genuine multi-validator verification: each autovalidator independently evaluates the event via `verifyTransfer` and `verifyGeneration` at `auto.go:991-1016`. These contain structural checks (amount > 0, no self-transfer, evidence hash present). The verdict is independently determined and voted on.

For TaskSettlement, the equivalent function (`verifyTaskSettlement` at `auto.go:1018`) skips all checks and returns `true` unconditionally.

---

## 6. Infrastructure That Exists but Is Not Wired for Multi-Validator Scoring

### Available infrastructure

| Component | Location | What it does | Could it enable compound verification? |
|-----------|----------|--------------|---------------------------------------|
| VerificationVote event type | `event/event.go:66` | Validator emits scored verdict on a pending event | Yes — if validators emitted VerificationVotes with scores for TaskSubmitted events |
| VotingRound with weighted tally | `consensus/voting.go:449` | Aggregates stake-weighted votes and determines supermajority | Yes — already handles multi-validator verdict aggregation |
| OCS pending + deadline sweep | `ocs/engine.go` | Tracks pending events and enforces 30s expiry | Yes — could track TaskSubmitted events as OCS-pending for scoring consensus |
| VerifierRegistry with per-category scorers | `evidence/registry.go` | Routes scoring to category-specific verifiers | Yes — each validator could independently invoke this |
| Evidence.ResolveContent() | `evidence/evidence.go:52` | Shared content resolution for scoring | Yes — ensures all validators score the same content |
| Replay coordinator | `replay/` | Selects tasks for secondary asynchronous verification | Partially — adds a second verification layer but not concurrent |
| Canary evaluation | `autovalidator/auto.go:472` | Compares worker output to ground truth for calibration | Partially — measures scorer accuracy but doesn't feed into consensus |

### The gap

The scoring pipeline (evidence verifiers, content quality analysis) runs locally on one node. The consensus pipeline (OCS, VotingRound, BFT) runs across all nodes. These two pipelines are not connected for task scoring:

- The scoring pipeline produces a score and a pass/fail verdict
- The verdict triggers a TaskSettlement event
- The consensus pipeline votes on the TaskSettlement event
- But the consensus pipeline does NOT run the scoring pipeline — it unconditionally approves

To enable compound verification, the scoring pipeline would need to run independently on each validator before any of them creates a TaskSettlement event. The consensus layer would then aggregate multiple independently-computed scores to determine the verdict and final score.

### What does NOT exist

- No `EventTypeTaskVerification` or per-validator scored verdict event for task quality
- No mechanism for validators to emit independent scores and aggregate them before settlement
- No consensus round that operates on TaskSubmitted events directly (they are not OCS-pending)
- No rejection DAG event — rejections are local-only state mutations with no cross-node consensus

---

## Summary

| Question | Answer |
|----------|--------|
| Does each validator independently score? | No — one validator scores, others rubber-stamp |
| Are scores aggregated? | No — single validator's score becomes the settlement score |
| What event types are involved? | TaskSubmitted → TaskSettlement → VerificationVote → Settlement |
| Is there a TaskVerification event? | No — it does not exist |
| How does BFT relate to scoring? | BFT confirms TaskSettlement propagation, not quality agreement |
| Can a single validator reject/censor? | Yes — rejection is a local-only decision with no consensus |
| Is the current design compound verification? | No — it is single-validator verification with BFT propagation confirmation |
