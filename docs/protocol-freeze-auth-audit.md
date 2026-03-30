# Protocol Freeze — Phase 6 Audit: Auth

**Date:** 2026-03-29
**Scope:** Complete inventory of API authentication, replay protection, rate limiting, self-registration, and key management.
**Prerequisites:** Phases 1-5 (event format, consensus, settlement, genesis, networking) are frozen.
**This is the final phase of the protocol freeze.**

---

## Task 1: AETHERNET-TX-V1 Signing Spec

### Required HTTP Headers

| # | Header | Format | Example |
|---|---|---|---|
| 1 | `X-AetherNet-Version` | String constant | `"AETHERNET-TX-V1"` |
| 2 | `X-AetherNet-Chain-ID` | String | `"aethernet-testnet-1"` |
| 3 | `X-AetherNet-Actor` | Hex-encoded Ed25519 public key (64 chars) | `"abc123def456..."` |
| 4 | `X-AetherNet-Created` | Unix timestamp (seconds), string | `"1700000000"` |
| 5 | `X-AetherNet-Expires` | Unix timestamp (seconds), string | `"1700000120"` |
| 6 | `X-AetherNet-Nonce` | 32 hex chars (128-bit random) | `"deadbeef01234567..."` |
| 7 | `X-AetherNet-Signature` | Hex-encoded Ed25519 signature (128 chars) | `"aabb..."` |

**Source:** `internal/auth/txverify.go:43-49`

### Canonical Sign Bytes Construction

1. Build a `Transaction` struct from the 7 headers plus `r.Method` and `r.URL.Path`
2. Compute `body_sha256 = SHA-256(JCS(request_body))` — JCS canonicalization per RFC 8785 (Phase 1). If body is not valid JSON, hash the raw bytes.
3. `json.Marshal(Transaction)` → JCS canonicalize → these are the sign bytes
4. `TxID = hex(SHA-256(sign_bytes))`

**Source:** `txverify.go:110-130` (reconstruction), `transaction.go:44-50` (SignBytes), `transaction.go:54-61` (TxID)

### Canonicalization

**JCS (RFC 8785)** for both the request body hash and the Transaction envelope. Verified — uses the same `CanonicalizeJSON` from `internal/auth/canonical.go` as Phase 1.

### Signature Algorithm

**Ed25519.** `ed25519.Verify(publicKey, signBytes, signature)` at `transaction.go:87`.

### Chain ID Format

`"aethernet-testnet-1"` for testnet, `"aethernet-mainnet-1"` for mainnet. Determined by `AETHERNET_TESTNET` env var (`transaction.go:17-20`).

### Nonce Format

32 hex characters (128 bits of randomness). Validated at `txverify.go:103-108`. Client generates via `os.urandom(16).hex()` (Python) or `os.Getenv(16).hex()` (Go).

### Expiry Window

- **Max lifetime:** `TxMaxLifetimeSecs = 120` seconds (expires_at - created_at) (`transaction.go:24`)
- **Clock skew tolerance:** `TxClockSkewSecs = 60` seconds (`transaction.go:27`)
- **Effective acceptance window:** A transaction is accepted if `now <= expires_at + 60s` and `created_at <= now + 60s`

### External Documentation

**Not documented outside the code.** The signing spec exists only in Go code comments and the Python SDK implementation. There is no standalone specification document.

---

## Task 2: Signature Verification

### Where Verification Happens

**In the `ServeHTTP` method** (`api/server.go:823-851`) — a centralized middleware that runs before request routing. Not per-handler.

Two auth schemes are checked in order:
1. **AETHERNET-TX-V1** (primary): If `X-AetherNet-Version` header is present, calls `auth.VerifyTransaction`
2. **AETHERNET-REQUEST-V1** (deprecated): If `X-Aethernet-Agent-ID` header is present, calls `auth.VerifyRequest`

### Verification Order (TX-V1)

1. Extract 7 headers (all required) — `txverify.go:43-68`
2. Verify `chain_id` matches server config — `txverify.go:71-73`
3. Parse and validate timestamps — `txverify.go:76-100`
4. Validate nonce format (32 hex chars) — `txverify.go:103-108`
5. Compute `body_sha256` from JCS-canonicalized body — `txverify.go:111-117`
6. Reconstruct Transaction struct — `txverify.go:120-130`
7. Look up public key for actor — `txverify.go:133-136`
8. Verify Ed25519 signature — `txverify.go:139-141`
9. Compute TxID — `txverify.go:144-147`

