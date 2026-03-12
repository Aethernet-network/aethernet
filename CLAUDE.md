# AetherNet — Project Conventions

## Architecture

AetherNet is a three-layer protocol. Never cross layer boundaries in imports.

- **L1 Protocol** (internal/crypto, event, dag, ledger, ocs, identity, staking, genesis, fees, escrow, wallet): Core chain primitives. No L2 or L3 imports.
- **L2 Network** (internal/registry, discovery, router, reputation, network, validator, consensus): Coordination layer. May import L1. Never imports L3.
- **L3 Applications** (internal/tasks, platform, autovalidator, demo, evidence): Services built on the protocol. May import L1 and L2.
- **Infrastructure** (internal/store, metrics, ratelimit, eventbus, config): Shared utilities. Any layer may import these.

## Error Handling

- **NEVER use _ = for store writes.** Every PutTransfer, PutStakeMeta, PutMeta, and PutGeneration call must have its error checked. If the function signature prevents returning the error, log at slog.Error level with full context.
- **NEVER swallow errors from ledger operations.** TransferFromBucket, FundAgent, BalanceCheck — all must propagate errors or log at ERROR.
- **Return early on failure.** If step 1 of a multi-step operation fails, do not proceed to step 2.

## State Mutation Rules

- **Persist BEFORE updating in-memory state.** Write to BadgerDB first, then update the in-memory map/counter. If persistence fails, abort with no in-memory change.
- **Ledger credit BEFORE memory debit.** In Unstake, credit the agent's balance in the ledger before decrementing sm.stakes. In Stake, debit the balance before incrementing.
- **Idempotent retries.** Any operation that modifies multiple entities must track which sub-operations completed. Use flags (WorkerPaid, ValidatorPaid) not implicit ordering.

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
- **When adding a new package, check imports don't violate layer boundaries.** L1 cannot import L2 or L3. L2 cannot import L3.

## Deployment

- **Docker image: 435998721364.dkr.ecr.us-east-1.amazonaws.com/aethernet:latest**
- **3 nodes: aethernet-node, aethernet-node2, aethernet-node3**
- **AETHERNET_TESTNET=true in Dockerfile for testnet activity generator**
- **--no-auth in Dockerfile CMD for testnet backward compatibility**
- **Never add AETHERNET_RESET to task definitions — it wipes the store**
