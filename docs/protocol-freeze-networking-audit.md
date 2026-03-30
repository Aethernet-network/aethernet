# Protocol Freeze — Phase 5 Audit: Networking

**Date:** 2026-03-29
**Scope:** Complete inventory of wire protocol, Fast Path pipeline, relay behavior, event broadcast, peer management, and DoS protections.
**Prerequisites:** Phases 1-4 (event format, consensus, settlement, genesis) are frozen.

---

## Task 1: Wire Protocol

### Message Types

**V1 (Legacy) — `internal/network/peer.go:74-89`:**

| MsgType | Payload | Purpose |
|---|---|---|
| `handshake` | `HandshakePayload` | AgentID + version + challenge-response auth + manifest digest |
| `event` | Serialized `event.Event` | Single event for direct DAG insertion |
| `sync_request` | Empty | Request peer's full DAG |
| `sync_batch` | `SyncBatchPayload` | Batch of events in response to sync_request |
| `ping` | Empty | Keepalive probe |
| `pong` | Empty | Keepalive reply |
| `vote` | `VotePayload` | Validator vote for OCS consensus |

**V2 (Fast Path) — `internal/network/protocol.go:27-48`:**

| MsgType | Plane | Payload | Purpose |
|---|---|---|---|
| `v2_event_header` | Causality | `EventHeader` | Lean header for relay-before-validation |
| `v2_frontier` | Causality | `FrontierSummary` | DAG tips snapshot |
| `v2_window_digest` | Causality | `WindowDigest` | Timestamp-windowed event list |
| `v2_checkpoint` | Causality | `CheckpointSummary` | Bootstrap state snapshot |
| `v2_event_body` | Body | `EventBody` | Event payload (receiver-fetched) |
| `v2_body_request` | Body | `BodyRef` | Request body by EventID + commitment |
| `v2_repair_request` | Repair | `RepairRequest` | Request missing events (by ID or window) |
| `v2_repair_response` | Repair | `RepairResponse` | Batch of requested events |
| `v2_hello` | Control | `HelloV2` | V2 capability negotiation |
| `v2_peer_status` | Control | `PeerStatus` | Queue depth, health signal |
| `v2_ack` | Control | `AckHint` | Received event acknowledgment |
| `v2_nack_missing` | Control | `NackMissingParent` | Missing parent notification |
| `v2_overloaded` | Control | `Overloaded` | Backpressure signal |

### V1 vs V2 Protocol

**V1:** Full-event broadcast. `MsgEvent` sends the entire serialized `event.Event` to all peers. `MsgRequestSync` polls peers every `SyncInterval` for their full DAG. Simple but bandwidth-heavy at scale.

**V2 (Fast Path):** Three-plane architecture separating causality (headers), body (payloads), and repair (gaps). Headers are relayed immediately (before body or validation). Bodies are fetched on demand by the receiver. Gaps are repaired via targeted requests.

### V2 Negotiation

**Source:** `internal/network/legacy.go:22-60`, `internal/network/compat.go:124-141`

1. V1 handshake completes (both peers at `PeerConnected`)
2. Both sides call `NegotiateV2(peer)` — sends `HelloV2` with `ProtocolVersion=2`, `Features=["fast_path","body_split","repair"]`, `FrontierTips`
3. If peer responds with `HelloV2` within 5s timeout: `v2Negotiated=true`, capabilities recorded
4. If no response or incompatible version: peer stays V1 (`v2Negotiated=false`)
5. V2 messages are silently dropped for V1 peers via `SafeSend` (`compat.go:182`)

**Mixed topology:** V1 peers use `MsgRequestSync`/`MsgSyncBatch` (polling). V2 peers use Fast Path (header relay, body fetch, repair). Both coexist on the same node.

### Wire Format

JSON-encoded messages over TCP. Each message is a `Message` struct with `type` and `payload` fields. The payload is itself JSON-encoded. Per-message size limit: **4 MiB** enforced by `resetLimitReader` (`peer.go:99-126`). The limit resets after each successful decode (per-message, not cumulative).

---

## Task 2: Fast Path Pipeline

### Stage 1: Announced (Header Admission + Relay)

**Trigger:** `MsgEventHeader` received from peer → `ingest.AdmitHeader()` creates tracking entry at `StageAnnounced`.

**What is sent:** `EventHeader` — EventID, Type, CausalRefs, AgentID, CausalTimestamp, StakeAmount, BodyCommitment (SHA-256 hex), Signature. Lean — no payload body.

