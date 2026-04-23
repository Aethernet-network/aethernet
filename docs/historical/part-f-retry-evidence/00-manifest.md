# Part F retry evidence — divergence at testnet wipe time

**Archived**: 2026-04-22, immediately before the 5-node testnet wipe-to-genesis.
**Reason for preservation**: this directory captures the cross-node ledger divergence state on the AWS testnet at the moment of the Part F Phase C-sanity abort. The frozen testnet state is being wiped to bootstrap a clean genesis for the next-workstream fix; the evidence here is the durable record that informs the architect-session response.

## What's here

- `00-manifest.md` — this file.
- `01-selection-race-characterization.md` — copy of `docs/plans/implementation/selection-race-characterization.md` at archive time. The bug-class characterization document.
- `02-multi-emit-bug-class-audit.md` — copy of `docs/plans/implementation/multi-emit-bug-class-audit.md` at archive time. The full audit of canonical event emission sites.
- `03-activation-event-ids.md` — the two activation event IDs emitted on this testnet:
  - `2a513ac6...` (Part F first-attempt; inert because Part E wiring gap)
  - `459a1fa143b6...` (Part F retry Phase D; correctly applied on all 5 nodes via Part E.1 admission router)
- `04-pre-activation-ledger-state-node{1..5}.json` — per-node `/v1/status` + `/v1/economics` snapshot at 19:10 UTC, captured before the activation emit.
- `05-post-csanity-ledger-state-node{1..5}.json` — per-node snapshot at 23:00 UTC, captured after the 3 sanity-task settlements that exposed the divergence.
- `06-phase-c-sanity-verdicts-per-node.txt` — log excerpts from each node's docker logs showing the divergent verdict on tasks `77c2bdf1` and `4ba459c6` (Node 2 reject, Nodes 1/3/4/5 accept).
- `07-phase-d-activation-response.json` — the admin endpoint's response from emitting the 459a1fa1 activation event.
- `08-cluster-divergence-summary.md` — quick-reference summary of the divergence shape and magnitude.

## Cluster state at archive time

- All 5 nodes running `integer-migration-part-e1-d6196ed` (image digest `sha256:67a9fa787ed6f6d5...`).
- Branch `feat/canonical-distribution-integer-migration` @ commit `d6196ed` (Part E.1) plus subsequent `c4fc190` (Part F first-attempt completion report) and Part F retry plan/characterization/audit docs.
- DAG size: 1108 events on every node (after Phase D + Phase C-sanity).
- Settler + gen-ledger flags: `shadowMode=false` on every node (correctly flipped by Phase D activation Apply on all 5).
- Treasury divergence: Nodes 1, 4, 5 at 100,000,030,096,000 µAET; Node 3 at +2,000 µAET; Node 2 at +204,000 µAET.
- Cross-node validator-stake ledger divergence (pre-existing 50 AET total across nodes; same selection-race mechanism is the leading hypothesis).

## Why the wipe

The bug found is a protocol-level consensus selection race that affects multiple HIGH-risk emission sites. Fixing it is out of scope for the integer-migration workstream; the architect session is designing a class-level fix in a new workstream. The testnet is wiped to genesis so the new workstream's binary can be exercised against clean state without ledger fossils from the divergent period.

The branch `feat/canonical-distribution-integer-migration` is NOT being merged. Integer-migration code stays on the branch for future re-verification once the selection-race fix lands.
