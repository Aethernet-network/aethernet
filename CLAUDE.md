# AetherNet — Project Guide

## Project Overview

AetherNet is a distributed incentive and settlement protocol for verified AI work. Written in Go. ~91K lines across 47 internal packages. Live on a 3-node testnet at testnet.aethernet.network. Full E2E verified: registration → consensus → settlement → cross-node balance convergence. Python SDK published to PyPI as `aethernet-sdk`.

## Build & Test

```bash
go build -o bin/aethernet ./cmd/node/        # Node binary
go build -o bin/aet ./cmd/aet/               # CLI tool
go test -p 1 ./... -race -count=1            # All 1,500+ tests (42 packages)
AETHERNET_E2E_TIMEOUT=60s go run ./cmd/aet-e2e  # Live testnet E2E verification
docker buildx build --platform linux/amd64 -t aethernet .  # Docker image
```

## Architecture Layers

Three-layer import hierarchy. Never cross boundaries.

- **Core Protocol (L1):** `event`, `dag`, `crypto`, `ledger`, `identity`, `staking`, `escrow`, `fees`, `genesis`, `wallet`, `ocs`, `consensus`, `validator`, `validatorlifecycle` — cannot import L2 or L3
- **Coordination (L2):** `router`, `reputation`, `discovery`, `registry`, `network` — may import L1
- **Application (L3):** `tasks`, `platform`, `autovalidator`, `evidence`, `verification`, `replay`, `canary`, `assurance`, `api` — may import L1 and L2
- **Infrastructure:** `store`, `metrics`, `ratelimit`, `eventbus`, `config`, `cloudmap`, `auth`, `localpub`, `settlement`, `trajectory`, `blobstore` — importable by all layers

## Key Invariants

These must never be violated:

- **localpub.Publisher** is the ONLY path for locally-created DAG events. Zero raw `dag.Add` calls in production code. The protocol client holds a `dagReader` interface (no Add method) — compile-time enforcement. `internal/localpub/enforcement_test.go` scans the entire repo and fails CI if unauthorized `dag.Add` calls appear.
- **SetFinalizationHandler** on the OCS engine is the authoritative settlement creation path. Settlement is triggered directly by the finalization-owning path, not a later observer.
- **Consensus votes authenticate against validator-seat snapshots**, NOT the identity registry. The identity registry is a backward-compatibility fallback only.
- **TrajectoryCommit checkpoint blobs** live in BlobStore (`internal/blobstore/`), NOT in Fast Path EventBody. Lean payloads flow through DAG.
- **All write API operations** require AETHERNET-TX-V1 Ed25519 signatures.
- **Startup ordering:** `av.Start()` must be called AFTER `SetFinalizationHandler`, `SetSyncHandler`, and `SetVoteHandler` are wired. If it starts before, the auto-validator can vote and trigger finalization while `onFinalized` is nil.
- **Genesis manifest** must contain actual persistent node public keys (not symbolic constants). Load via `AETHERNET_VALIDATOR_MANIFEST` env var. Without it, `SingleNodeManifest(agentID)` auto-generates from the node's keypair (single-node dev mode).
- **Persist before mutating in-memory state.** Write to BadgerDB first. If persistence fails, abort.
- **The SettlementApplicator** is the ONLY component that mutates canonical ledger state.
- **The protocol client** (`internal/protocol/client.go`) is the ONLY interface for application-layer token movement.

## Subsystems