**Relay:** Immediately enqueued to `relayQ`. The `relayWorker` (`relay.go:25`) drains the queue and sends the header to a bounded set of V2 peers selected by `MeshManager.SelectRelayTargets()`:
- Target fanout: 6 peers (configurable, min 2, max 12)
- Excludes: origin peer, overloaded peers, peers with score < `MinUsableScore` (20)
- Diversity injection: 30% probability of replacing one slot with a random usable peer

**Validation at this stage:** None beyond JSON deserialization. This is relay-before-validation — the header is forwarded before the body is fetched or the signature is checked.

**Failure:** If `AdmitHeader` fails (tracking full at `MaxTracked=10000` or already tracked): header is dropped, no relay.

### Stage 2: Completed (Body Fetch)

**Trigger:** `completionWorker` (`completion.go:125`) drains `announceQ` and calls `maybeRequestBody()`.

**Body request:** `MsgBodyRequest` with `BodyRef{EventID, BodyCommitment}` sent to source peer or best-scored fallback. Receiver-driven — the node decides whether to fetch.

**Body delivery:** Peer responds with `MsgEventBody{EventID, Payload}`. The body commitment is verified: `SHA-256(received_payload) == header.BodyCommitment`.

**Size limit:** Bodies are subject to the 4 MiB per-message limit. No separate body-size limit.

**Failure:** Commitment mismatch → body rejected, peer score decremented by `ScoreInvalidBody=-20`. Missing body → entry expires at `AnnounceTTL=60s`.

### Stage 3: Validated (Signature + EventID Check)

**Trigger:** `validationWorker` (`validation.go:133`) drains `completeQ`.

**Validation performed (`ValidateEvent`, `validation.go:73-93`):**
1. **Signature:** Non-genesis events must have a valid Ed25519 signature. Genesis events (empty CausalRefs) are allowed unsigned in the Fast Path pre-screening. **Note:** `dag.Add` enforces signatures on ALL events (Phase 4, Fix 5), so unsigned genesis events would be caught at Stage 4.
2. **EventID:** `ReconstructEvent` (`validation.go:40-67`) rebuilds the full `event.Event` from header+body, computes `ComputeID()`, and compares against the header's EventID. Mismatch → rejected.
3. **Type-specific:** TrajectoryCommit events validated for payload fields and causal ref shape.

**Failure:** Invalid events are logged and NOT enqueued for materialization. Tracking entry stays at `StageCompleted` until TTL expiry.

### Stage 4: Materialized (DAG Insertion)

**Trigger:** `materializeWorker` (`materialize.go:24`) drains `validateQ`.

**Action:** Calls `dag.Add(e)` which enforces:
- No duplicate EventID
- All CausalRefs present in DAG
- Valid Ed25519 signature (ALL events, no genesis exception)

**On success:** `syncHandler` fired (routes event to OCS, settlement, task manager, etc.).

**On `ErrMissingCausalRef`:** Missing parent IDs stored in `tracking.MissingParents`. Event enqueued to `repairCh` for gap repair.

**Failure:** Duplicates silently skipped. Other errors logged, entry removed from tracking.

### Stage 5: Repair (Gap Detection)

**Trigger:** `materializeEvent` detects `ErrMissingCausalRef` → enqueues to `repairCh`.

**Request:** `repairWorker` (`repair.go:119`) sends `MsgRepairRequest` with up to `MaxRepairIDs=256` missing event IDs to the source peer or best-scored fallback.

**Response:** `MsgRepairResponse` with up to 256 events. Each event is signature-verified and added to DAG via `dag.Add`. `retryBlockedChildren` re-enqueues children whose parents are now resolved.

**Retry:** No automatic retry with backoff. If repair response doesn't fill all gaps, the child remains blocked until another event triggers the same parent repair (e.g., from another peer sync).

---

## Task 3: Relay Behavior

### Relay-Before-Validation

**Yes.** When a header is received (`MsgEventHeader`), it is relayed to other peers BEFORE the body is fetched and BEFORE validation. This is the core latency optimization — event awareness propagates at header speed, not body-fetch speed.

**Source:** `relay.go:1-11` (doc comment), `relayWorker` drains `relayQ` which is filled at `StageAnnounced`.

### Safeguards Against Invalid Header Flooding

1. **Per-peer quota:** `PeerQuota.AllowHeader()` limits headers per window (default 500 per 60s per peer). Exceeding quota silently drops headers. (`backpressure.go:32-156`)

2. **Bounded relay fanout:** `MeshManager` limits relay to `TargetFanout=6` peers (max 12), not all peers. This bounds amplification to O(fanout) per hop, not O(N). (`mesh.go:76`)

