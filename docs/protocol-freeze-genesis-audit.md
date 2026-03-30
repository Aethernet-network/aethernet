# Protocol Freeze — Phase 4 Audit: Genesis Format

**Date:** 2026-03-29
**Scope:** Complete inventory of genesis manifest, genesis events, initial balances, DAG replay, and fail-closed startup.
**Prerequisites:** Phase 1 (event format), Phase 2 (consensus rules), Phase 3 (settlement) are frozen.

---

## Task 1: Genesis Manifest

### Format

The manifest is a JSON file with a single `entries` array. Each entry describes one genesis validator seat.

**Struct:** `GenesisManifest` (`internal/validatorlifecycle/genesis.go:79-82`)

```json
{
  "entries": [
    {
      "validator_id": "seat-abc123...",
      "operator_agent_id": "<hex-encoded Ed25519 public key>",
      "consensus_public_key": "<hex-encoded Ed25519 public key>",
      "key_epoch": 1,
      "bonded_stake": 50000000000,
      "initial_status": "active",
      "reputation_baseline": 5000,
      "categories": []
    }
  ]
}
```

**Required fields per entry** (`genesis.go:97-125`):
- `validator_id` — stable seat identifier (string, non-empty)
- `operator_agent_id` — hex Ed25519 public key (string, non-empty)
- `consensus_public_key` — hex Ed25519 public key (string, non-empty, unique)
- `key_epoch` — must be > 0
- `bonded_stake` — must be > 0 (micro-AET)
- `initial_status` — one of: `"active"`, `"pending_join"`, `"probationary"`

**Optional fields:**
- `reputation_baseline` — initial reputation score (default 0)
- `categories` — task categories this validator handles

### Where Loaded From

**Environment variable:** `AETHERNET_VALIDATOR_MANIFEST` (`cmd/node/main.go:1058`)

**Load path:**
```
ENV AETHERNET_VALIDATOR_MANIFEST=/path/to/manifest.json
  → validatorlifecycle.LoadManifestFromFile(path)
    → os.ReadFile(path)
    → json.Unmarshal into GenesisManifest
    → manifest.Validate()
```

**Source:** `cmd/node/main.go:1058-1075`

### Validation on Load

`GenesisManifest.Validate()` (`genesis.go:90-128`) performs:
1. At least one entry required (`ErrManifestEmpty`)
2. No duplicate `validator_id` values (`ErrManifestDuplicateID`)
3. No duplicate `consensus_public_key` values (`ErrManifestDuplicateKey`)
4. All required fields non-empty (`ErrManifestMissingField`)
5. `bonded_stake > 0` (`ErrManifestInvalidStake`)
6. `key_epoch > 0` (`ErrManifestInvalidKeyEpoch`)
7. `initial_status` non-empty

After load, additional startup checks (`startup.go:52-84`):
- Reducer seeded with > 0 seats
- Reducer version > 0 (has been seeded)
- If `ExpectedDigest` is set: snapshot digest must match

### Missing Manifest Behavior

**Not fail-closed for testnet.** When `AETHERNET_VALIDATOR_MANIFEST` is not set:
- Falls back to `SingleNodeManifest(agentID)` (`genesis.go:155-173`)
- Creates a single Active seat using the node's own Ed25519 public key
- The VotingRound does NOT bind a snapshot — falls back to identity registry

**Source:** `cmd/node/main.go:1068-1073`

**For production** (`ProductionStartupCheck`): `RequireManifest: true` → node refuses to start without a manifest (`startup.go:114-118`). Currently unused — testnet uses `DefaultStartupCheck` which has `RequireManifest: false`.

### Malformed Manifest Behavior

**Fail-closed.** If `LoadManifestFromFile` fails (bad JSON, file not found), or `Validate()` fails:
```go
slog.Error("validator lifecycle: failed to load manifest file", ...)
os.Exit(1)
```
**Source:** `cmd/node/main.go:1063-1066`

