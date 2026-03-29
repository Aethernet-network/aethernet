# AetherNet — Project Conventions

## Architecture

AetherNet is a three-layer protocol. Never cross layer boundaries in imports.

- **Core Protocol** (internal/crypto, event, dag, ledger, ocs, identity, staking, genesis, fees, escrow, wallet, consensus, validator, validatorlifecycle): Canonical state, finality, and economic security. Cannot import Coordination or Application packages.
- **Coordination Layer** (internal/registry, discovery, router, reputation, network): Network intelligence for routing and scheduling. May import Core Protocol. Cannot import Application packages.
- **Application Layer** (internal/tasks, platform, autovalidator, demo, evidence, verification, replay, canary, assurance): Product behavior and user-facing logic. May import Core Protocol and Coordination.
- **Infrastructure** (internal/store, metrics, ratelimit, eventbus, config, cloudmap, localpub): Shared utilities. Any layer may import these.

## Event Publication

- **All locally-created DAG events go through `localpub.Publisher`.** The publisher guarantees the three-step sequence: dag.Add → SubmitLocalEvent (Fast Path V2) → Broadcast (legacy V1). No other code path may create locally-authored events.
- **Zero raw `dag.Add` calls remain in `cmd/node/main.go`.** The protocol client's DAG interface is `dagReader` (Tips + Get only, no Add method). A compile-time guarantee prevents the protocol client from calling dag.Add directly.
- **Enforcement test prevents bypass.** `internal/localpub/enforcement_test.go` scans the entire repository for unauthorized dag.Add calls and fails CI if any are found outside the allowlist.
- **Allowlist for legitimate dag.Add:** DAG internals (`internal/dag/dag.go`), network sync/repair/materialize (`internal/network/`), and the publisher itself (`internal/localpub/publisher.go`).
- **Inbound/synced/replayed events must NOT use the publisher.** Those go through dag.Add directly in the sync handler. The publisher is for locally-authored events only.
- **The publisher is created early with nil disseminator** (before engine.Start). Startup events (genesis funding, registration) are persisted to DAG but not broadcast. After node.Start(), `pub.SetDisseminator(node)` wires the network. `broadcastLocalEvents` handles deferred dissemination of pre-networking events after peer connections are established.

## Error Handling

- **NEVER use _ = for store writes.** Every PutTransfer, PutStakeMeta, PutMeta, and PutGeneration call must have its error checked. If the function signature prevents returning the error, log at slog.Error level with full context.
- **NEVER swallow errors from ledger operations.** TransferFromBucket, FundAgent, BalanceCheck — all must propagate errors or log at ERROR.
- **Return early on failure.** If step 1 of a multi-step operation fails, do not proceed to step 2.

## State Mutation Rules

- **Persist BEFORE updating in-memory state.** Write to BadgerDB first, then update the in-memory map/counter. If persistence fails, abort with no in-memory change.
- **Ledger credit BEFORE memory debit.** In Unstake, credit the agent's balance in the ledger before decrementing sm.stakes. In Stake, debit the balance before incrementing.
- **Idempotent retries.** Any operation that modifies multiple entities must track which sub-operations completed. Use flags (WorkerPaid, ValidatorPaid) not implicit ordering.

## Canonical State — No Local-Only Mutations

- **ALL economically meaningful token movement must go through canonical protocol events.** No direct TransferFromBucket calls from application code.
- **The SettlementApplicator is the ONLY component that mutates ledger state** in response to consensus-finalized events. No other code path may write to the transfer or generation ledger.
- **The protocol client (internal/protocol/client.go) is the ONLY interface** that application-layer and marketplace code may use for token movement. Methods: SubmitTransfer, SubmitEscrowLock, SubmitEscrowRelease, SubmitRefund, SubmitGrant.
- **Transfer events carry Reason and TaskID metadata.** Reason classifies the transfer: "transfer", "escrow-lock", "escrow-release", "task-refund", "faucet-grant", "onboarding-grant", "node-bootstrap", "stake-lock", "stake-unlock". Protocol-internal reasons are exempt from fee collection.
- **Protocol-internal transfers are fee-exempt.** Staking, escrow, faucet, onboarding, and genesis operations are protocol mechanics, not taxable economic transactions.
- **Genesis-level operations (EventTypeGenesisFunding, EventTypeRegistration) are applied deterministically** on every node without consensus. They are protocol state, not economic transactions.