3. **Tracking deduplication:** `AdmitHeader` checks if the EventID is already tracked. Duplicate headers are dropped and the source peer's score is decremented (`ScoreDuplicateHeader=-1`).

4. **MaxTracked cap:** At most 10,000 events can be in the ingest pipeline simultaneously. Beyond that, new headers are dropped.

5. **TTL expiry:** Announced entries that don't complete body fetch within 60s are evicted by the GC goroutine (every 10s).

### TTL / Hop Limit

**No explicit hop counter or TTL on relayed messages.** The deduplication mechanism (tracked EventID set) prevents infinite relay loops — once a node has seen a header, it won't relay it again. But there is no hop limit that would prevent a header from traversing the entire network. For a network of diameter D with fanout F, a header propagates in O(D) rounds with O(F^D) total messages in the worst case.

### Amplification Risk

**Bounded by fanout and dedup.** An attacker sending one crafted header causes at most `TargetFanout` relays per node, and each relay is deduplicated at the recipient. Total amplification for a single header = O(N) where N is the number of nodes (each node relays once). This is linear, not exponential.

However, an attacker sending K unique headers can cause O(K × fanout) messages per node. The per-peer quota (500/60s) bounds K, so worst case is 500 × 6 = 3,000 relay messages per peer per minute.

---

## Task 4: Event Broadcast

### Publication Path

**Source:** `internal/localpub/publisher.go:111-129`

When `localpub.Publisher.Publish(ev)` is called:
1. `dag.Add(ev)` — persist in DAG (authoritative, must succeed)
2. `disseminator.SubmitLocalEvent(ev)` — enter Fast Path v2 pipeline (header relay)
3. `disseminator.Broadcast(ev)` — send full event to all peers via V1 `MsgEvent`

Steps 2 and 3 are **best-effort** — errors are logged but don't fail Publish.

### Delivery Scope

**All connected peers** receive the event via both paths:
- V2 peers: receive the header via relay (step 2), then fetch the body on demand
- V1 peers: receive the full event via `MsgEvent` broadcast (step 3)

### Delivery Guarantee

**Eventual delivery, not guaranteed immediate delivery.** If a peer is temporarily disconnected:
- The event is persisted in the local DAG
- When the peer reconnects, the periodic sync (`syncLoop`, V1) or checkpoint-based repair (V2) will deliver the event
- The repair mechanism detects missing parents and requests them

### Acknowledgment

**No ACK for V1 `MsgEvent`.** V2 has `MsgAckHint` but it is a hint for body-fetch optimization, not a delivery guarantee.

### Events Lost Between Creation and Broadcast

**Possible but recoverable.** If the node crashes after `dag.Add` (step 1) but before broadcast (steps 2-3), the event is persisted in the local store but peers don't have it. On restart, `broadcastLocalEvents` (`cmd/node/main.go`) re-broadcasts all local events to newly connected peers. Additionally, peer sync and repair will eventually pull the event.

---

## Task 5: Peer Management

### Discovery

Two mechanisms:

1. **Static peers:** `--peer <addr>` or `AETHERNET_PEER=<addr>` (comma-separated). Connected at startup. (`cmd/node/main.go:2227-2244`)

2. **DNS-based discovery:** `--discover <dns-name>` with `PeerDiscovery` (`discovery.go`). Resolves DNS A records every 30 seconds. New IPs are dialed automatically. Designed for AWS Cloud Map but works with any DNS service.

### Connection Establishment

**Connector side (`Connect`, `node.go:368-497`):**
1. Dial TCP
2. Self-connection guard: check if target IP is self
3. Send `HandshakePayload` with AgentID, Version, TipCount, Challenge (32 random bytes), PublicKey, ManifestDigest
4. Receive peer's HandshakePayload
5. AgentID self-connection guard (defense-in-depth)
6. Verify peer's challenge-response signature
7. Manifest digest comparison (Phase 4)
8. Send our challenge response
9. Register peer, start I/O loops
10. V2 negotiation: send `HelloV2`, wait for response

**Acceptor side (`handleIncomingConn`, `node.go:878-974`):** Mirror sequence.

### Connection Maintenance

**Keepalive:** `MsgPing`/`MsgPong` sent every `KeepAliveInterval` (default 30s). (`peer.go` keepalive goroutine)

**Read deadline:** Each message resets a read deadline. Default timeout: 90 seconds (3× keepalive). No response within deadline → connection closed.

### Disconnection Detection