- **Event Publication** (`internal/localpub/`): `Publisher.Publish(ev)` → dag.Add → SubmitLocalEvent (Fast Path V2) → Broadcast (V1). Enforcement test scans repo for bypass.
- **Auth** (`internal/auth/`): AETHERNET-TX-V1 signing. JCS canonicalization (RFC 8785). TxID replay protection. Rate limiting. Self-registration: actor IS hex-encoded Ed25519 public key.
- **Fast Path** (`internal/network/`): Three-plane networking — causality (headers), body (payloads), repair (gaps). 5-stage ingest: Announced → Completed → Validated → Materialized. 6 concurrent workers. V2 negotiation with V1 fallback. Production observability: `fastpath:` prefix logs at every stage.
- **Trajectory** (`internal/trajectory/`, `internal/blobstore/`): TrajectoryCommit events capture exploration paths. PrimaryTips() filters trajectory commits from parent selection. Evidence anchoring via ExplorationRoot Merkle.
- **Validator Lifecycle** (`internal/validatorlifecycle/`): ValidatorSeat with 7-state lifecycle. Deterministic Reducer with immutable snapshots. EffectiveFromVersion prevents retroactive round corruption. Committee selection via SHA-256 sortition (min=3, max=21). Key rotation, slashing, cooldown enforcement. 8 DAG event types.
- **Settlement** (`internal/settlement/`): SettlementApplicator — sole ledger mutator. Apply → RecordFromSync → Settle. Idempotent via IsApplied.
- **OCS** (`internal/ocs/`): Optimistic Capability Settlement engine. Submit → pending → verification → ProcessResult (metrics) + onFinalized (settlement). 30s deadline sweep.
- **Consensus** (`internal/consensus/`): Reputation-weighted virtual voting. Snapshot-bound VotingRound. ValidatorSetVersion captured at round-open time. SupermajorityThreshold 0.667.
- **DAG** (`internal/dag/`): Append-only causal DAG. Tip tracking. Topological sort (Kahn's algorithm) for replay. Content-addressed EventID = SHA-256.
- **Tasks** (`internal/tasks/`): Full lifecycle: Post → Claim → Submit → Approve/Dispute/Cancel. Subtask decomposition. Escrow lock on post, release on approve.
- **Ledger** (`internal/ledger/`): Dual ledger — TransferLedger (value transfer) + GenerationLedger (new value creation). Balance = Settled inflows - (Settled + Optimistic) outflows.
- **Crypto** (`internal/crypto/`): Ed25519 key generation, signing, verification. Scrypt-encrypted key storage.

## Key Files

```
cmd/node/main.go                    — Node binary, all subsystem wiring
cmd/aet-e2e/main.go                 — Live-cluster E2E verification harness
cmd/aet/                            — CLI wallet (wallet, balance, stake, tasks)
internal/api/server.go              — HTTP REST API (67 routes)
internal/event/event.go             — All event types and payloads
internal/dag/dag.go                 — Append-only causal DAG
internal/ocs/engine.go              — OCS engine + SetFinalizationHandler
internal/consensus/voting.go        — Snapshot-bound VotingRound
internal/validatorlifecycle/        — Validator seat lifecycle (8 files)
internal/network/                   — Fast Path v1 (12 files)
internal/localpub/publisher.go      — Authoritative local event publication
internal/auth/transaction.go        — TX-V1 signing envelope
internal/settlement/applicator.go   — Settlement applicator
sdk/python/                         — Python SDK (PyPI: aethernet-sdk)
scripts/deploy-testnet.sh           — Testnet deployment
scripts/generate-validator-manifest.sh — Collect node keys + generate manifest
```

## Error Handling

- **NEVER use `_ =` for store writes.** Check every PutTransfer, PutStakeMeta, PutMeta error.
- **NEVER swallow ledger operation errors.** TransferFromBucket, FundAgent, BalanceCheck — propagate or log at ERROR.
- **Return early on failure.** If step 1 fails, do not proceed to step 2.

## Logging

- Use `slog`, never `log.Printf`. Structured logging only.
- ERROR: data loss, state corruption, failed persistence.
- WARN: degraded operation, retryable failures.
- INFO: startup, shutdown, milestone events, pipeline stage transitions.
- DEBUG: per-request, per-event details.
- Fast Path logs use `fastpath:` prefix with event_id, type, stage, source_peer, latency_ms.

## Testing

- Every change must pass: `go test -p 1 ./... -race -count=1`
- New features require tests. No exceptions.
- Test failure paths, not just happy paths.
- Supply invariant test must pass after any settlement change.
- Enforcement test prevents dag.Add bypass: `TestEnforcement_NoUnauthorizedDAGAdd`.
- E2E settlement chain: `TestE2E_GrantSettlesWithBalance` verifies grant → vote → settlement → balance > 0.

## Change Discipline

- Run tests before and after every change.
- One concern per prompt. Do not refactor adjacent code.
- Preserve existing behavior unless explicitly told to change it.
- New event-emitting code must use `localpub.Publisher`. Never call `dag.Add` directly.
- New interfaces must have at least one test.
- Check import boundaries when adding packages.

## Deployment

```
Testnet: testnet.aethernet.network
ECR: 435998721364.dkr.ecr.us-east-1.amazonaws.com/aethernet
Nodes: 3x m7i.xlarge EC2 in us-east-1 (44.200.60.102, 3.87.68.158, 100.27.227.231)
ALB: port 8338 (API), P2P: port 8337
```

- **AETHERNET_TESTNET=true** enables faucet, auto-genesis, shared API key.
- **AETHERNET_CONSENSUS_MIN_PARTICIPANTS=2** for 3-node testnet.
- **AETHERNET_VALIDATOR_MANIFEST=/path/to/manifest.json** loads shared genesis manifest.
- **AETHERNET_RESET=true** wipes DB but preserves keys at `{data_dir}/node_keys/`.
- **Persistent keys** at `/data/aethernet/node_keys/identity.json`. Do NOT wipe on redeploy.
- **Generate manifest:** `./scripts/generate-validator-manifest.sh`
- **Verify validator set:** `aethernet validator-set --manifest path.json --verify <digest>`
- **E2E verify after deploy:** `AETHERNET_E2E_TIMEOUT=60s go run ./cmd/aet-e2e`

```bash
# Build and push
aws ecr get-login-password --region us-east-1 | docker login --username AWS --password-stdin 435998721364.dkr.ecr.us-east-1.amazonaws.com
docker buildx build --no-cache --platform linux/amd64 -t 435998721364.dkr.ecr.us-east-1.amazonaws.com/aethernet:latest --push .

# Force deploy all 3 services
for svc in aethernet-node aethernet-node2 aethernet-node3; do
  aws ecs update-service --cluster aethernet-testnet --service $svc --force-new-deployment --region us-east-1 --no-cli-pager > /dev/null
done
```

## Supply Invariant

- `FundAgent` creates tokens from nothing — ONLY during genesis and onboarding.
- Fee collection uses `TransferFromBucket` or `CollectFeeFromRecipient`. Never FundAgent.
- After every settlement, `sum(all balances) == genesis total`.
