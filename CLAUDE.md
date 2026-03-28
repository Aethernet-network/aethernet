# AetherNet — Project Conventions

## Architecture

AetherNet is a three-layer protocol. Never cross layer boundaries in imports.

- **Core Protocol** (internal/crypto, event, dag, ledger, ocs, identity, staking, genesis, fees, escrow, wallet, consensus, validator): Canonical state, finality, and economic security. Cannot import Coordination or Application packages.
- **Coordination Layer** (internal/registry, discovery, router, reputation, network): Network intelligence for routing and scheduling. May import Core Protocol. Cannot import Application packages.
- **Application Layer** (internal/tasks, platform, autovalidator, demo, evidence, verification, replay, canary, assurance): Product behavior and user-facing logic. May import Core Protocol and Coordination.
- **Infrastructure** (internal/store, metrics, ratelimit, eventbus, config, cloudmap): Shared utilities. Any layer may import these.

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
- **emitDAGEvent applies locally before broadcasting.** After dag.Add, ApplyDAGEvent runs synchronously on the local node so the API response reflects the new state. Peers apply asynchronously when they receive the event.
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
- **New files live in internal/network/.** Do not bloat into node.go.

## Trajectory Layer

- **TrajectoryCommit is a standard event.Event.** Uses EventTypeTrajectoryCommit, flows through normal DAG/signing/Fast Path.
- **Checkpoint blobs are NOT Fast Path EventBody.** The lean TrajectoryCommitPayload is the event payload. Large CheckpointBody is stored in blobstore (internal/blobstore/), fetched by trajectory service, invisible to network layer.
- **PrimaryTips() filters trajectory commits from default parent selection.** Non-trajectory event emission uses PrimaryTips(). Falls back to Tips() when all tips are trajectories.
- **Only the task claimer can emit trajectory commits.** Enforced at service/API layer.
- **Evidence packets anchor exploration via ExplorationRoot (Merkle) and ExplorationSample.**
- **Trajectory service wired in cmd/node/main.go.** BlobStore at {data_dir}/blobs, 4MB max.

## Supply Invariant

- **FundAgent creates tokens from nothing.** It must ONLY be called during genesis and onboarding. Never in fee collection, settlement, or slashing paths.
- **Fee collection uses TransferFromBucket or CollectFeeFromRecipient.** Never FundAgent.
- **Slashing uses TransferFromBucket("staking-pool", treasury, amount).** Never FundAgent.
- **After every settlement, sum(all balances) must equal genesis total.** The supply_test.go tests verify this.

## Security

- **Auth is ON by default.** requireAuth = true. Use --no-auth flag explicitly for testnet.
- **Vote identity comes from handshake, not payload.** Never trust VoterID or PublicKey from wire messages without verifying against the identity registry.
- **All P2P messages must be signed.** Unsigned votes and events from remote peers are dropped.
- **from_agent is never read from request body.** The authenticated node identity is the economic sender.

## Consensus

- **VotingRound state must be persisted.** Every RegisterVote writes to BadgerDB. On restart, pending votes are reloaded.
- **MinParticipants is configurable.** Single-node testnet uses 1. Multi-node must use 3+.
- **Clock skew tolerance: 60 seconds.** Votes older than VoteMaxAge are dropped.

## L1/L2 Boundary

- **L1 (Core Protocol) owns:** canonical DAG events, consensus, settlement finality, ledger mutations, token economics, staking/slashing, escrow balances.
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
- **INFO: startup, shutdown, configuration, milestone events.**
- **DEBUG: per-request, per-event, per-vote details.**

## Testing

- **Every change must pass: go test -p 1 ./... -race -count=1**
- **New features require tests.** No exceptions.
- **Test the failure path, not just the happy path.** If a function handles errors, test what happens when the error occurs.
- **Supply invariant test must pass after any settlement-related change.**

## Change Discipline

- **Run tests before and after every change.** Record pass count at start, verify same or higher at end.
- **Never swallow store write errors.** Every store.Put*, store.Delete*, store.Get* call must have its error checked and logged at slog.Error with full context. No exceptions.
- **One concern per prompt.** Do not refactor adjacent code that was not requested. If you notice an issue in unrelated code, note it in the response but do not fix it.
- **Preserve all existing behavior unless explicitly told to change it.** A refactor that changes behavior is a bug, not an improvement.
- **Persist before mutating in-memory state.** If the persist fails, abort the in-memory change.
- **New interfaces must have at least one test.** New public types and methods require tests.
- **Do not remove or rename existing public APIs** unless the prompt explicitly requests it. Other code may depend on them.
- **When adding a new package, check imports don't violate layer boundaries.** Core Protocol cannot import Coordination or Application packages. Coordination cannot import Application packages.

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
- **Clean deploy requires wiping /data/aethernet on all nodes (rm -rf, not just rm -rf *)**
- **Restart without wipe preserves state.** DAG replay uses topological sort (Kahn's algorithm) to load events in causal order from BadgerDB. Events with unresolvable parents are logged and skipped. Normal restarts should never require a data wipe.
- **Shared testnet API key: aethernet-testnet-arena-key-v1 (registered on all nodes at boot)**
- **--no-auth in Dockerfile CMD for testnet backward compatibility**
- **Never add AETHERNET_RESET to task definitions — it wipes the store**
- **Blob store directory:** {AETHERNET_DATA}/blobs (created automatically)
- **Grant settlement requires consensus.** Onboarding grants go through OCS → autovalidator → consensus vote → settlement. Not instant.