## Event-Sourced Task State

- **The DAG is the single source of truth for task state.** All task state transitions (post, claim, submit, approve, dispute, cancel) flow through DAG events.
- **API handlers validate, then emit.** The handler checks preconditions (auth, router assignment, status), and if valid, emits a DAG event. It does NOT call state-mutating methods directly.
- **TaskManager.ApplyDAGEvent is the ONLY path that mutates task state.** Both local and peer-originated DAG events are applied through the same method. This guarantees all nodes converge on identical task state.
- **Validate methods are read-only.** ValidateClaimTask, ValidateSubmitResult, ValidateApproveTask, ValidateDisputeTask check preconditions without mutation. They are called by API handlers before emitting DAG events.
- **Apply methods are idempotent.** applyTaskClaimed, applyTaskSubmitted, etc. check event-sourcing invariants (correct status transition) and skip gracefully if already applied. No API-level business logic (router checks, self-claim) — that was enforced at the API boundary.
- **emitDAGEvent publishes through the authoritative publisher.** After publication, ApplyDAGEvent runs synchronously on the local node so the API response reflects the new state. Peers apply asynchronously when they receive the event.
- **Never add a new direct task mutation path.** If a new task state transition is needed, add: (1) a Validate method, (2) an apply method called from ApplyDAGEvent, (3) an API handler that validates then emits. No exceptions.

## Causal Genesis Infrastructure

- **Evidence packets carry causal ancestry.** The `GenesisChain` field lists parent evidence hashes that an output derived from. When Agent B consumes Agent A's verified output, B's evidence must reference A's evidence hash.
- **Genesis chain must never be lossy.** Every verified input consumed must appear. Dropping links destroys causal provenance permanently.
- **Evidence packets are self-contained trust primitives.** Each packet carries producer and validator Ed25519 signatures over AETHERNET-EVIDENCE-V1 canonical serialization. A single packet can prove its own provenance without DAG access.
- **ComputeVerificationDepth is analytics, not core protocol.** It lives in `internal/analytics/` and uses a cached recursive traversal with cycle detection. It does not modify state.
- **Evidence signatures are separate from DAG event signatures.** The DAG signature proves event authenticity within the DAG. The evidence signature proves evidence authenticity anywhere — portable beyond AetherNet.

## Fast Path v1 Networking

- **Three-plane architecture:** causality (headers), body (payloads), repair (missing events). Relay happens before validation.
- **5-stage ingest pipeline:** Announced → Completed → Validated → Materialized. Events relay at Announced state.
- **dag.Add retains full enforcement.** Fast Path validation is advisory pre-screening; dag.Add re-checks independently (defense-in-depth).
- **Peer scoring is advisory.** 8 score events, sorted selection, unusable peer exclusion. Does not affect consensus or settlement.
- **Backpressure:** per-peer quotas, node-level overload detection, low-priority work shed before high-priority.
- **Checkpoint bootstrap:** deterministic state hash, bounded backfill for long-disconnected nodes.
- **Rolling upgrade safe.** V1 peers continue full-event sync. V2 peers use Fast Path. Both coexist.
- **6 concurrent workers:** relay, completion, validation, materialization, repair, backpressure.
- **Production observability:** Info-level structured logs at every pipeline stage (fastpath: header admitted, body completed, validation passed, event materialized) with event_id, type, source_peer, latency_ms, and reason codes for failures.
- **New files live in internal/network/.** Do not bloat into node.go.

