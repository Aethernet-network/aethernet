# Activation events emitted on the testnet during Part F + Part F retry

## Event 1: 2a513ac6b17112b9bb75a0c46d5ce5ab0f16da12b5e24e02d52a66e4fac23761

- **Emitted**: 2026-04-22 18:02:49 UTC (Part F first-attempt Phase D).
- **Emitting agent**: 68778249080a8eb0ff20b359baf520ca2c2178ecdf3b1be7510d047be0bee35a (`part-f-operator` wallet).
- **Reason**: "Part F rehearsal 2026-04-22: 20-task shadow corpus, 178 shadow_delta lines across 5 nodes, 36/36 int_sum cross-node equivalent, 0 sum_delta violations, 0 max_per_recipient_delta observed"
- **Effect on first-attempt binary**: NONE. Part E wiring gap (no recognition→dispatcher admission adapter for `EventTypeIntegerMigrationActivation`) caused the consumer's Apply to never run. Event landed in DAG as inert data.
- **Effect on Part F retry binary (E.1)**: ALSO NONE. The Part E.1 admission router covers live commits but does not replay historical DAG events to newly-registered consumers (see `docs/plans/implementation/part-f-retry-plan.md` §5 + the SourceReplay/LoadFromStore architectural gap finding). Event remained inert across the rehearsal.
- **Status at wipe time**: present in DAG, semantically inert.

## Event 2: 459a1fa143b6ea11ccc43663462782a9bb886fcebe9a948dc4cbe742eb1ffe6b

- **Emitted**: 2026-04-22 20:36:14 UTC (Part F retry Phase D).
- **Emitting agent**: 68778249080a8eb0ff20b359baf520ca2c2178ecdf3b1be7510d047be0bee35a (`part-f-operator` wallet).
- **Reason**: "Part F retry 2026-04-22: live-testnet rehearsal of canonical settlement integer cutover. Pre-activation state shows 2000 uAET treasury divergence across 5 nodes (2+3 vs 1+4+5 partition), consistent with Part B's hypothesized float-path receive-order non-determinism, confirmed empirically after 2-3 hours of background settlement traffic."
- **Effect**: SUCCESS on all 5 nodes within ~16 seconds. Each node's recognition bus → admission router → dispatcher → IntegerMigrationActivationConsumer.Apply. Activation state persisted to BadgerDB; settler + gen-ledger flags flipped to integer-canonical mode (shadowMode=false).
- **Status at wipe time**: present in DAG; activation state persisted in meta-store on all 5 nodes; flags flipped.

## Significance

These two events together constitute **Evidence 2** (idempotency on fresh emit was not exercised because Sub-scenario A wasn't attainable, but the correctness of Part E.1's admission router and Part E's consumer Apply path was empirically verified in Phase D retry).

The selection-race bug-class is upstream of and orthogonal to the integer migration. The 459a1fa1 activation correctly flipped flags cluster-wide; subsequent settlements on the integer path then exhibited divergence due to the selection-race in the TVConsensus emit path, not in the integer arithmetic itself.