No partial parsing — the entire manifest is rejected on any validation failure.

### Hot Reload

**No.** The manifest is loaded once at startup and used to seed the Reducer. The Reducer then processes lifecycle events from the DAG. There is no mechanism to reload the manifest at runtime. Any change requires a node restart.

---

## Task 2: Genesis Events

### Events Created During Genesis

Genesis creates **no DAG events** for initial token allocation. The `seedGenesis` function (`cmd/node/main.go:2409-2468`) calls `FundAgent` and `TransferFromBucket` directly on the transfer ledger. These are internal ledger writes, not DAG events.

DAG events created during startup (after genesis seeding):

| Event | Type | Source | Signed? |
|---|---|---|---|
| Genesis funding | `GenesisFunding` | `cmd/node/main.go:1195-1228` | Yes (node keypair) |
| Node registration | `Registration` | `cmd/node/main.go:1274-1285` | Yes (node keypair) |
| Validator genesis set | `ValidatorGenesisSet` | Not auto-created — manifest seeds the Reducer directly | N/A |

**Key finding:** The initial token allocation (`seedGenesis`) does NOT produce DAG events. The `GenesisFunding` event created later is for the node-agent bootstrap (200,000 AET from rewards bucket), NOT for the initial 6-bucket allocation. This means the genesis allocation is **not auditable from the DAG** — it's an opaque ledger operation.

### Are Genesis Events Deterministic?

**Partially.** The `seedGenesis` function is idempotent (checks for `genesisMarkerKey`) and uses fixed allocation constants from `internal/genesis/genesis.go`. However:

1. `FundAgent` generates IDs using `fundCounter.Add(1)` — a monotonic counter that starts at 0 on each startup. If genesis runs on different nodes at different times (or after restarts with different store states), the entry IDs will differ.

2. `TransferFromBucket` generates IDs using `bucketCounter.Add(1)` — same issue.

3. The `GenesisFunding` event (node bootstrap) has a deterministic payload but the EventID depends on JCS canonicalization of a payload that includes the node's AgentID — so each node creates a **different** genesis funding event (different AgentID → different EventID).

**Conclusion:** Genesis ledger entries are NOT deterministic across nodes. Each node independently seeds its own ledger. Consistency is achieved through DAG sync and settlement, not through identical genesis entries.

### Do Genesis Events Go Through localpub.Publisher?

**The `GenesisFunding` event does** (`cmd/node/main.go:1214`): `pub.Publish(gfEv)`.

**The initial 6-bucket allocation does NOT** — it's a direct `FundAgent` call (`cmd/node/main.go:2439`). There is no DAG event for the genesis token distribution.

### Are Genesis Events Signed?

The `GenesisFunding` and `Registration` events are signed by the node's keypair (`cmd/node/main.go:1213,1281`). Genesis events (empty CausalRefs) are allowed unsigned in `dag.Add` (`dag/dag.go:171-179`), but in practice they are signed.

---

## Task 3: Initial Balances

### Total Genesis Supply

**1,000,000,000 AET** (1 quadrillion micro-AET = 1,000,000,000,000,000 µAET). Source: `genesis/genesis.go:11`.

### Distribution

| Bucket | Amount (AET) | Percentage | Purpose |
|---|---|---|---|
| `genesis:founders` | 150B | 15% | Founder allocation |
| `genesis:investors` | 150B | 15% | Investor allocation |
| `genesis:ecosystem` | 300B | 30% | Grants, onboarding, growth |
| `genesis:rewards` | 200B | 20% | Validators and verifiers |
| `genesis:treasury` | 100B | 10% | Protocol governance |
| `genesis:public` | 100B | 10% | Public sale / airdrops |
| **Total** | **1,000B** | **100%** | |

**Post-genesis transfers from buckets:**
- `genesis:rewards` → `testnet-validator`: 200,000 AET (genesis validator funding)
- `genesis:ecosystem` → `genesis:faucet`: 10B AET (testnet faucet, only if `AETHERNET_TESTNET=true`)