## Trajectory Layer

- **TrajectoryCommit is a standard event.Event.** Uses EventTypeTrajectoryCommit, flows through normal DAG/signing/Fast Path.
- **Checkpoint blobs are NOT Fast Path EventBody.** The lean TrajectoryCommitPayload is the event payload. Large CheckpointBody is stored in blobstore (internal/blobstore/), fetched by trajectory service, invisible to network layer.
- **PrimaryTips() filters trajectory commits from default parent selection.** Non-trajectory event emission uses PrimaryTips(). Falls back to Tips() when all tips are trajectories.
- **Only the task claimer can emit trajectory commits.** Enforced at service/API layer.
- **Evidence packets anchor exploration via ExplorationRoot (Merkle) and ExplorationSample.**
- **Trajectory service wired in cmd/node/main.go.** BlobStore at {data_dir}/blobs, 4MB max.

## Validator Lifecycle

- **ValidatorSeat is the canonical unit of consensus participation.** Seats have a stable ValidatorID derived from the join event, persist across key rotations, and carry a 7-state lifecycle: PendingJoin → Probationary → Active ←→ Suspended → CoolingDown → Exited (re-entry allowed). Excluded is terminal.
- **All seat state is deterministically derived from DAG events via the Reducer.** No wall-clock time or randomness. Two nodes processing the same lifecycle events produce identical snapshots with identical digests.
- **ValidatorSnapshot is immutable.** Taken via `Reducer.Snapshot()`, decoupled from subsequent mutations. Consensus rounds bind to a snapshot version. Seats only participate if `EffectiveFromVersion ≤ snapshot.Version`.
- **Committee selection uses SHA-256 sortition.** `SelectBoundedCommittee` with `DefaultCommitteePolicy()` (min=3, max=21). For ≤21 validators, all seats participate. For larger sets, deterministic sortition bounds the committee per-round.
- **Key rotation preserves seat identity.** Old key authorizes transition. New key proves possession. Key history is tracked for historical signature verification.
- **Slashing atomically reduces stake and changes status.** Non-permanent slashes suspend with cooldown. Permanent slashes exclude (terminal). Cooldown enforcement gates reinstatement.
- **Resume returns to Probationary, not Active.** The seat must re-earn Active through probation.
- **Genesis manifest loaded from AETHERNET_VALIDATOR_MANIFEST env var** or auto-generated from the node's own keypair (single-node dev mode via `SingleNodeManifest`). Fail-closed startup: node exits if manifest is invalid or reducer cannot be seeded.
- **8 DAG event types for lifecycle transitions:** ValidatorGenesisSet, ValidatorJoin, ValidatorActivate, ValidatorSuspend, ValidatorResume, ValidatorExit, ValidatorKeyRotate, ValidatorSlashApplied.

## Consensus

- **VotingRound reads vote eligibility and weight from validator-seat snapshots, NOT identity registry alone.** When `SetValidatorSet(snapshot)` is called, `computeWeight` uses the snapshot's seat weight. The identity registry is a backward-compatibility fallback only.
- **Committee membership gates voting.** When `SetCommitteeSource` is wired, only committee members can vote for a round. Out-of-committee votes are rejected with ErrVoterNotInCommittee.
- **VoteRecord captures ValidatorSetVersion at round-open time.** All votes for an event are evaluated against the snapshot that was active when the round opened, not the latest runtime view.
- **VotingRound state must be persisted.** Every RegisterVote writes to BadgerDB. On restart, pending votes are reloaded.
- **MinParticipants is configurable.** Single-node testnet uses 1. Multi-node must use 2+.
- **Clock skew tolerance: 60 seconds.** Votes older than VoteMaxAge are dropped.

## Settlement

