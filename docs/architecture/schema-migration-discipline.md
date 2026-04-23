# Schema migration discipline

**Status**: F4A B.3 — initial discipline document. Establishes policy and enumerates current state. Actual migrations are future work; this document gates new schema changes.

**Scope**: every persisted record type — anything that survives a process restart — and every event payload version. Excludes in-process derived state (projections, indexes, caches) which is rebuilt from canonical state on startup.

**Why this exists**: F4A B.2 store tests surfaced 5 FINDINGs about silent schema-version handling (`store-corruption-fail-stop`, `stake-meta-silent-zero`, `admission-schema-no-gate`, `admission-state-no-gate`, `replay-reserve-truncated-zero`). All are observable in the current code. None are fixed by this document — but every future schema change must go through this discipline so the surface stops growing.

---

## 1. Policy: introducing a schema change

A schema change is **any** of:
- Adding, removing, or renaming a field on a persisted record or event payload.
- Changing the encoding of an existing field (uint64 → string, etc.).
- Changing the meaning of a field's value space (new enum variant, new error code, etc.).
- Changing the wire format of a sub-record (e.g., the validator block of an identity).

For every schema change, do these steps in order. Skipping any step is a P0 review block.

### 1.1 Bump the version

Increment the `Version uint8` field of the affected payload / record. Never reuse a version number.

If the record currently has no version field, **add one** as part of the change. The default initial value for a new field is the next unused integer in the type's history (see §2 for current versions). For records that have never been versioned, this is a v2 bump that begins the discipline at v1=existing. Document the v1→v2 mapping in §2.

### 1.2 Decide on a migration strategy

Pick one. Document the choice in the change's commit message.

| Strategy | When | Mechanism |
|---|---|---|
| **Dual-read** | Wire format changed but old records remain in the store. | `GetX` understands both old and new versions; new writes use new version only. Old records are converted lazily on next mutation. |
| **Forward-only cutover** | Old records cannot remain. | Operator-driven migration script reads old, writes new, deletes old. Requires a downtime window or a forward-only cutover commit. |
| **Removal** | Field is being deleted. | Set field to its zero value on read (dual-read for one release), then delete the field in a follow-up that bumps the version again. |
| **New record type** | Existing records keep their version; a new record type has its own version starting at 1. | Add new prefix to `internal/store/store.go`. Document in §2. |

### 1.3 Add the gate

Every persisted record's `Get` and `AllX` methods MUST validate the version field on read:

- If `Version` is 0, treat as a missing field — caller decides whether to upgrade in place or fail.
- If `Version` is recognized (current or older with a documented dual-read path), proceed.
- If `Version` is newer than current, fail with `ErrSchemaTooNew`. Operator must upgrade the binary before it can read this record.

The current store has 0 such gates (FINDING `admission-schema-no-gate`). Adding gates is part of every schema-change PR going forward; retroactive gating is tracked as a separate work item.

### 1.4 Add tests

Every schema change needs:
- A round-trip test for the new version.
- A dual-read test (where applicable) asserting old-version records still decode correctly.
- A negative test: a record with a `Version` field beyond the current implementation returns `ErrSchemaTooNew` rather than silently mis-decoding.

The B.2 test suite (`internal/store/store_extended_test.go`) has table-driven precedents for all three patterns.

### 1.5 Document in this file

Update the relevant table in §2 with the new version, change date, and migration strategy. The git history of this file is the schema changelog.

---

## 2. Current state — persisted records

### 2.1 Store-layer records (`internal/store/store.go`)

Each row: BadgerDB key prefix; what's stored; current schema version (if any); validation gate (yes / no); test coverage status.