**Source:** `cmd/node/main.go:2425-2468` (seedGenesis function).

### Auditable?

**No.** Initial balances are set via direct `FundAgent` calls, not DAG events. The 6-bucket allocation is invisible to the DAG. Only subsequent transfers (node bootstrap, onboarding grants, faucet grants, task escrow) produce DAG events.

### Can Balances Be Changed Without Changing the Manifest?

**Yes.** The genesis allocation is hardcoded in `genesis/genesis.go`, independent of the validator manifest. The manifest only controls which validators start the network, not how tokens are distributed. Changing initial balances requires a code change, not a manifest change.

---

## Task 4: DAG Replay from Genesis

### Peer Discovery

Two mechanisms (`cmd/node/main.go:2205-2244`):

1. **DNS-based discovery:** `--discover <dns-name>` or via Cloud Map. `network.NewPeerDiscovery` periodically resolves DNS A records and connects to discovered peers. 30-second refresh interval.

2. **Static peers:** `--peer <addr>` or `AETHERNET_PEER=<addr>` (comma-separated). Connected at startup.

### Full DAG Request

When a peer connects, the handshake includes `TipCount` (`network/peer.go:175`). If the peer has more events, the local node requests a sync batch. The sync handler (`network/node.go:1096-1119`) receives events sorted by CausalTimestamp and adds them to the DAG.

### Replay Process

**From store (restart):** `dag.LoadFromStore(s)` (`dag/dag.go:212-236`):
1. Load all persisted events from BadgerDB
2. Topological sort via Kahn's algorithm
3. Insert in parent-before-child order
4. Skip duplicate events and events with unresolvable parents

**From peers (new node):** `MsgSyncBatch` handler (`network/node.go:1096-1119`):
1. Sort batch by CausalTimestamp
2. Verify each event's Ed25519 signature (`crypto.VerifyEvent`)
3. Add to DAG (skips duplicates via `ErrDuplicateEvent`)
4. Call sync handler for downstream processing (settlement, lifecycle, etc.)

### Ledger State Guarantee

**Qualified yes.** After DAG replay:
- All Transfer events in the DAG are processed by the SettlementApplicator
- Settlement is idempotent (applied set prevents double-application)
- Topological order guarantees parent-before-child

**However:** The initial genesis allocation is done via `seedGenesis` (direct ledger writes), not DAG events. If `seedGenesis` produces different entry IDs on different nodes (due to atomic counter differences), the ledger entries themselves differ even though the balances are the same.

### Replay Performance

Estimated for the in-memory DAG with BadgerDB persistence:
- **1,000 events:** < 1 second (topological sort + insert is O(E + V))
- **100,000 events:** 2-10 seconds (dominated by BadgerDB read I/O)
- **1,000,000 events:** 30-120 seconds (depends on disk speed and event size)

Bottleneck is `AllEvents()` which reads all events from BadgerDB into memory.

### Interrupted Replay

**Safe.** Events are persisted to BadgerDB individually on first acceptance. `LoadFromStore` on restart reads all persisted events and reconstructs the DAG. `addFromStore` skips duplicates. The settlement `applied` set is also persisted, preventing double-application.

### Signature and EventID Verification During Replay

**From store (`addFromStore`):** No signature verification. Events were verified on first acceptance; replay trusts the store.

**From peers (`MsgSyncBatch`):** Yes, every event's signature is verified via `crypto.VerifyEvent` before `dag.Add`. Events with invalid signatures are dropped with a warning.

**EventID verification during replay:** Not explicitly. `dag.Add` and `addFromStore` trust the `e.ID` field. They do not recompute `ComputeID(e)` and compare. An attacker who corrupted the store could insert events with wrong IDs. However, signature verification (on peer sync) implicitly validates the content — a different EventID would mean different canonical bytes → signature mismatch.

---

## Task 5: Fail-Closed Startup

### Manifest Key Verification