- **SetFinalizationHandler on the OCS engine is the authoritative settlement creation path.** When `processVoteInternal` detects supermajority, it calls `onFinalized` which creates the Settlement DAG event via `pub.Publish` and calls `settlementApp.Apply`. Settlement is inevitable once consensus finalizes — it does not depend on a later sync handler observation.
- **ProcessResult is metrics only.** It removes the event from the OCS pending queue and updates counters. It does NOT create Settlement events or call the applicator.
- **Settlement applicator (internal/settlement/applicator.go) is the ONLY component that mutates canonical ledger state.** `Apply` → `applyTransfer` → `RecordFromSync` (creates ledger entry) → `Settle` (transitions to SettlementSettled).
- **Settlement is idempotent.** `IsApplied(targetID)` prevents double-settlement.
- **Startup ordering is critical.** The auto-validator must start AFTER SetFinalizationHandler is wired. If it starts before, it can vote and trigger finalization while onFinalized is nil.

## Network Vote Admission

- **SetVoteAdmission replaces identity-registry-only gate.** The admission callback checks the lifecycle snapshot first, falls back to the identity registry for agents not in the snapshot. Snapshot takes precedence — a voter in the registry but not in the snapshot is rejected.
- **Structured rejection reasons:** "seat not in snapshot", "key mismatch", "seat not eligible".

## Supply Invariant

- **FundAgent creates tokens from nothing.** It must ONLY be called during genesis and onboarding. Never in fee collection, settlement, or slashing paths.
- **Fee collection uses TransferFromBucket or CollectFeeFromRecipient.** Never FundAgent.
- **Slashing uses TransferFromBucket("staking-pool", treasury, amount).** Never FundAgent.
- **After every settlement, sum(all balances) must equal genesis total.** The supply_test.go tests verify this.

## Security

- **Auth is ON by default.** requireAuth = true. Use --no-auth flag explicitly for testnet.
- **AETHERNET-TX-V1 transaction signing** is the primary auth path. JCS-canonicalized JSON envelope with chain_id, actor, method, path, body_sha256, nonce, created_at, expires_at. Ed25519 signature. Self-registration: actor IS the hex-encoded public key.
- **Vote identity comes from validator-seat snapshot, then handshake.** Never trust VoterID or PublicKey from wire messages without verifying against the snapshot.
- **All P2P messages must be signed.** Unsigned votes and events from remote peers are dropped.
- **from_agent is never read from request body.** The authenticated node identity is the economic sender.

## L1/L2 Boundary

- **L1 (Core Protocol) owns:** canonical DAG events, consensus, settlement finality, ledger mutations, token economics, staking/slashing, escrow balances, validator lifecycle.
- **L2 (Application/Marketplace) owns:** task posting, routing, discovery, workflow orchestration, UI/UX, reputation overlays.
- **L2 interacts with L1 ONLY through the protocol client interface.** No direct imports of ledger, settlement, or ocs from marketplace code.
- **If it changes balances, escrow, fees, settlement, or economic accountability, it goes through the protocol path.** No exceptions.

## Configuration

- **All constants come from internal/config.** No hardcoded magic numbers in package code.
- **DefaultConfig() matches current behavior.** Changing a default is a protocol change and requires documentation.
- **Config is loaded from JSON file (--config) or environment variables (AETHERNET_*).**

## Logging

- **Use slog, never log.Printf.** All new code uses structured logging.
- **ERROR: data loss, state corruption, failed persistence.** These must be fixed.
- **WARN: degraded operation, retryable failures.** These should be investigated.
- **INFO: startup, shutdown, configuration, milestone events, pipeline stage transitions.**
- **DEBUG: per-request, per-event, per-vote details.**
- **Fast Path logs use "fastpath:" prefix** with structured fields: event_id, type, stage, source_peer, reason, latency_ms.

## Testing

