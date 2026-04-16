# Historical Divergent Tasks — Pre-F3-B-Fix

This document annotates the three tasks that exhibited cross-node ledger divergence on the AetherNet testnet prior to the F3-B settlement-consensus-integrity fix. These tasks are part of the historical record. No canonical state has been modified; the annotation is forward-only.

## Context

Between 2026-04-11 and 2026-04-15, three tasks settled on the multi-validator pipeline with different ledger state on different testnet nodes. The verdicts were correct; the cross-node application of those verdicts diverged. The root cause — dual-path delivery of canonical settlement events through both the local-publish and recognition-fabric paths, with an inadequate `round.IsTerminal()` idempotency guard — was identified by the settlement-divergence investigation audit (`docs/audits/2026-04-15-settlement-divergence-investigation.md`) and closed by the F3-B fix workstream (locked design at `docs/plans/2026-04-15-settlement-consensus-integrity-fix.md`).

The cross-node consistency audit (`docs/audits/2026-04-15-poster-fee-cross-node-consistency.md`) provided the per-node balance evidence for all three tasks.

## The three tasks

### Task `a2b588c8b207aa68288020a10b440a67`

- **First observed**: 2026-04-15T15:48:24Z (settlement log on Node 1)
- **Verdict**: fail (reject)
- **Budget**: 500,000,000 µAET
- **Divergence**: Node 1 applied the reject settlement and drained its escrow bucket to 0. Nodes 2–5 never drained (escrow bucket remained at 500,000,000 µAET). Pattern: 1-alone-applied vs 4-did-not.
- **Audit reference**: `docs/audits/2026-04-15-poster-fee-cross-node-consistency.md`, per-task escrow bucket table row `a2b588c8…`.

### Task `b2f96181104a8911783cd2600cc630e4`

- **First observed**: 2026-04-15T16:14:57Z (settlement log on Node 1)
- **Verdict**: pass (accept)
- **Budget**: 500,000,000 µAET
- **Divergence**: Node 1 retained 500,000,000 µAET in the escrow bucket post-settlement (consistent with the F1 double-debit creating a 2× escrow, then settlement draining 1×). Nodes 3, 4, 5 drained to 0. Nodes 2 retained 500,000,000 µAET. Multi-layered divergence: both the pre-settlement escrow state and the settlement-application path differed across nodes.
- **Audit reference**: `docs/audits/2026-04-15-poster-fee-cross-node-consistency.md`, per-task escrow bucket table row `b2f96181…`.

### Task `52c5b97a555f8d83dbcee9751ea73d62` (2026-04-11 first-observed accept verdict)

- **First observed**: 2026-04-11T17:52:30Z (commit `dcc7c17`)
- **Verdict**: pass / accept_supermajority
- **Budget**: 100,000 µAET
- **Divergence**: Escrow bucket balance: 200,000 µAET on Nodes 1 and 4, 100,000 µAET on Nodes 2, 3, 5. The `esc:` metadata and synthetic `txf:bucket:` entries were present only on Nodes 1 and 4; Nodes 2, 3, 5 had neither. The F1 synthetic duplicate fired on 2 of 5 nodes.
- **Audit reference**: `docs/audits/2026-04-15-poster-fee-cross-node-consistency.md`, per-task escrow bucket table row `52c5b97a…`. Also `docs/lessons.md` entry "2026-04-11 first-observed accept verdict — verification status."

## Forward correction

The F3-B fix workstream closes the underlying mechanism through six parts:
- **Part E**: `RegisterEscrow` replaces the combined fund-and-register `Hold`, eliminating the F1 double-debit.
- **Part C**: `CanonicalEventDispatcher` provides exactly-once admission per (event, consumer) pair, eliminating the dual-path settlement-application race.
- **Part D**: Causal prerequisite gating ensures settlement fires only after all causally required predecessor events are projected locally.
- **Parts A+B**: Startup wiring for `LoadApplied` and `LoadFromStore` closes the restart-and-replay state loss.
- **Commit 9**: Wires `TaskVerificationConsensusConsumer` through the dispatcher as the first real consumer.

Every future settlement verification must assert cross-node ledger convergence per success criterion 6 of the workstream's §10 end-to-end verification.