**No check that the node's key is in the manifest.** The startup sequence loads the manifest and seeds the Reducer, but does not verify that the local node's keypair matches any seat in the manifest. A node whose key is not in the manifest will:
- Start successfully
- Be unable to vote (not in the validator snapshot)
- Be unable to finalize any consensus rounds
- Operate as a passive observer / API server

**Source:** `cmd/node/main.go:1058-1121` — no `agentID in manifest` check.

### Node Not in Validator Set

The node starts but cannot participate in consensus. The auto-validator's votes would be rejected with `ErrVoterNotInSnapshot` (`consensus/voting.go:365`). The node can still:
- Accept API requests
- Relay DAG events
- Serve balance queries
- Run the faucet (testnet)

### Manifest Digest Mismatch

**Production mode (`ProductionStartupCheck`):** `os.Exit(1)` if digest doesn't match. **Testnet mode (`DefaultStartupCheck`):** Digest check is disabled (`ExpectedDigest: ""`).

**Source:** `startup.go:71-81`

### Cross-Node Manifest Consistency Verification

**Partial.** The `ValidatorSnapshot.ComputeDigest()` method (`snapshot.go:49-62`) computes a deterministic SHA-256 hash of the canonical snapshot representation. This digest is logged at startup:

```go
validatorlifecycle.LogSnapshotDigest(lifecycleReducer)
```

Operators can compare digests across nodes manually. The `generate-validator-manifest.sh` script outputs the digest for verification.

**Automated verification:** When `ExpectedDigest` is set on `StartupCheck`, the node refuses to start if the digest doesn't match. This is available but not enabled for testnet.

**No runtime consensus on manifest:** Nodes do not exchange manifest digests during handshake or sync. A node with a different manifest would silently diverge.

---

## Task 6: Gap Analysis

### GAPS THAT BLOCK PROTOCOL FREEZE

**1. Genesis allocation is not in the DAG**

The 6-bucket allocation (`seedGenesis`) uses direct `FundAgent` calls — not DAG events. This means:
- The genesis allocation is not auditable from the DAG
- Two nodes that seed genesis independently produce different ledger entry IDs (atomic counters differ)
- There is no way for a new node to verify that an existing node's genesis allocation is correct
- A malicious operator could modify `genesis/genesis.go` to allocate more tokens to a specific bucket

**Recommendation:** Create a canonical `GenesisAllocation` DAG event type that records the 6-bucket distribution. This event should be the first event in the DAG (genesis root), signed by the genesis validator, and verified during DAG replay.

**2. No EventID verification during DAG replay from store**

`addFromStore` (`dag/dag.go:303-332`) does not recompute `ComputeID(e)` and compare against `e.ID`. A corrupted store could contain events with tampered content but original IDs. Signature verification would catch content tampering for signed events, but `addFromStore` skips signature verification.

**Recommendation:** Add EventID verification during store replay: `recomputed := ComputeID(e); if recomputed != e.ID { reject }`

**3. No runtime manifest consistency check between nodes**

Nodes do not exchange manifest digests during handshake. A node with a different manifest will:
- Produce a different validator snapshot
- Accept/reject different votes
- Potentially finalize different consensus outcomes
- Silently diverge from the network

**Recommendation:** Include the manifest/snapshot digest in the P2P handshake (`HandshakePayload`). Nodes with mismatched digests should refuse to sync.

**4. `FundAgent` entry IDs are non-deterministic**

`FundAgent` uses `fundCounter.Add(1)` for the sequence number in entry IDs (`transfer.go:413-414`). The counter starts at 0 on each process start. If two nodes call `FundAgent` in different orders (or after different restart histories), they generate different entry IDs for the same logical operation.

**Recommendation:** Use content-addressed IDs for genesis entries: `SHA-256(bucket_name + amount + "genesis")`. This makes genesis entries deterministic and idempotent.

**5. Genesis events (empty CausalRefs) skip signature verification**