| Prefix | Record | Version field | Gate | Test | Notes |
|---|---|---|---|---|---|
| `evt:` | DAG event (`*event.Event`) | per-payload (see §2.2) | no | yes | Container envelope is unversioned; payloads carry their own `v`. |
| `txf:` | Transfer ledger entry (`*ledger.TransferEntry`) | none | no | yes (B.2) | `TransferEntry` struct has no Version field. **Action**: add `Version uint8` at next change. |
| `gen:` | Generation ledger entry (`*ledger.GenerationEntry`) | none | no | yes (B.2) | Same as transfer. **Action**: add `Version uint8`. |
| `ocs:` | OCS pending item (`*ocs.PendingItem`) | none | no | yes | In-flight consensus state. Pending entries are short-lived; missing version is lower-risk. |
| `idn:` | Identity fingerprint (`*identity.CapabilityFingerprint`) | none | no | yes (B.2) | Long-lived. **Action**: add `Version uint8` at next change to capability surface. |
| `stk:` | Stake meta (3×int64 + uint64 packed binary) | none (positional) | no — silent-zero on truncation | yes (B.2) | FINDING `stake-meta-silent-zero`: `parseStakeMetaValue` returns `(0,0,0)` for any blob < 16 bytes. **Action**: prefix records with a 1-byte version tag at next change; current code reads 16-byte legacy + new tagged format via dual-read. |
| `reg:` | Service-registry listing (raw JSON) | caller-controlled | no | yes (B.2) | Opaque blob from `internal/marketplace`. The marketplace package owns the version inside the JSON. **Action**: surface the contract — marketplace should emit `{"v":N, ...}` and the loader should validate `v`. |
| `meta:` | Generic key-value metadata | caller-controlled | no | yes (B.2) | Holds genesis marker, onboarding counter, etc. Each meta key has its own ad-hoc encoding. **Action**: enumerate every `meta:` key and its encoding in §2.3. |
| `key:` | Platform developer API key (raw JSON) | caller-controlled | no | yes (B.2) | Same shape as `reg:` — opaque. **Action**: same as `reg:`. |
| `esc:` | Escrow entry (`*escrow.EscrowEntry`) | none | no | yes (B.2) | **Action**: add `Version uint8` at next escrow-record change. |
| `val:` | Validator registry record | caller-controlled (binary) | no | yes (B.2) | Binary-packed. **Action**: prefix with version byte at next change. |
| `rsvr:` | Per-category replay reserve balance (uint64 LE) | none (positional) | no — silent-zero on truncation | yes (B.2) | FINDING `replay-reserve-truncated-zero`. **Action**: same dual-read pattern as `stk:`. |
| `chal:` | Challenge bond record (raw JSON) | caller-controlled | no | yes (B.2) | **Action**: assert `{"v":N}` on the JSON envelope. |
| `dispatch:` | Dispatcher admission record (`AdmissionRecord` JSON) | `SchemaVersion uint32` (persisted) | **no** | yes (B.2) | FINDING `admission-schema-no-gate`. The field is written and read but never validated — a `SchemaVersion: 999` round-trips opaquely. **Action**: add `if rec.SchemaVersion > AdmissionCurrentVersion { return ErrSchemaTooNew }` to `GetAdmission` + `AllAdmissions`. Highest priority of the gate gaps because the dispatcher's exactly-once admission state machine depends on this record. |
| `tsk:` | Task lifecycle blob (raw JSON from `tasks` package) | caller-controlled | no | yes (B.2) | The `tasks.Task` struct has its own version field; verify it's used. **Action**: assert version inside `tsk:` JSON. |
| `rep:` | Reputation entry (raw JSON) | caller-controlled | no | yes (B.2) | **Action**: assert `{"v":N}`. |
| `rpj:` | Replay job record (raw JSON) | caller-controlled | no | yes (B.2) | **Action**: assert `{"v":N}`. |
| `rpo:` | Replay outcome record (raw JSON) | caller-controlled | no | yes (B.2) | **Action**: assert `{"v":N}`. |
| `vot:` | Persisted vote record (`PersistedVote` JSON) | none | no | yes (B.2) | **Action**: add `Version uint8`. Votes are causal events; their persisted form must be versioned for replay safety. |
| `cnr:` | Canary record (raw JSON) | caller-controlled | no | yes (B.2) | **Action**: assert `{"v":N}`. |
| `cnrt:` | Canary task index (raw JSON) | caller-controlled | no | yes (B.2) | Index — rebuildable. Lower priority. |
| `cal:` | Calibration signal (raw JSON) | caller-controlled | no | yes (B.2) | **Action**: assert `{"v":N}`. |

**Cross-cutting FINDING `store-corruption-fail-stop`**: every `AllX` iterator in `internal/store/store.go` propagates the FIRST JSON unmarshal error and stops, hiding every healthy row that comes after. Combined with the lack of schema gates, a single mis-versioned record halts iteration of every record under that prefix on startup. **Action**: change every `AllX` to skip-with-warn for individual decode errors, OR keep fail-stop and add a schema gate so unknown versions fail loudly with operator guidance. Decision deferred to F4B.

### 2.2 Event payload schema versions (`internal/event/event.go`)

Every event payload struct carries `Version uint8` at field index 0 (JSON tag `"v"`). Current versions are all `1` — the protocol has not yet rotated any payload version.