### Failure Response

- Missing header: `400 Bad Request` with `"tx: missing required header: X-AetherNet-..."`
- Chain mismatch: `400` with `"tx: chain_id mismatch"`
- Expired: `400` with `"tx: transaction expired"`
- Bad signature: `400` with `"tx: signature verification failed"`
- Rate limited: `429 Too Many Requests` with `Retry-After` header

**Source:** `server.go:837-851`

### Body Consumption

The body is read once into `bodyBytes` at the top of `ServeHTTP` (`server.go:800-810`), then wrapped in a `bytes.NewReader` and set back on `r.Body` for downstream handlers. This avoids the double-read issue.

### Endpoints That Don't Require Signing

**All GET endpoints** do not require signing. The middleware only rejects unsigned write operations when `requireAuth=true` (`server.go:899-913`).

### Write Endpoints Missing Auth

**CRITICAL findings from the exploration:**

| Endpoint | Method | Auth status |
|---|---|---|
| `POST /v1/platform/keys` | POST | **NONE** — anyone can generate API keys |
| `POST /v1/registry` | POST | **NONE** — no signature, no ownership check |
| `DELETE /v1/registry/{id}` | DELETE | **PARTIAL** — only node's own identity |
| `POST /v1/router/register` | POST | **NONE** — no signature, no ownership check |
| `DELETE /v1/router/register/{id}` | DELETE | **NONE** — no signature, no ownership check |
| `PUT /v1/router/availability/{id}` | PUT | **NONE** — no signature, no ownership check |

These L2 (coordination layer) endpoints were added after the auth middleware was designed for L1 (core protocol) endpoints. They bypass `checkTxIDSeen` and `getAuthAgent`.

### grep Verification Output

```
398:  mux.HandleFunc("POST /v1/agents", s.handleRegisterAgent)
404:  mux.HandleFunc("POST /v1/transfer", s.handleTransfer)
405:  mux.HandleFunc("POST /v1/generation", s.handleGeneration)
406:  mux.HandleFunc("POST /v1/verify", s.handleVerify)
415:  mux.HandleFunc("POST /v1/stake", s.handleStake)
416:  mux.HandleFunc("POST /v1/unstake", s.handleUnstake)
417:  mux.HandleFunc("POST /v1/faucet", s.handleFaucet)
435:  mux.HandleFunc("POST /v1/registry", s.handlePostRegistry)
438:  mux.HandleFunc("DELETE /v1/registry/{agent_id}", s.handleDeleteRegistry)
443:  mux.HandleFunc("POST /v1/router/register", s.handleRouterRegister)
444:  mux.HandleFunc("DELETE /v1/router/register/{agent_id}", s.handleRouterUnregister)
445:  mux.HandleFunc("PUT /v1/router/availability/{agent_id}", s.handleRouterAvailability)
457:  mux.HandleFunc("POST /v1/tasks", s.handlePostTask)
468:  mux.HandleFunc("POST /v1/tasks/{id}/trajectory/commit", s.handleTrajectoryCommit)
469:  mux.HandleFunc("POST /v1/tasks/{id}/claim", s.handleClaimTask)
470:  mux.HandleFunc("POST /v1/tasks/{id}/submit", s.handleSubmitTask)
471:  mux.HandleFunc("POST /v1/tasks/{id}/approve", s.handleApproveTask)
472:  mux.HandleFunc("POST /v1/tasks/{id}/dispute", s.handleDisputeTask)
473:  mux.HandleFunc("POST /v1/tasks/{id}/cancel", s.handleCancelTask)
474:  mux.HandleFunc("POST /v1/tasks/{id}/subtask", s.handleCreateSubtask)
476:  mux.HandleFunc("POST /v1/replay/outcome", s.handleReplayOutcome)
479:  mux.HandleFunc("POST /v1/replay/submit", s.handleReplaySubmit)
482:  mux.HandleFunc("POST /v1/challenges/{id}/resolve", s.handleResolveChallenge)
483:  mux.HandleFunc("POST /v1/challenges", s.handleOpenChallenge)
490:  mux.HandleFunc("POST /v1/platform/keys", s.handleGenerateKey)
492:  mux.HandleFunc("DELETE /v1/platform/keys/{key}", s.handleRevokeKey)
```

