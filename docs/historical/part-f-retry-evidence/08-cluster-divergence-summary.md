# Cluster divergence summary at wipe time

Quick-reference summary of the divergence shape captured immediately before the testnet wipe.

## Treasury divergence (post-csanity)

| Node | treasury_balance (µAET) | Δ from Nodes 1, 4, 5 |
|---|---:|---:|
| 1 | 100,000,030,096,000 | baseline |
| 2 | 100,000,030,300,000 | **+204,000** |
| 3 | 100,000,030,098,000 | +2,000 (pre-existing fossil) |
| 4 | 100,000,030,096,000 | baseline |
| 5 | 100,000,030,096,000 | baseline |

## Per-task verdict divergence (Phase C-sanity)

| Task | Node 1 | Node 2 | Node 3 | Node 4 | Node 5 |
|---|---|---|---|---|---|
| `d0c0bd21...` (sanity_1, 100K µAET) | accept | accept | accept | accept | accept |
| `77c2bdf1...` (sanity_2, 100K µAET) | accept | **REJECT** | accept | accept | accept |
| `4ba459c6...` (sanity_3, 10M µAET) | accept | **REJECT** | accept | accept | accept |

All 3 tasks reported `agreeing_validators=4` on every node — protocol thinks consensus reached, but consensus *verdict* differs on Node 2.

## TVConsensus event multi-emit per round

| Task | N1 emits | N2 emits | N3 emits | N4 emits | N5 emits | Total in DAG |
|---|---:|---:|---:|---:|---:|---:|
| `d0c0bd21...` | 1 | 0 | 1 | 0 | 1 | 3 |
| `77c2bdf1...` | 1 | 1 | 1 | 1 | 2 | 6 |
| `4ba459c6...` | 1 | 2 | 0 | 2 | 2 | 7 |

When emit count > number of distinct verdicts that converged, multiple validators emitted with conflicting verdicts. The race fires when the DAG ends up containing both "accept" and "reject" TVConsensus events for the same RoundID.

## Cluster total ledger sum (cross-node conservation)

| Node | Σ all known agent balances (µAET) | Δ from Node 1 |
|---|---:|---:|
| 1 | 550,800,000,000,000 | baseline |
| 2 | 550,825,000,000,000 | +25,000,000,000 (25 AET) |
| 3 | 550,825,000,000,000 | +25,000,000,000 (25 AET) |
| 4 | 550,850,000,000,000 | +50,000,000,000 (50 AET) |
| 5 | 550,800,000,000,000 | 0 |

Cross-node conservation broken. Total ledger value differs by up to 50 AET cluster-wide. The 25 AET unit fits the genesis validator stake amount, suggesting the same selection-race mechanism applied historically to staking-related events (Divergence A in the characterization document).

## Per-validator stake-state divergence (Divergence A — pre-existing)

| Validator | N1 | N2 Δ | N3 Δ | N4 Δ | N5 Δ |
|---|---:|---:|---:|---:|---:|
| v1 (`d839e1`) | 25,006,597,462 | +25,000,189,668 | +24,999,998,000 | +25,000,003,832 | 0 |
| v3 (`05adbeb`) | 50,008,393,378 | −24,999,848,334 | −25,000,000,000 | +5,750 | 0 |
| v5 (`5df098c`) | 25,004,273,482 | +25,000,029,666 | +25,000,000,000 | +25,000,001,916 | 0 |

3 of 5 validators show 25-AET-unit divergences across nodes. Mechanism (per characterization §6): same selection race applied to the Settlement event-emit path (Settlement events are emitted independently by every node's OCS finalization handler, see `cmd/node/main.go:1717-1792`).

## DAG state agreement

| Property | Cluster-uniform? |
|---|---|
| `dag_size` (count of events) | YES (1108 on every node) |
| `ocs_pending` (queue depth) | YES (0 on every node) |
| `total_supply` | YES |
| `circulating_supply` | YES |
| `treasury_balance` | NO (3 distinct values) |
| Per-validator ledger balance | NO (multiple validators show divergent values per node) |
| Per-task settlement verdict | NO (Node 2 reject vs Nodes 1/3/4/5 accept on 2 of 3 tasks) |

The DAG is byte-identical across nodes. The protocol disagreement is entirely in *which* DAG events each node selected as canonical when multiple events for the same logical key exist.