| Payload | Field-set summary | Current `v` | Notes |
|---|---|---:|---|
| `TransferPayload` | from, to, amount, currency | 1 | |
| `GenerationPayload` | recipient, amount, source, currency, ref | 1 | |
| `AttestationPayload` | trajectory ref, attester, signature | 1 | |
| `VerificationPayload` | task, verifier, decision, evidence | 1 | |
| `DelegationPayload` | delegator, delegate, scope | 1 | |
| `RegistrationPayload` | agent_id, public_key, reputation, staked_amount | 1 | |
| `GenesisFundingPayload` | from_bucket, to_agent, amount | 1 | |
| `TaskPostedPayload` | task_id, poster, budget, category, requirements | 1 | |
| `TaskClaimedPayload` | task_id, worker, claim_ts | 1 | |
| `TaskSubmittedPayload` | task_id, worker, evidence | 1 | |
| `TaskApprovedPayload` | task_id, approver | 1 | |
| `TaskDisputedPayload` | task_id, disputer, reason | 1 | |
| `TaskVerificationVotePayload` | round_id, voter, verdict, score, family | 1 | |
| `TaskVerificationConsensusPayload` | round_id, task_id, final_verdict, final_score, finalization_ts | 1 | F4B may add a logical-key projection field per the LogicalKeyConsumer pattern (plan §5). |
| `SlashingChallengePayload` | challenger, target, evidence | 1 | |
| `PrerequisiteWithholdingPayload` | reporter, withholder, evidence | 1 | |
| `TrajectoryPayload` (`internal/event/trajectory.go`) | trajectory body | 1 | |

**Validation gate**: `event.GetPayload[T]` (used by every consumer) does NOT validate the version field. A consumer that calls `GetPayload[TaskVerificationConsensusPayload]` on a `v: 99` event silently decodes whatever fields happen to overlap. **Action**: add a generic version gate in `GetPayload[T]` that compares against a per-type max-supported-version table, returning a typed error on mismatch. This is the highest-leverage gate to add — single change protects every payload type at once.

### 2.3 `meta:` key inventory

Each key under the `meta:` prefix has its own ad-hoc encoding. Enumerating here so future keys go in the table rather than growing organically.

| Key (after `meta:` prefix) | Encoding | Producer | Consumer | Notes |
|---|---|---|---|---|
| `genesis-funding:<event_id>` | empty marker | `cmd/node/main.go` (sync handler) | startup idempotency | Presence == "applied" |
| `genesis-marker` | binary tag | `cmd/node/main.go` | startup once-only check | |
| `onboarding-counter` | uint64 LE | `internal/api/onboarding.go` | onboarding rate limiter | |
| `dispatch-admission:<event_id>` | (planned — currently uses `dispatch:` prefix instead) | dispatcher | dispatcher | Documented for completeness; no current key. |

If you add a new `meta:` key in any PR, add it to this table in the same PR.

---

## 3. Open gaps (in priority order, for F4B / follow-up workstreams)

The following gaps are not closed by this document. Each is a discrete piece of work that should be planned and sequenced explicitly.

1. **Add `event.GetPayload[T]` version gate.** Single change protects every consumer at once. Highest leverage.
2. **Add `dispatch:` admission-record schema gate.** Highest individual-record priority because it gates the dispatcher's exactly-once state machine — F4B's LogicalKeyConsumer changes will cross this surface.
3. **Decide `AllX` corruption policy: skip-with-warn vs fail-loudly.** Cross-cutting policy decision; affects every store iterator.
4. **Add `Version uint8` field to records currently without one.** Prioritized list: `vot:` (causal), `txf:` / `gen:` (ledger), `idn:` (identity), `esc:` (escrow), `stk:` / `rsvr:` (binary positional → tagged).
5. **Surface JSON-envelope versioning for opaque-blob prefixes** (`reg:`, `key:`, `chal:`, `tsk:`, `rep:`, `rpj:`, `rpo:`, `cnr:`, `cnrt:`, `cal:`).

Each of these has a roughly bounded blast radius and can be sequenced independently. None are urgent in the absence of an actual schema change; all become urgent the moment a schema change is needed and the version-handling discipline is missing.

---

## 4. Glossary

- **Persisted record**: anything written to BadgerDB via `internal/store/store.go`. Survives process restart.
- **Schema version**: a monotonic uint8 (or uint32 for `dispatch:` admission) carried by the record; used to dispatch decoder selection.
- **Dual-read**: reading both the new and old encoding from the same `Get` call. Required when records of mixed versions co-exist in the store.
- **Forward-only cutover**: a one-time migration that converts every old record to the new encoding, after which the old decoder is removed.
- **Schema gate**: a runtime check in `GetX` / `AllX` that rejects records with versions outside the implementation's supported range.
- **FINDING**: a documented current-state issue surfaced during F4A B.2 store testing; tracked here for the F4A architect-gate report.

---

**End of schema migration discipline v1.**