---

## Task 3: TxID Replay Protection

### How TxID Is Computed

`TxID = hex(SHA-256(JCS(json.Marshal(Transaction))))` — deterministic from the transaction's canonical bytes. Same transaction → same TxID on every node.

**Source:** `transaction.go:54-61`

### Storage

**Dual-layer:**
1. **In-memory hot cache:** `TxIDStore.local map[string]int64` — keyed by `"txid:" + txID`, value is Unix timestamp
2. **BadgerDB persistence:** `PutMeta("txid:" + txID, timestamp)` — survives restarts

**Source:** `txidstore.go:14-57`

### TTL

**10 minutes** (`txidTTL = 10 * time.Minute`, `txidstore.go:9`). Cleanup goroutine runs every 5 minutes, evicting entries older than 10 minutes from the in-memory cache. BadgerDB entries are NOT explicitly cleaned (they're harmless — the timestamp check prevents reuse).

### Duplicate Request Handling

`checkTxIDSeen` (`server.go:610-648`):
1. Extract TxID from verified transaction context
2. Call `txIDStore.MarkSeen(txID)` — returns true if already seen
3. If already seen: return `409 Conflict` with `"DUPLICATE_TX"` code and the TxID
4. The response is idempotent — the first request succeeds, subsequent ones get 409

### Collision Probability

TxID is SHA-256(sign_bytes) which includes a 128-bit random nonce. Collision probability is negligible (birthday bound at ~2^128 for the nonce alone, further mixed with timestamp, actor, path, and body hash).

### After Node Restart

**Yes, previously seen TxIDs are still rejected.** BadgerDB persistence survives restarts. `MarkSeen` checks BadgerDB when the in-memory cache misses (`txidstore.go:44-48`).

### Cross-Node Replay

**Not protected.** Each node maintains its own TxIDStore. The same signed request hitting two different nodes via ALB will succeed on both. This is a **known limitation** — the consensus layer (Phase 2) is the authoritative deduplication mechanism for economic operations (same event can't be finalized twice).

---

## Task 4: Rate Limiting

### Rate Limit Types

**Three-dimensional per-endpoint limiting** (`auth/ratelimiter.go`):
1. **Per-agent per-hour** — limits how many requests a single signing identity can make
2. **Per-IP per-hour** — limits requests from a single IP address
3. **Global per-hour** — limits total requests to an endpoint across all callers

### Configured Limits

| Endpoint | Per-Agent/hr | Per-IP/hr | Global/hr |
|---|---|---|---|
| `/v1/agents` | unlimited | 3 | 30 |
| `/v1/faucet` | 1 | 3 | 84 |
| `/v1/tasks` | 20 | 50 | 500 |
| `/v1/tasks/claim` | 60 | 200 | 2000 |
| `/v1/tasks/submit` | 60 | 200 | 2000 |
| `/v1/tasks/approve` | 60 | 200 | 2000 |
| `/v1/tasks/dispute` | 10 | 50 | 200 |
| `/v1/tasks/cancel` | 20 | 50 | 500 |
| `/v1/transfer` | 120 | 300 | 3000 |
| `/v1/stake` | 20 | 100 | 500 |
| `/v1/unstake` | 20 | 100 | 500 |
| `/v1/challenge` | 10 | 50 | 200 |

**Source:** `auth/ratelimiter.go:18-31`

Additionally, `AgentRateLimiter` (`auth/ratelimit.go`) provides a separate per-agent hourly cap applied in the `ServeHTTP` middleware (`server.go:842`).

### Storage

**In-memory only.** Rate limit counters are not persisted to BadgerDB and reset on node restart. Each node tracks independently.

### Exceeded Behavior

`429 Too Many Requests` with `Retry-After` header indicating seconds until the current hour window resets.

### Bypass Vectors

- **Key rotation:** Generating a new Ed25519 key creates a new agent identity, resetting per-agent limits. Combined with open self-registration, an attacker can create unlimited identities.
- **IP rotation:** Different source IPs bypass per-IP limits.
- **Multi-node:** Per-node rate limits mean an attacker can hit each node at the full limit (3 nodes × limit).
- **Missing endpoints:** L2 endpoints (`/v1/registry`, `/v1/router/*`, `/v1/platform/keys`) have **no rate limits configured**.

### Cross-Node Consistency

**Not consistent.** Rate limits are per-node in-memory. An attacker can hit each node independently.

---

## Task 5: Self-Registration Auth

### Resolving the Chicken-and-Egg Problem

When `VerifyTransaction` looks up the actor's public key and the actor is not registered, it falls back to hex-decoding the actor field itself as the public key (`server.go:830-834`):

```go
pubBytes, hexErr := hex.DecodeString(actor)
if hexErr == nil && len(pubBytes) == 32 {
    return ed25519.PublicKey(pubBytes), nil
}
```

**The actor IS the hex-encoded public key.** The server uses it directly for signature verification without needing a prior registry entry. After signature verification succeeds, `handleRegisterAgent` uses the verified actor as the new agent's identity.

### Can an Attacker Register with Someone Else's Key?

**No.** The actor field must be the hex encoding of the signer's public key. The signature is verified against this key (`txverify.go:139`). An attacker would need the victim's private key to produce a valid signature for the victim's public key.

### Can an Attacker Replay a Registration Request?

**Yes, but harmlessly.** Registration is idempotent — `handleRegisterAgent` does not call `checkTxIDSeen`. Re-registering an already-registered agent returns the existing identity (status 409, "already registered"). The onboarding grant is also idempotent (checked by balance/agent count).

### Registration Spam Prevention

- **IP rate limit:** 3 registrations per IP per hour, 30 global per hour (`ratelimiter.go:19`)
- **No per-agent limit** on `/v1/agents` (PerAgentPerHr: 0) — because the agent doesn't exist yet
- **No proof of work or invite required**
- **Onboarding cost:** Each registration triggers an onboarding grant from the ecosystem bucket, which is finite. After 800,000 registrations, the onboarding allocation is exhausted.

---

## Task 6: Key Management

### Private Key Storage

**Client-side only.** Private keys are never sent to the server. The server only sees the hex-encoded public key (as the actor/AgentID) and the signature.

Python SDK stores keys at `~/.aethernet/keys/{name}.json` with `0o600` permissions (`signing.py:136-143`). File format:
```json
{"seed": "<hex-encoded 32-byte Ed25519 seed>", "public_key": "<hex>"}
```

Go node stores keys at `{data_dir}/node_keys/identity.json` with scrypt encryption (`internal/crypto/keys.go`).

### Key Format

Ed25519 seed (32 bytes), stored as hex. The full keypair is derived from the seed. No PKCS8 or X.509 wrapping.

### Key Rotation

**Not supported at the agent level.** An agent's identity IS its public key. Changing the key means creating a new identity. There is no mechanism to link old and new identities.

**Supported at the validator level.** The `ValidatorKeyRotate` event type (`event.go:124`) allows validators to rotate their consensus key while keeping their seat identity. This rotates the operator/consensus key, not the agent identity.

### Key Compromise Recovery

**No revocation mechanism.** If an agent's private key is compromised:
- The attacker can sign requests as the agent
- There is no way to revoke the compromised key
- The agent must create a new identity (new key) and transfer assets manually
- Staked funds under the compromised key cannot be unstaked without the private key

For validators: key rotation via `ValidatorKeyRotate` allows switching to a new key, but requires the old key to sign the rotation event. If the old key is compromised, the attacker could rotate first.

---

## Task 7: Gap Analysis

### GAPS THAT BLOCK PROTOCOL FREEZE

**1. L2 endpoints lack auth — router/register, registry, platform/keys are completely open**

`POST /v1/router/register`, `DELETE /v1/router/register/{id}`, `PUT /v1/router/availability/{id}`, `POST /v1/registry`, and `POST /v1/platform/keys` have NO signature verification and NO `checkTxIDSeen`. Any caller can:
- Register any agent for autonomous task routing
- Unregister any agent from routing
- Toggle any agent's availability
- Create service listings for any agent
- Generate unlimited API keys

**Recommendation:** Add `getAuthAgent` checks to all L2 write endpoints. Require that the authenticated agent matches the target agent_id.

**2. TX-V1 signing spec is not documented outside the code**

The complete signing specification — header format, canonical bytes construction, JCS canonicalization rules — exists only in Go code and Python SDK. Third-party implementers must reverse-engineer the spec from code.

**Recommendation:** Create `docs/tx-v1-signing-spec.md` with the complete specification, test vectors, and example implementations.

**3. L2 endpoints have no rate limits**

`/v1/registry`, `/v1/router/*`, and `/v1/platform/keys` are not in `DefaultLimits()` (`ratelimiter.go:18-31`). They have zero rate limiting.

**Recommendation:** Add rate limits for all L2 write endpoints.

**4. No rate limiting on registration at the per-agent level**

`/v1/agents` has `PerAgentPerHr: 0` — unlimited per-agent. Combined with open self-registration (any Ed25519 key can register), an attacker can create thousands of identities per hour limited only by IP (3/hr/IP) and global (30/hr).

**Recommendation:** The IP and global limits are the primary defense. Consider adding proof-of-work or a deposit requirement for mainnet.

### RISKS FOR MAINNET

**1. Cross-node replay via ALB**

The same signed request hitting multiple nodes via the load balancer succeeds on all of them. For transfers and tasks, this is harmless (the DAG deduplicates the resulting events). For faucet grants, the cooldown is per-ledger (per-node), so a grant could succeed on multiple nodes simultaneously.

**2. Key compromise with no revocation**

A compromised agent key allows the attacker to spend all the agent's balance, unstake their tokens, and act in their name. There is no emergency freeze or key revocation mechanism.

**3. Rate limit bypass via identity rotation**

An attacker can generate new Ed25519 keys rapidly, register each one, and use the per-agent limits of each new identity. The IP limit (3/hr) and global limit (30/hr) on `/v1/agents` are the only defense.

**4. Registration spam creating unbounded state**

Each registration creates an identity registry entry, a faucet grant (from ecosystem bucket), and potentially staking entries. With 800,000 max registrations and ~100 AET minimum per agent, the total state is bounded but large.

**5. `--no-auth` flag disables all protection**

When `--no-auth` is set, ALL write operations are accepted unsigned. This is documented as testnet-only but there is no enforcement mechanism.

### THINGS ALREADY SOLID

**1. Ed25519 signing with JCS canonicalization**

The TX-V1 scheme uses Ed25519 for signatures and RFC 8785 JCS for canonical bytes. Both are industry-standard, deterministic, and cross-language compatible. The same sign bytes produce identical signatures in Go and Python (verified by cross-language test vectors in Phase 1).

**2. Self-registration verification is cryptographically sound**

The actor-as-public-key pattern elegantly solves the chicken-and-egg problem. Only the holder of the corresponding private key can register a given public key as an agent. The signature proves possession without requiring pre-registration.

**3. Nonce-based replay protection with persistence**

TxIDs (SHA-256 of sign bytes) are stored in both in-memory cache and BadgerDB. Replayed requests are rejected with 409. The 10-minute TTL covers the 2-minute transaction lifetime with margin.

**4. Three-dimensional rate limiting**

Per-agent, per-IP, and global limits provide defense-in-depth. An attacker must bypass all three dimensions simultaneously to exceed limits.

**5. Body re-read prevention**

The `ServeHTTP` middleware reads the body once, stores it as `bodyBytes`, and wraps it in a `bytes.NewReader` for downstream handlers. This prevents the double-read bug that plagues many Go HTTP servers with body-consuming middleware.

**6. Timestamp-based transaction expiry**

The 120-second max lifetime with 60-second clock skew tolerance provides a tight window. Transactions cannot be stockpiled for later replay.

**7. Idempotent registration**

`handleRegisterAgent` is idempotent — re-registering an existing agent returns 409 without side effects. The onboarding grant is also idempotent (checked by agent count).

---

## Summary of Action Items (Priority Order)

| # | Action | Severity | Effort |
|---|---|---|---|
| 1 | Add TX-V1 auth to L2 write endpoints (registry, router, platform/keys) | **Blocks freeze** | Medium |
| 2 | Add rate limits for L2 endpoints | **Blocks freeze** | Low |
| 3 | Create TX-V1 signing spec document | **Blocks freeze** | Medium |
| 4 | Add faucet cross-node dedup (ledger-based cooldown already exists but verify ALB behavior) | Pre-mainnet | Low |
| 5 | Add key revocation / emergency freeze mechanism | Pre-mainnet | High |
| 6 | Add proof-of-work or deposit for registration on mainnet | Pre-mainnet | Medium |
| 7 | Remove `--no-auth` flag or require explicit confirmation | Pre-mainnet | Low |