- **Read timeout:** Decoder returns error after deadline expires → `readLoop` exits, peer marked `PeerDisconnected`, `disconnectReason="read_timeout"`
- **Write error:** Encoder returns error → `writeLoop` exits, peer closed
- **Remote close:** TCP RST/FIN detected by decoder → `disconnectReason="remote_closed"`

### Reconnection

**No automatic reconnection.** If a static peer disconnects, it is not automatically re-dialed. DNS-based discovery will re-discover the peer's IP on the next resolution cycle (30s) and re-connect.

### Maximum Peer Count

`NodeConfig.MaxPeers` (default varies by deployment). Enforced at accept time — incoming connections beyond the limit are rejected.

### Peer Banning

**No explicit ban mechanism.** Peers with low scores (`score < MinUsableScore=20`) are excluded from relay fanout and repair targeting, but the connection is not closed. A sufficiently misbehaving peer (e.g., sending many invalid signatures) will have their score driven below 20 and become effectively inert.

---

## Task 6: Resource Limits and DoS Protection

### Maximum Message Size

**4 MiB per message** (`peer.go:99-126`). Enforced by `resetLimitReader` which wraps the TCP connection. Limit resets after each successful decode — it's per-message, not cumulative.

### Maximum Event Size

No separate event size limit beyond the 4 MiB message envelope. A single event body approaching 4 MiB would consume the entire message budget.

### Rate Limiting on Inbound Messages

**Per-peer quotas** (`backpressure.go:32-156`, `PeerQuota`):
- Headers: 500 per 60-second window
- Bodies: 100 per 60-second window
- Repairs: 50 per 60-second window
- Concurrent body requests: max 10 pending
- Concurrent repair requests: max 5 pending

**Sync request rate limit:** 10-second minimum interval per peer (`peer.go:368-377`).

### Malformed Data Handling

- **JSON decode error:** Message dropped, no peer score impact
- **Invalid signature:** Event dropped, peer score decremented by `ScoreInvalidSig=-30`
- **Invalid body commitment:** Body dropped, peer score decremented by `ScoreInvalidBody=-20`
- **Oversized message:** `resetLimitReader` returns error → `readLoop` terminates → connection closed

Connection is NOT explicitly closed on malformed data (except oversized). The peer score system gradually degrades the peer's usefulness.

### Excessive Valid Data

- **IngestManager** caps tracked events at `MaxTracked=10000`. Beyond that, new headers are dropped.
- **Queue capacities:** `announceQ`, `relayQ`, `completeQ`, `validateQ` each capped at 4096. Full queues cause backpressure (events dropped at admission).
- **Backpressure signaling:** When queue depth exceeds 3000, node sends `MsgOverloaded` to peers, who deprioritize it in relay fanout.

### Memory/Goroutine Bounding

- **Fixed worker count:** 1 goroutine each for relay, completion, validation, materialization, repair, backpressure = 6 workers per node. Not proportional to peer count.
- **Queue-based:** All inter-stage communication via bounded channels. No unbounded goroutine spawning.
- **Peer goroutines:** 4 per peer (readLoop, writeLoop, keepalive, dispatcher). With MaxPeers=50, that's 200 goroutines — manageable.

---

## Task 7: Gap Analysis

### GAPS THAT BLOCK PROTOCOL FREEZE

**1. Fast Path validation still allows unsigned genesis events**

`ValidateEvent` (`validation.go:74-83`) has the old `isGenesis` exception that allows unsigned events with empty CausalRefs. While `dag.Add` (Phase 4, Fix 5) enforces signatures on ALL events, the pre-screening in the Fast Path should be consistent.

**Impact:** Low — `dag.Add` is the authoritative gate. But the inconsistency could confuse debugging and allows unsigned headers to consume pipeline resources before being rejected at Stage 4.

**Recommendation:** Remove the `isGenesis` exception from `ValidateEvent` to match `dag.Add`.

**2. No hop limit on header relay**

Headers are relayed without a hop counter. While deduplication prevents loops, there is no mechanism to prevent a header from traversing the entire network diameter. In a large network, this could cause unnecessary relay traffic for events that are only relevant to a subnet.

**Impact:** Bandwidth waste, not correctness. Acceptable for testnet, should be evaluated for mainnet.

**3. No automatic reconnection to static peers**

If a `--peer` static peer disconnects, the node does not automatically re-dial. DNS-based discovery provides eventual reconnection (30s cycle), but nodes using only static peers have no reconnection mechanism.

**Impact:** Potential network fragmentation if static peers go down temporarily.

### RISKS FOR MAINNET

**1. Scalability of relay with 100+ peers**

