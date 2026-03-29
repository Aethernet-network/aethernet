# AetherNet Architecture

## Overview

AetherNet is a sovereign settlement protocol for verifiable AI work. It provides the incentive, evaluation, and settlement infrastructure for delegated AI computation — a layer between AI agents and the economic outcomes of their work.

**Key numbers:** ~91K lines of Go across 275 files, 54 packages, 1,500+ tests, all passing.

## Layer Model

```
                    Application Layer (L3)
              tasks, autovalidator, evidence, replay,
              canary, assurance, verification, platform

                   Coordination Layer (L2)
              network, registry, discovery, router, reputation

                    Core Protocol (L1)
              event, dag, ledger, ocs, consensus, settlement,
              crypto, identity, staking, genesis, fees, escrow,
              wallet, validator, validatorlifecycle

                      Infrastructure
              store, metrics, ratelimit, eventbus, config,
              cloudmap, localpub
```

Import rules are strict: L1 cannot import L2 or L3. L2 cannot import L3. Infrastructure is importable by any layer.

## DAG Structure

AetherNet uses a Directed Acyclic Graph (DAG), not a blockchain. Events — not blocks — are the atomic unit.

- Each event references specific prior events it has validated (CausalRefs)
- Content-addressed: EventID = SHA-256 of canonical fields
- Append-only: no deletion, no rewriting
- Events carry Ed25519 signatures verified on Add
- Lamport clock (CausalTimestamp) establishes partial order

This enables fine-grained parallelism. Multiple events can be created concurrently as long as they reference valid parents.

## Dual Ledger

Two ledgers track economically distinct operations:

- **Transfer Ledger:** Existing value moving between agents. Balance = sum(Settled inflows) - sum(Settled + Optimistic outflows).
- **Generation Ledger:** Net-new value created by AI computation. Verified claims backed by evidence.

Separation prevents inflation attacks — you cannot create value by relabeling a transfer as a generation.

## Event Publication

All locally-created events flow through `localpub.Publisher`:

```
event.New(...) → crypto.SignEvent(...) → publisher.Publish(ev)
                                              │
                                              ├─ dag.Add(ev)           [persist]
                                              ├─ SubmitLocalEvent(ev)  [Fast Path V2]
                                              └─ Broadcast(ev)         [legacy V1]
```

The publisher is the single authoritative path. The protocol client's DAG interface is `dagReader` (no Add method) — a compile-time guarantee against bypass. An enforcement test scans the repo for unauthorized dag.Add calls.

## Fast Path v1 Networking

Three-plane pre-materialization architecture:

1. **Causality plane:** Event headers relay before bodies — peers learn causal structure immediately
2. **Body plane:** Receiver-driven body fetch with commitment verification
3. **Repair plane:** Missing-parent resolution with bounded backfill

5-stage ingest pipeline: Announced → Completed → Validated → Materialized

6 concurrent workers (relay, completion, validation, materialization, repair, backpressure) process events asynchronously. dag.Add retains full enforcement as defense-in-depth.

## Validator Lifecycle

Validators hold seats with a 7-state lifecycle managed by a deterministic Reducer:

```
PendingJoin → Probationary → Active ←→ Suspended
                                ↓           ↓
                           CoolingDown → Exited
                                ↓
                            (re-entry)

Any non-terminal → Excluded (via slashing, terminal)
```

- State is derived from DAG events via `validatorlifecycle.Reducer`
- Immutable snapshots with SHA-256 digests for cross-node verification
- EffectiveFromVersion prevents retroactive eligibility changes
- Committee selection via SHA-256 sortition (min=3, max=21 members)
- Key rotation preserves seat identity; old key authorizes transition
- Genesis manifest with fail-closed startup checks

## Consensus

Reputation-weighted virtual voting with snapshot-bound eligibility:

1. Event enters OCS pending queue via `engine.Submit`
2. Auto-validator votes via `ProcessVote` → `processVoteInternal`
3. `VotingRound.RegisterVote` accumulates weighted votes
4. Supermajority (2/3 weight) triggers finalization
5. `SetFinalizationHandler` callback creates Settlement event
6. `settlementApp.Apply` mutates canonical ledger state

Vote eligibility is checked against the validator-seat snapshot, not the identity registry. Committee membership (from snapshot) gates which validators can vote for each round.

## Settlement

Settlement is triggered by the finalization-owning path — inevitable once consensus finalizes:

```
processVoteInternal detects supermajority
    → ProcessResult (clear pending, metrics only)
    → onFinalized callback
        → settlementApp.IsApplied? (idempotency guard)
        → Build SettlementPayload with attestations
        → pub.Publish(settlementEvent) → dag.Add + broadcast
        → settlementApp.Apply(payload)
            → transfer.RecordFromSync → creates ledger entry
            → transfer.Settle → transitions to SettlementSettled
            → Balance formula counts Settled inflows
```

The settlement applicator is the ONLY component that mutates canonical ledger state.

## Trajectory Layer

Agents emit `TrajectoryCommit` events to checkpoint their work:

- Lean payload in the DAG (task_id, outcome, checkpoint_hash, quality_score)
- Large checkpoint body stored in the blobstore (`{data_dir}/blobs`)
- Evidence packets anchor exploration via Merkle root
- PrimaryTips() filters trajectory commits from default parent selection

This captures the "negative knowledge" of AI work — dead ends, pivots, and explorations that inform verification.

## Authentication

AETHERNET-TX-V1 transaction signing:

- JCS-canonicalized JSON envelope: version, chain_id, actor, method, path, body_sha256, nonce, timestamps
- Ed25519 signature over canonical bytes
- TxID = SHA-256(sign_bytes) for replay protection
- Self-registration: actor field IS the hex-encoded public key
- Per-agent rate limiting on signed requests

## Key Packages

| Package | Purpose |
|---------|---------|
| `internal/event/` | Event types, canonical serialization, ComputeID |
| `internal/dag/` | Append-only causal DAG with strict enforcement |
| `internal/ocs/` | Optimistic Capability Settlement engine |
| `internal/consensus/` | VotingRound with snapshot-bound eligibility |
| `internal/validatorlifecycle/` | 7-state validator seat lifecycle, deterministic Reducer |
| `internal/network/` | Fast Path v1 three-plane networking |
| `internal/trajectory/` | TrajectoryCommit service, BlobStore |
| `internal/localpub/` | Authoritative local event publication |
| `internal/settlement/` | Settlement applicator (sole ledger mutator) |
| `internal/auth/` | AETHERNET-TX-V1 signing, verification |
| `internal/crypto/` | Ed25519 signing, bech32 addresses |
| `internal/identity/` | Agent identity registry |
| `internal/staking/` | Stake management with trust multipliers |
| `internal/ledger/` | Dual ledger (Transfer + Generation) |
| `pkg/sdk/` | Go SDK client (HTTP, no internal deps) |
| `sdk/python/` | Python SDK + agent scripts |
| `cmd/node/` | Node binary |
| `cmd/aet-e2e/` | Live-cluster E2E verification harness |
| `cmd/aet/` | CLI tool (wallet, balance, tasks) |