`dag.Add` (`dag/dag.go:171-179`) allows unsigned events when `CausalRefs` is empty (genesis events). This is intentional for bootstrapping, but means:
- Any node can inject unsigned genesis events into the DAG
- There is no authentication on who created the genesis root events

**Recommendation:** Require genesis events to be signed by a key in the manifest. The manifest is the root of trust — only manifest keys should be able to create genesis events.

### RISKS FOR MAINNET

**1. Manifest distribution is manual**

The manifest must be copied to each node as a file and referenced via environment variable. There is no automated distribution mechanism. If a node starts with a stale or incorrect manifest, it silently diverges.

**2. No genesis-to-DAG audit trail**

Since initial allocations are direct ledger writes, there is no way to independently verify that a node's genesis state matches the protocol specification. A node operator could modify the genesis constants and no other node would detect it (until ledger divergence causes settlement failures).

**3. `seedGenesis` idempotency relies on balance checks**

The idempotency check (`cmd/node/main.go:2412-2422`) verifies treasury and ecosystem balances > 0. If a node is reset (data wiped but marker key survives, or balances wiped but marker survives), the check may pass incorrectly.

**4. No genesis event ordering guarantee for concurrent nodes**

If multiple nodes start simultaneously and each creates genesis events, the events may have different CausalTimestamps and different positions in the DAG. While this doesn't affect correctness (each event is independent), it makes the DAG non-deterministic across fresh deployments.

### THINGS ALREADY SOLID

**1. Manifest validation is comprehensive**

`Validate()` checks for empty entries, duplicate IDs, duplicate keys, missing fields, zero stake, and zero key epoch. Malformed manifests are rejected entirely — no partial parsing.

**2. Fail-closed on manifest load failure**

If `LoadManifestFromFile` fails, the node calls `os.Exit(1)`. No graceful degradation — the node refuses to start with a bad manifest.

**3. Snapshot digest mechanism exists**

`ComputeDigest()` produces a deterministic SHA-256 hash of the canonical snapshot. The digest can be verified across nodes and is available for production enforcement via `ProductionStartupCheck`.

**4. Startup checks prevent unconfigured consensus**

`ValidateReducer()` verifies the Reducer has seats, has been seeded (version > 0), and optionally matches the expected digest. The node will not start consensus without a valid Reducer.

**5. DAG replay from peers verifies signatures**

`MsgSyncBatch` handler verifies every event's Ed25519 signature before inserting into the DAG. Tampered events are dropped.

**6. Settlement idempotency survives replay**

The `applied` set in `SettlementApplicator` is persisted to BadgerDB and restored on startup. Replayed settlement events are silently skipped.

**7. Mint cap enforcement after genesis**

`SetMintCap(totalMinted)` is called after genesis completes (`cmd/node/main.go:2161-2163`). Any subsequent `FundAgent` call returns `ErrMintCapExceeded`. The cap is persisted and restored on restart.

**8. Topological sort guarantees causal replay order**

`LoadFromStore` uses Kahn's algorithm for topological sort, with `(CausalTimestamp, EventID)` tiebreaking for determinism. All nodes replay the same events in the same order.

---

## Summary of Action Items (Priority Order)

| # | Action | Severity | Effort |
|---|---|---|---|
| 1 | Create GenesisAllocation DAG event for auditable genesis | **Blocks freeze** | Medium |
| 2 | Add EventID verification during store replay | **Blocks freeze** | Low |
| 3 | Include snapshot digest in P2P handshake | **Blocks freeze** | Medium |
| 4 | Make FundAgent entry IDs content-addressed (deterministic) | **Blocks freeze** | Low |
| 5 | Require genesis events to be signed by manifest keys | **Blocks freeze** | Low |
| 6 | Enable ProductionStartupCheck for mainnet (require manifest + digest) | Pre-mainnet | Low |
| 7 | Add automated manifest distribution mechanism | Pre-mainnet | High |
| 8 | Verify node key is in manifest at startup (warn if passive-only) | Pre-mainnet | Low |