The relay fanout is bounded (6-12 peers), but each node still receives headers from all peers that relay to it. With 100+ nodes each relaying to 6 peers, a single event generates O(600) relay messages network-wide. This is acceptable but should be monitored.

**2. Bandwidth consumption of repair under adversarial conditions**

An attacker who creates events with missing parents can force repair requests across the network. Each repair request is bounded to 256 IDs, and per-peer quota limits repairs to 50 per 60s. But a coordinated attack with many peers could still generate significant repair traffic.

**3. Eclipse attack resistance**

If an attacker controls all of a node's peers, they can:
- Withhold events (the node never sees them)
- Present a forked DAG (different events for the same causal position)
- Prevent vote propagation (stall consensus)

Mitigation: DNS-based discovery provides peer diversity. Manifest digest verification prevents peers with different validator sets from connecting. But there is no mechanism to detect or recover from a full eclipse.

**4. Sybil attack on peer discovery**

DNS-based discovery trusts the DNS resolver. An attacker who compromises the DNS (or Cloud Map) can inject arbitrary peer addresses. The handshake provides authentication (challenge-response + manifest digest), but the attacker could still consume connection slots with valid but unhelpful peers.

**5. No peer reputation persistence**

Peer scores are in-memory only. On restart, all peers start with `BaseScore=100`. A previously misbehaving peer gets a fresh score on every restart.

### THINGS ALREADY SOLID

**1. Three-plane separation (causality / body / repair)**

The Fast Path cleanly separates header relay (latency-critical, small), body fetch (bandwidth-critical, receiver-driven), and repair (correctness-critical, bounded). This is a sound architectural decision that enables independent optimization of each plane.

**2. V2 negotiation with V1 fallback**

Mixed V1/V2 topologies work correctly. V2 messages are silently dropped for V1 peers via `SafeSend`. The transition from V1 to V2 is seamless — no flag day required.

**3. Relay-before-validation for latency**

Event awareness propagates at header speed (< 1KB) rather than full-event speed (potentially MBs). Bodies are fetched on demand by receivers who need them. This is the right design for minimizing consensus latency.

**4. Manifest digest in handshake (Phase 4)**

Nodes with different validator manifests are rejected during handshake. This prevents silent network divergence caused by misconfigured manifests. Backward-compatible: empty digest (dev mode) accepts any peer.

**5. Per-message size limit via resetLimitReader**

The 4 MiB per-message limit is enforced at the transport level and resets after each decode. This prevents oversized-message DoS without the connection-kill bug of a cumulative `io.LimitReader`.

**6. Bounded relay fanout via MeshManager**

Score-weighted, diversity-injected peer selection bounds relay to O(fanout) per hop. This prevents O(N²) relay storms in large networks while maintaining good propagation latency.

**7. Per-peer quotas and backpressure signaling**

The `PeerQuota` system limits inbound message rates per category per peer. The `OverloadState` and `MsgOverloaded` signaling allows nodes to communicate backpressure to peers, who deprioritize overloaded nodes in relay fanout. This is a sound design for preventing cascade failures.

**8. Challenge-response peer authentication**

Both sides exchange random 32-byte challenges and sign them with Ed25519. Combined with PublicKey inclusion, this prevents AgentID impersonation. The challenge-response is performed during handshake before any DAG data is exchanged.

**9. Vote deduplication by signature**

Using the Ed25519 signature as the dedup key prevents gossip loops and ensures the original signed message is forwarded verbatim. Downstream nodes can independently verify the vote without trusting the relay node.

**10. Comprehensive body commitment verification**

`SHA-256(received_body) == header.BodyCommitment` verification at body reception prevents payload tampering. A malicious peer cannot substitute a different body for a relayed header.

**11. Sync batching prevents message-size connection kills**

V1 sync responses are batched into 100-event chunks (~100-200KB each), preventing the 4 MiB limit from killing connections during full-DAG sync.

---

## Summary of Action Items (Priority Order)

| # | Action | Severity | Effort |
|---|---|---|---|
| 1 | Remove unsigned-genesis exception from Fast Path ValidateEvent | **Blocks freeze** | Low |
| 2 | Add automatic reconnection for static peers | Pre-mainnet | Low |
| 3 | Add hop counter to relayed headers | Pre-mainnet | Medium |
| 4 | Persist peer scores across restarts | Pre-mainnet | Low |
| 5 | Evaluate relay bandwidth at 100+ node scale | Pre-mainnet | Medium |
| 6 | Add eclipse attack detection (peer diversity monitoring) | Pre-mainnet | High |