- **Every change must pass: go test -p 1 ./... -race -count=1**
- **New features require tests.** No exceptions.
- **Test the failure path, not just the happy path.** If a function handles errors, test what happens when the error occurs.
- **Supply invariant test must pass after any settlement-related change.**
- **E2E settlement tests prove the complete chain.** `TestE2E_GrantSettlesWithBalance` verifies: grant → OCS → vote → finalization → settlement → balance > 0.
- **Enforcement test prevents dag.Add bypass.** `TestEnforcement_NoUnauthorizedDAGAdd` scans all non-test Go files.
- **Live-cluster verification:** `go run ./cmd/aet-e2e` runs 8 stages against the real testnet.

## Change Discipline

- **Run tests before and after every change.** Record pass count at start, verify same or higher at end.
- **Never swallow store write errors.** Every store.Put*, store.Delete*, store.Get* call must have its error checked and logged at slog.Error with full context. No exceptions.
- **One concern per prompt.** Do not refactor adjacent code that was not requested. If you notice an issue in unrelated code, note it in the response but do not fix it.
- **Preserve all existing behavior unless explicitly told to change it.** A refactor that changes behavior is a bug, not an improvement.
- **Persist before mutating in-memory state.** If the persist fails, abort the in-memory change.
- **New interfaces must have at least one test.** New public types and methods require tests.
- **Do not remove or rename existing public APIs** unless the prompt explicitly requests it. Other code may depend on them.
- **When adding a new package, check imports don't violate layer boundaries.** Core Protocol cannot import Coordination or Application packages. Coordination cannot import Application packages.
- **New event-emitting code must use localpub.Publisher.** Never call dag.Add directly for locally-authored events. The enforcement test will catch it.

## Deployment

- **Docker image: 435998721364.dkr.ecr.us-east-1.amazonaws.com/aethernet:latest**
- **3 testnet nodes across 3 AWS AZs (m7i.xlarge EC2 Nitro instances)**
  - Node 1: 44.200.60.102 (us-east-1a)
  - Node 2: 3.87.68.158 (us-east-1b)
  - Node 3: 100.27.227.231 (us-east-1c)
- **ALB: testnet.aethernet.network -> all 3 nodes on port 8338**
- **Explorer: testnet.aethernet.network/explorer**
- **Arena: aethernet-arena.vercel.app**
- **P2P port: 8337, API port: 8338**
- **AETHERNET_TESTNET=true enables:** faucet bucket, shared testnet API key, auto-genesis
- **AETHERNET_CONSENSUS_MIN_PARTICIPANTS=2 for 3-node testnet**
- **AETHERNET_VALIDATOR_MANIFEST=/path/to/manifest.json** loads a shared genesis validator manifest with actual node Ed25519 public keys. Without this, each node auto-generates a single-seat manifest from its own keypair (single-node dev mode).
- **Persistent validator keys at {AETHERNET_DATA}/node_keys/identity.json.** Keys survive AETHERNET_RESET (which only wipes the DB). Do NOT wipe node_keys/ on redeploy — keys must persist for validator seat matching.
- **Generate manifest:** `./scripts/generate-validator-manifest.sh` collects agent IDs from running nodes and produces a manifest file.
- **Verify validator set:** `aethernet validator-set --manifest path.json --verify <digest>`
- **Clean deploy:** AETHERNET_RESET=true wipes DB but preserves keys. Do NOT rm -rf /data/aethernet/node_keys/.
- **Restart without wipe preserves state.** DAG replay uses topological sort (Kahn's algorithm) to load events in causal order from BadgerDB. Events with unresolvable parents are logged and skipped. Normal restarts should never require a data wipe.
- **Shared testnet API key: aethernet-testnet-arena-key-v1 (registered on all nodes at boot)**
- **Blob store directory:** {AETHERNET_DATA}/blobs (created automatically)
- **Grant settlement requires consensus.** Onboarding grants go through OCS → autovalidator → consensus vote → finalization handler → settlement applicator → balance update. Not instant.
- **E2E verification after deploy:** `go run ./cmd/aet-e2e` runs 8 stages: reachability → peers → DAG → register → dissemination → consensus → balance → convergence.
