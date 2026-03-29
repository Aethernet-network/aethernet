# Protocol Freeze — Phase 1 Audit: Event Format

**Date:** 2026-03-29
**Scope:** Complete inventory of all event types, payload schemas, serialization behavior, and gaps.

---

## Task 1: All Event Types

### Event Envelope (shared across all types)

**Struct:** `event.Event` (`internal/event/event.go:166`)

| Field | Go type | JSON tag | In canonical hash? | Notes |
|---|---|---|---|---|
| ID | `EventID` (string) | `"id"` | No | Excluded (circular — we are computing it) |
| Type | `EventType` (string) | `"type"` | Yes | |
| CausalRefs | `[]EventID` | `"causal_refs"` | Yes | Normalized to `[]` (never null) |
| Payload | `json.RawMessage` | `"payload"` | Yes | Pre-marshaled at creation time |
| AgentID | `string` | `"agent_id"` | Yes | |
| Signature | `[]byte` | `"signature,omitempty"` | No | Excluded (circular — signing happens after hashing) |
| CausalTimestamp | `uint64` | `"causal_timestamp"` | Yes | Lamport clock |
| StakeAmount | `uint64` | `"stake_amount"` | Yes | µAET |
| SettlementState | `SettlementState` (string) | `"settlement_state"` | No | Excluded (mutable post-creation) |

### Complete Event Type Table

| # | Type constant | String value | Payload struct | Defined in |
|---|---|---|---|---|
| 1 | `EventTypeTransfer` | `"Transfer"` | `TransferPayload` | `event/event.go:451` |
| 2 | `EventTypeGeneration` | `"Generation"` | `GenerationPayload` | `event/event.go:488` |
| 3 | `EventTypeAttestation` | `"Attestation"` | `AttestationPayload` | `event/event.go:519` |
| 4 | `EventTypeVerification` | `"Verification"` | `VerificationPayload` | `event/event.go:544` |
| 5 | `EventTypeDelegation` | `"Delegation"` | `DelegationPayload` | `event/event.go:571` |
| 6 | `EventTypeRegistration` | `"Registration"` | `RegistrationPayload` | `event/event.go:596` |
| 7 | `EventTypeVerificationVote` | `"VerificationVote"` | `VerificationVotePayload` | `settlement/verdict.go:24` |
| 8 | `EventTypeSettlement` | `"Settlement"` | `SettlementPayload` | `settlement/verdict.go:45` |
| 9 | `EventTypeTaskSettlement` | `"TaskSettlement"` | `TaskSettlementPayload` | `settlement/verdict.go:64` |
| 10 | `EventTypeGenesisFunding` | `"GenesisFunding"` | `GenesisFundingPayload` | `event/event.go:606` |
| 11 | `EventTypeTrajectoryCommit` | `"TrajectoryCommit"` | `TrajectoryCommitPayload` | `event/trajectory.go:41` |
| 12 | `EventTypeTaskPosted` | `"TaskPosted"` | `TaskPostedPayload` | `event/event.go:616` |
| 13 | `EventTypeTaskClaimed` | `"TaskClaimed"` | `TaskClaimedPayload` | `event/event.go:630` |
| 14 | `EventTypeTaskSubmitted` | `"TaskSubmitted"` | `TaskSubmittedPayload` | `event/event.go:636` |
| 15 | `EventTypeTaskApproved` | `"TaskApproved"` | `TaskApprovedPayload` | `event/event.go:645` |
| 16 | `EventTypeTaskDisputed` | `"TaskDisputed"` | `TaskDisputedPayload` | `event/event.go:651` |
| 17 | `EventTypeValidatorGenesisSet` | `"ValidatorGenesisSet"` | `ValidatorGenesisSetPayload` | `validatorlifecycle/events.go:64` |
| 18 | `EventTypeValidatorJoin` | `"ValidatorJoin"` | `ValidatorJoinPayload` | `validatorlifecycle/events.go:109` |
| 19 | `EventTypeValidatorActivate` | `"ValidatorActivate"` | `ValidatorActivatePayload` | `validatorlifecycle/events.go:159` |
| 20 | `EventTypeValidatorSuspend` | `"ValidatorSuspend"` | `ValidatorSuspendPayload` | `validatorlifecycle/events.go:180` |
| 21 | `EventTypeValidatorResume` | `"ValidatorResume"` | `ValidatorResumePayload` | `validatorlifecycle/events.go:206` |
| 22 | `EventTypeValidatorExit` | `"ValidatorExit"` | `ValidatorExitPayload` | `validatorlifecycle/events.go:238` |
| 23 | `EventTypeValidatorKeyRotate` | `"ValidatorKeyRotate"` | `ValidatorKeyRotatePayload` | `validatorlifecycle/events.go:267` |
| 24 | `EventTypeValidatorSlashApplied` | `"ValidatorSlashApplied"` | `ValidatorSlashAppliedPayload` | `validatorlifecycle/events.go:303` |

### Payload Field Inventory

**1. TransferPayload** (`event/event.go:451`)

| Field | Go type | JSON tag | Required? |
|---|---|---|---|
| FromAgent | `string` | `"from_agent"` | Yes |
| ToAgent | `string` | `"to_agent"` | Yes |
| Amount | `uint64` | `"amount"` | Yes |
| Currency | `string` | `"currency"` | Yes |
| Memo | `string` | `"memo,omitempty"` | No |
| Reason | `string` | `"reason,omitempty"` | No |
| TaskID | `string` | `"task_id,omitempty"` | No |

**2. GenerationPayload** (`event/event.go:488`)

| Field | Go type | JSON tag | Required? |
|---|---|---|---|
| GeneratingAgent | `string` | `"generating_agent"` | Yes |
| BeneficiaryAgent | `string` | `"beneficiary_agent"` | Yes |
| ClaimedValue | `uint64` | `"claimed_value"` | Yes |
| EvidenceHash | `string` | `"evidence_hash"` | Yes |
| TaskDescription | `string` | `"task_description"` | Yes |

**3. AttestationPayload** (`event/event.go:519`)

| Field | Go type | JSON tag | Required? |
|---|---|---|---|
| AttestingAgent | `string` | `"attesting_agent"` | Yes |
| TargetEventID | `EventID` (string) | `"target_event_id"` | Yes |
| ClaimedAccuracy | **`float64`** | `"claimed_accuracy"` | Yes |
| StakedAmount | `uint64` | `"staked_amount"` | Yes |

**4. VerificationPayload** (`event/event.go:544`)

| Field | Go type | JSON tag | Required? |
|---|---|---|---|
| VerifyingAgent | `string` | `"verifying_agent"` | Yes |
| TargetEventID | `EventID` (string) | `"target_event_id"` | Yes |
| Verdict | `bool` | `"verdict"` | Yes |
| EvidenceHash | `string` | `"evidence_hash"` | Yes |
| StakedAmount | `uint64` | `"staked_amount"` | Yes |

**5. DelegationPayload** (`event/event.go:571`)

| Field | Go type | JSON tag | Required? |
|---|---|---|---|
| DelegatorAgent | `string` | `"delegator_agent"` | Yes |
| DelegateAgent | `string` | `"delegate_agent"` | Yes |
| SpendingLimit | `uint64` | `"spending_limit"` | Yes |
| PermittedCategories | `[]string` | `"permitted_categories"` | Yes |
| ExpiresAt | **`time.Time`** | `"expires_at"` | Yes |

**6. RegistrationPayload** (`event/event.go:596`)

| Field | Go type | JSON tag | Required? |
|---|---|---|---|
| AgentID | `string` | `"agent_id"` | Yes |
| PublicKey | `[]byte` | `"public_key"` | Yes |
| ReputationScore | `uint64` | `"reputation_score"` | Yes |
| StakedAmount | `uint64` | `"staked_amount"` | Yes |

**7. VerificationVotePayload** (`settlement/verdict.go:24`)

| Field | Go type | JSON tag | Required? |
|---|---|---|---|
| TargetEventID | `string` | `"target_event_id"` | Yes |
| VoterID | `string` | `"voter_id"` | Yes |
| Verdict | `string` | `"verdict"` | Yes |
| VerifiedValue | `uint64` | `"verified_value"` | Yes |
| Timestamp | `int64` | `"timestamp"` | Yes |

**8. SettlementPayload** (`settlement/verdict.go:45`)

| Field | Go type | JSON tag | Required? |
|---|---|---|---|
| TargetEventID | `string` | `"target_event_id"` | Yes |
| Verdict | `string` | `"verdict"` | Yes |
| VerifiedValue | `uint64` | `"verified_value"` | Yes |
| ConsensusRound | `uint64` | `"consensus_round"` | Yes |
| Attestations | `[]VoterAttestation` | `"attestations"` | Yes |

*Nested:* **VoterAttestation** (`settlement/verdict.go:35`)

| Field | Go type | JSON tag |
|---|---|---|
| VoterID | `string` | `"voter_id"` |
| Verdict | `string` | `"verdict"` |
| Weight | `uint64` | `"weight"` |

**9. TaskSettlementPayload** (`settlement/verdict.go:64`)

| Field | Go type | JSON tag | Required? |
|---|---|---|---|
| TaskID | `string` | `"task_id"` | Yes |
| PosterID | `string` | `"poster_id"` | Yes |
| ClaimerID | `string` | `"claimer_id"` | Yes |
| Budget | `uint64` | `"budget"` | Yes |
| AcceptanceHash | `string` | `"acceptance_hash"` | Yes |
| EvidenceHash | `string` | `"evidence_hash"` | Yes |
| Category | `string` | `"category"` | Yes |
| Score | **`float64`** | `"score"` | Yes |
| HoldGeneration | `bool` | `"hold_generation"` | Yes |

**10. GenesisFundingPayload** (`event/event.go:606`)

| Field | Go type | JSON tag | Required? |
|---|---|---|---|
| FromBucket | `string` | `"from_bucket"` | Yes |
| ToAgent | `string` | `"to_agent"` | Yes |
| Amount | `uint64` | `"amount"` | Yes |
| Reason | `string` | `"reason"` | Yes |

**11. TrajectoryCommitPayload** (`event/trajectory.go:41`)

| Field | Go type | JSON tag | Required? |
|---|---|---|---|
| TaskID | `string` | `"task_id"` | Yes |
| ParentCommitID | `string` | `"parent_commit_id,omitempty"` | No |
| Outcome | `TrajectoryOutcome` (string) | `"outcome"` | Yes |
| CheckpointHash | `string` | `"checkpoint_hash"` | Yes |
| CheckpointSize | `int64` | `"checkpoint_size"` | Yes |
| ComputeCost | `uint64` | `"compute_cost"` | Yes |
| QualityScore | **`float64`** | `"quality_score"` | Yes |
| CategoryHint | `string` | `"category_hint,omitempty"` | No |
| BranchID | `string` | `"branch_id,omitempty"` | No |

**12. TaskPostedPayload** (`event/event.go:616`)

| Field | Go type | JSON tag | Required? |
|---|---|---|---|
| TaskID | `string` | `"task_id"` | Yes |
| PosterID | `string` | `"poster_id"` | Yes |
| Title | `string` | `"title"` | Yes |
| Description | `string` | `"description"` | Yes |
| Category | `string` | `"category"` | Yes |
| Budget | `uint64` | `"budget"` | Yes |
| DeliveryMethod | `string` | `"delivery_method,omitempty"` | No |
| SuccessCriteria | `[]string` | `"success_criteria,omitempty"` | No |
| AssuranceLane | `string` | `"assurance_lane,omitempty"` | No |
| MaxDeliveryTime | `int64` | `"max_delivery_time_secs,omitempty"` | No |

**13. TaskClaimedPayload** (`event/event.go:630`)

| Field | Go type | JSON tag | Required? |
|---|---|---|---|
| TaskID | `string` | `"task_id"` | Yes |
| ClaimerID | `string` | `"claimer_id"` | Yes |

**14. TaskSubmittedPayload** (`event/event.go:636`)

| Field | Go type | JSON tag | Required? |
|---|---|---|---|
| TaskID | `string` | `"task_id"` | Yes |
| ClaimerID | `string` | `"claimer_id"` | Yes |
| ResultHash | `string` | `"result_hash"` | Yes |
| ResultNote | `string` | `"result_note,omitempty"` | No |
| ResultURI | `string` | `"result_uri,omitempty"` | No |

**15. TaskApprovedPayload** (`event/event.go:645`)

| Field | Go type | JSON tag | Required? |
|---|---|---|---|
| TaskID | `string` | `"task_id"` | Yes |
| ApproverID | `string` | `"approver_id"` | Yes |

**16. TaskDisputedPayload** (`event/event.go:651`)

| Field | Go type | JSON tag | Required? |
|---|---|---|---|
| TaskID | `string` | `"task_id"` | Yes |
| PosterID | `string` | `"poster_id"` | Yes |

**17. ValidatorGenesisSetPayload** (`validatorlifecycle/events.go:64`)

| Field | Go type | JSON tag | Required? |
|---|---|---|---|
| Seats | `[]GenesisSeatEntry` | `"seats"` | Yes (len >= 1) |
| EffectiveFromVersion | `uint64` | `"effective_from_version"` | Yes |

*Nested:* **GenesisSeatEntry** (`validatorlifecycle/events.go:46`)

| Field | Go type | JSON tag |
|---|---|---|
| ValidatorID | `ValidatorID` (string) | `"validator_id"` |
| OperatorAgentID | `crypto.AgentID` (string) | `"operator_agent_id"` |
| ConsensusPublicKey | `crypto.AgentID` (string) | `"consensus_public_key"` |
| BondedStake | `uint64` | `"bonded_stake"` |

**18. ValidatorJoinPayload** (`validatorlifecycle/events.go:109`)

| Field | Go type | JSON tag | Required? |
|---|---|---|---|
| ValidatorID | `ValidatorID` (string) | `"validator_id"` | Yes |
| OperatorAgentID | `crypto.AgentID` (string) | `"operator_agent_id"` | Yes |
| ConsensusPublicKey | `crypto.AgentID` (string) | `"consensus_public_key"` | Yes |
| KeyEpoch | `uint64` | `"key_epoch"` | Yes (> 0) |
| BondedStake | `uint64` | `"bonded_stake"` | Yes (> 0) |
| EffectiveFromVersion | `uint64` | `"effective_from_version"` | Yes |

**19. ValidatorActivatePayload** (`validatorlifecycle/events.go:159`)

| Field | Go type | JSON tag | Required? |
|---|---|---|---|
| ValidatorID | `ValidatorID` (string) | `"validator_id"` | Yes |
| EffectiveFromVersion | `uint64` | `"effective_from_version"` | Yes |

**20. ValidatorSuspendPayload** (`validatorlifecycle/events.go:180`)

| Field | Go type | JSON tag | Required? |
|---|---|---|---|
| ValidatorID | `ValidatorID` (string) | `"validator_id"` | Yes |
| Reason | `string` | `"reason"` | Yes |
| EvidenceRef | `string` | `"evidence_ref,omitempty"` | No |
| EffectiveFromVersion | `uint64` | `"effective_from_version"` | Yes |

**21. ValidatorResumePayload** (`validatorlifecycle/events.go:206`)

| Field | Go type | JSON tag | Required? |
|---|---|---|---|
| ValidatorID | `ValidatorID` (string) | `"validator_id"` | Yes |
| EffectiveFromVersion | `uint64` | `"effective_from_version"` | Yes |

**22. ValidatorExitPayload** (`validatorlifecycle/events.go:238`)

| Field | Go type | JSON tag | Required? |
|---|---|---|---|
| ValidatorID | `ValidatorID` (string) | `"validator_id"` | Yes |
| Phase | `ExitPhase` (string) | `"phase"` | Yes |
| CooldownDuration | `uint64` | `"cooldown_duration,omitempty"` | Conditional (required if phase=begin_cooldown) |
| EffectiveFromVersion | `uint64` | `"effective_from_version"` | Yes |

**23. ValidatorKeyRotatePayload** (`validatorlifecycle/events.go:267`)

| Field | Go type | JSON tag | Required? |
|---|---|---|---|
| ValidatorID | `ValidatorID` (string) | `"validator_id"` | Yes |
| OldConsensusKey | `crypto.AgentID` (string) | `"old_consensus_key"` | Yes |
| NewConsensusKey | `crypto.AgentID` (string) | `"new_consensus_key"` | Yes |
| OldKeyEpoch | `uint64` | `"old_key_epoch"` | Yes (> 0) |
| NewKeyEpoch | `uint64` | `"new_key_epoch"` | Yes (> old) |
| EffectiveFromVersion | `uint64` | `"effective_from_version"` | Yes |

**24. ValidatorSlashAppliedPayload** (`validatorlifecycle/events.go:303`)

| Field | Go type | JSON tag | Required? |
|---|---|---|---|
| ValidatorID | `ValidatorID` (string) | `"validator_id"` | Yes |
| Offense | `string` | `"offense"` | Yes |
| EvidenceRef | `string` | `"evidence_ref"` | Yes |
| SlashPercent | **`float64`** | `"slash_percent"` | Yes (0, 100] |
| SlashAmount | `uint64` | `"slash_amount"` | Yes |
| RemainingStake | `uint64` | `"remaining_stake"` | Yes |
| PermanentExclusion | `bool` | `"permanent_exclusion"` | Yes |
| CooldownDuration | `uint64` | `"cooldown_duration,omitempty"` | No |
| Reason | `string` | `"reason"` | Yes |
| EffectiveFromVersion | `uint64` | `"effective_from_version"` | Yes |

---

## Task 2: Schema Version Check

### Does any payload carry a schema version field?

**No.** None of the 24 payload structs contain a `version`, `schema_version`, or `payload_version` field. The only version field in the protocol is the `"version"` header in the AETHERNET-TX-V1 signing envelope (which identifies the signing scheme, not the payload schema).

### Forward compatibility: unknown fields

Go's `encoding/json` unmarshaler **silently ignores** unknown JSON fields by default. This means:

- **New field added to payload struct in newer code:** Old nodes parsing the new event will silently drop the unknown field. The event will deserialize successfully with the new field's zero value.
- **This is safe for reads** but **dangerous for canonical hashing**: if an old node receives an event created by a new node, the old node's `json.Marshal` of the deserialized payload will **omit the unknown field**, producing different canonical bytes than the originating node computed. This means the old node will compute a different EventID for the same event.

### Backward compatibility: missing fields

If a new node expects a field that an old event doesn't have:

- `omitempty` fields: marshal as absent, unmarshal as zero value. Safe.
- Non-`omitempty` fields: marshal as zero value (`""`, `0`, `false`). The field will be present with its zero value in canonical bytes. Safe as long as zero value is semantically valid.

### Impact assessment

| Scenario | Behavior | Risk |
|---|---|---|
| New node reads old event (missing new field) | Zero value used | Low — if zero value is invalid, validation catches it |
| Old node reads new event (extra unknown field) | Silently dropped | **HIGH** — EventID recomputation will differ |
| Old node relays new event (Payload is json.RawMessage) | Preserved verbatim | Safe — raw bytes pass through |
| Old node recomputes EventID of new event | Different hash | **HIGH** — breaks DAG integrity checks |

**Key insight:** Because `Payload` is stored as `json.RawMessage`, old nodes that simply relay events without re-marshaling the payload are safe. The risk only materializes if an old node deserializes and re-serializes a payload (e.g., during `GetPayload[T]` followed by re-creation). The `ComputeID` function uses the `json.RawMessage` directly, so EventID verification of relayed events is safe.

---

## Task 3: EventID Derivation

### Canonical projection

EventIDs are computed from a strict subset of Event fields. The projection is defined in `eventCanonical` (`event/event.go:236`):

```go
type eventCanonical struct {
    Type            EventType       `json:"type"`
    CausalRefs      []EventID       `json:"causal_refs"`
    Payload         json.RawMessage `json:"payload"`
    AgentID         string          `json:"agent_id"`
    CausalTimestamp uint64          `json:"causal_timestamp"`
    StakeAmount     uint64          `json:"stake_amount"`
}
```

### Fields included in hash

| Field | Why included |
|---|---|
| Type | Determines semantic meaning |
| CausalRefs | Causal edges — defines position in DAG |
| Payload | The actual content (pre-marshaled JSON bytes) |
| AgentID | Author attribution |
| CausalTimestamp | Lamport clock position |
| StakeAmount | Economic commitment |

### Fields excluded from hash

| Field | Why excluded |
|---|---|
| ID | Circular — this is what we're computing |
| Signature | Circular — signing happens after hashing |
| SettlementState | Mutable — changes from Optimistic to Settled/Adjusted post-creation |

### Hash function and algorithm

1. Construct `eventCanonical` struct from Event fields
2. `json.Marshal(eventCanonical)` — standard Go JSON marshaling
3. `sha256.Sum256(marshaledBytes)`
4. `hex.EncodeToString(hash[:])` — lowercase hex

### Code path

```
event.New()                          // event/event.go:287
  → json.Marshal(payload)           // Pre-marshal payload to json.RawMessage (line 322)
  → ComputeID(e)                    // event/event.go:248
    → json.Marshal(eventCanonical)  // Standard Go marshaling (line 258)
    → sha256.Sum256(data)           // SHA-256 of marshaled bytes (line 261)
    → hex.EncodeToString(sum[:])    // Hex-encode (line 262)
```

### Critical observation: NOT using JCS for EventID

**EventID computation uses `json.Marshal`, NOT `CanonicalizeJSON` (JCS).** Go's `json.Marshal` produces deterministic output for these specific struct types because:

- All fields have concrete types (no `interface{}`)
- Struct field order is defined by the Go struct definition
- `json.RawMessage` Payload bytes are passed through verbatim

However, this is **not** RFC 8785 compliant — it relies on Go's struct-ordered marshaling rather than lexicographic key sorting. The output happens to be deterministic because:
- Go marshals struct fields in declaration order (always the same)
- `json.RawMessage` passes through unchanged
- No maps are involved

**Risk:** If the `eventCanonical` struct field order were ever reordered, all historical EventIDs would become invalid. The struct declaration order IS the canonical order. This should be documented as a frozen invariant.

---

## Task 4: Serialization Audit

### Fields using `interface{}` or `map[string]interface{}`

**None in any payload struct.** The `event.New()` function accepts `payload interface{}` as a parameter, but this is immediately marshaled to `json.RawMessage` at creation time (`event/event.go:314-328`). No payload struct field uses `interface{}` or `map[string]interface{}`.

Historical note: a comment at `event/event.go:313` references a past fix: *"This is the fix for the JSON non-determinism bug where interface{} payloads produced different canonical bytes after a network round-trip."* The migration to `json.RawMessage` resolved this.

### Fields using `float64`

| Payload | Field | JSON tag | Risk level |
|---|---|---|---|
| `AttestationPayload` | `ClaimedAccuracy` | `"claimed_accuracy"` | **HIGH** — participates in canonical hash via Payload |
| `TrajectoryCommitPayload` | `QualityScore` | `"quality_score"` | **HIGH** — participates in canonical hash via Payload |
| `TaskSettlementPayload` | `Score` | `"score"` | **HIGH** — participates in canonical hash via Payload |
| `ValidatorSlashAppliedPayload` | `SlashPercent` | `"slash_percent"` | **HIGH** — participates in canonical hash via Payload |

All four float64 fields participate in the EventID hash through the Payload bytes. Float serialization edge cases:

- `0.0` vs `0` vs `-0`: Go's `json.Marshal` always produces `0` for float64(0), which is safe.
- `1.0` vs `1`: Go produces `1`, Python produces `1.0` for `float(1)` — **cross-language divergence risk**.
- Very large/small values: scientific notation (`1e20`) — Go and Python may format differently.
- `NaN`, `Infinity`: Go's `json.Marshal` returns an error for these. Not reachable through validation but not defensively guarded at the serialization layer.

**Specific risks per field:**
- `ClaimedAccuracy`: values in [0.0, 1.0]. Values like `0.5` serialize consistently. Values like `1.0` may serialize as `1` (Go) vs `1.0` (Python).
- `QualityScore`: same range and risk as ClaimedAccuracy.
- `Score`: used in task settlement. Created server-side only (auto-validator), so cross-language risk is lower. But round-trip through network sync still applies.
- `SlashPercent`: values in (0, 100]. Integer percentages like `50` would serialize as `50` in Go, `50.0` in Python.

### Custom MarshalJSON/UnmarshalJSON on payload types

**None.** No payload struct defines custom JSON marshaling. All use Go's default struct marshaling.

`time.Time` (in `DelegationPayload.ExpiresAt`) uses Go's default `time.Time.MarshalJSON()` which produces RFC 3339 format (`"2006-01-02T15:04:05.999999999Z07:00"`). This is a standard format but the sub-second precision and timezone suffix can vary — see serialization concerns in Task 6.

### `[]byte` fields

`RegistrationPayload.PublicKey` is `[]byte`, which Go's `json.Marshal` encodes as **base64**. Cross-language implementations must use base64 encoding for this field.

---

## Task 5: Cross-Language Parity Check

The Python SDK (`sdk/python/aethernet/client.py`) communicates via REST API — it sends HTTP requests that the Go API server processes into events. The SDK does NOT construct event payloads directly; the server does. This is an important distinction.

### Mapping: Python SDK methods to Go event types

| Python SDK method | HTTP endpoint | Go event type created server-side |
|---|---|---|
| `register()` | `POST /v1/agents` | `Registration` |
| `transfer()` | `POST /v1/transfer` | `Transfer` |
| `generate()` | `POST /v1/generation` | `Generation` |
| `verify()` | `POST /v1/verify` | `Verification` |
| `post_task()` | `POST /v1/tasks` | `TaskPosted` |
| `claim_task()` | `POST /v1/tasks/{id}/claim` | `TaskClaimed` |
| `submit_task_result()` | `POST /v1/tasks/{id}/submit` | `TaskSubmitted` |
| `approve_task()` | `POST /v1/tasks/{id}/approve` | `TaskApproved` |
| `dispute_task()` | `POST /v1/tasks/{id}/dispute` | `TaskDisputed` |
| `emit_trajectory_commit()` | `POST /v1/tasks/{id}/trajectory/commit` | `TrajectoryCommit` |

### Field name parity analysis

**Transfer:** Python sends `to_agent`, `amount`, `currency`, `memo`, `stake_amount`, `from_agent`, `causal_refs`. Go's `TransferPayload` has `from_agent`, `to_agent`, `amount`, `currency`, `memo`, `reason`, `task_id`. **Note:** Python sends `stake_amount` and `causal_refs` which are Event-level fields, not payload fields. The API server separates these. Python does NOT send `reason` or `task_id` — these are set server-side for internal transfers (escrow, fees).

**Generation:** Python sends `claimed_value`, `evidence_hash`, `task_description`, `stake_amount`, `beneficiary_agent`, `causal_refs`. Go's `GenerationPayload` has `generating_agent`, `beneficiary_agent`, `claimed_value`, `evidence_hash`, `task_description`. `generating_agent` is set server-side from the authenticated actor. Match is correct.

**Task posting:** Python sends `title`, `description`, `category`, `budget`, `delivery_method`, `poster_id`, `success_criteria`, plus additional fields: `required_checks`, `policy_version`, `challenge_window_secs`, `generation_eligible`, `max_delivery_time_secs`, `assurance_lane`. Go's `TaskPostedPayload` does NOT have `required_checks`, `policy_version`, `challenge_window_secs`, or `generation_eligible`. These are API-layer parameters consumed by the server but NOT stored in the DAG event payload.

**Task submission:** Python sends `evidence` (dict), `result_hash`, `result_note`, `result_uri`, `result_content`, `result_encrypted`, `claimer_id`. Go's `TaskSubmittedPayload` has `task_id`, `claimer_id`, `result_hash`, `result_note`, `result_uri`. **Note:** `result_content`, `result_encrypted`, and `evidence` are API-layer fields processed by the server but NOT stored in the DAG event payload. The event stores only the hash reference.

**Trajectory commit:** Python sends `outcome`, `approach_description`, `compute_cost`, `quality_score`, `parent_commit_id`, `parameters`, `evidence_snippet`, `error_detail`, `intermediate_output_hash`, `category_hint`, `branch_id`. Go's `TrajectoryCommitPayload` has `task_id`, `parent_commit_id`, `outcome`, `checkpoint_hash`, `checkpoint_size`, `compute_cost`, `quality_score`, `category_hint`, `branch_id`. **Note:** Python sends `approach_description`, `parameters`, `evidence_snippet`, `error_detail`, `intermediate_output_hash` — these are blobstore fields. The server creates a `CheckpointBody`, stores it in the blobstore, and computes `checkpoint_hash` and `checkpoint_size` for the DAG event payload. Python's extra fields become the checkpoint blob, not the DAG payload.

### Parity verdict

The Python SDK is correctly designed as an API client, not a direct event constructor. The server is the authoritative event creator. Field name mismatches between Python request bodies and Go payload structs are intentional — the API layer translates between the two. **No parity bugs found.**

### Fields Python sends that Go payload structs don't have

These are API-layer parameters, not event payload fields:
- `stake_amount` (event envelope, not payload)
- `causal_refs` (event envelope, not payload)
- `required_checks`, `policy_version`, `challenge_window_secs`, `generation_eligible` (task configuration)
- `result_content`, `result_encrypted`, `evidence` (delivery/blobstore)
- `approach_description`, `parameters`, `evidence_snippet`, `error_detail`, `intermediate_output_hash` (checkpoint blob)

### Event types NOT exposed by Python SDK

The following event types are server-internal only (no Python SDK method):
- `Attestation`
- `Delegation`
- `VerificationVote`
- `Settlement`
- `TaskSettlement`
- `GenesisFunding`
- All 8 `Validator*` event types

This is correct — these are protocol-internal events, not user-facing operations.

---

## Task 6: Gap Analysis

### GAPS THAT BLOCK PROTOCOL FREEZE

**1. No payload schema version field**

None of the 24 payload structs carry a version field. After protocol freeze, any payload field addition, removal, or type change breaks backward compatibility with no mechanism for nodes to detect the incompatibility. A version field (`schema_version uint64`) on each payload — or a single protocol-wide version on the Event envelope — is required before freeze.

**Recommendation:** Add `schema_version uint64` to the `Event` struct (not individual payloads). Include it in the `eventCanonical` hash. This provides a single mechanism for all event types.

**2. Four float64 fields need concrete alternatives**

| Payload | Field | Current type | Proposed replacement |
|---|---|---|---|
| `AttestationPayload` | `ClaimedAccuracy` | `float64` | `uint32` basis points (0-10000 = 0.00%-100.00%) |
| `TrajectoryCommitPayload` | `QualityScore` | `float64` | `uint32` basis points (0-10000) |
| `TaskSettlementPayload` | `Score` | `float64` | `uint32` basis points (0-10000) |
| `ValidatorSlashAppliedPayload` | `SlashPercent` | `float64` | `uint32` basis points (0-10000) |

Floating-point serialization is non-deterministic across languages and JSON libraries. Go's `json.Marshal(1.0)` produces `1`, Python's `json.dumps(1.0)` produces `1.0` — different canonical bytes for the same semantic value. Basis points (integer representation of percentages) eliminate this entire class of bugs.

**3. `DelegationPayload.ExpiresAt` uses `time.Time`**

`time.Time` has a custom `MarshalJSON` that produces RFC 3339 with variable-precision nanoseconds. Different Go versions, or different construction paths (`time.Now()` vs `time.Unix()`), can produce different string representations for the same instant:
- `"2024-01-01T00:00:00Z"` vs `"2024-01-01T00:00:00.000000000Z"`
- Timezone: `+00:00` vs `Z`

**Recommendation:** Replace with `int64` Unix seconds. Wall-clock expiry does not need sub-second precision.

**4. EventID uses `json.Marshal`, not JCS**

`ComputeID` uses `json.Marshal(eventCanonical)` which produces field-order-dependent output. The canonical hash depends on the Go struct field declaration order of `eventCanonical`. This is deterministic within a single Go binary but:
- A non-Go implementation must replicate Go's struct field ordering, not JCS key sorting.
- Reordering `eventCanonical` fields would silently break all historical EventIDs.

**Recommendation:** Either (a) switch to JCS for EventID computation (breaking change to all existing EventIDs), or (b) document the exact field order as a frozen protocol invariant and add a test that asserts it. Option (b) is lower-risk if historical EventIDs must be preserved.

**5. `RegistrationPayload.PublicKey` is `[]byte` (base64)**

Go's `json.Marshal` encodes `[]byte` as base64. This is implicit and undocumented. A non-Go implementation must know to use base64 for this specific field. All other key fields (`AgentID`, `OperatorAgentID`, `ConsensusPublicKey`) use hex-encoded strings.

**Recommendation:** Change to `string` with explicit hex encoding, matching the convention used everywhere else in the protocol.

### RISKS THAT SHOULD BE ADDRESSED BEFORE MAINNET

**1. No explicit null/empty distinction for `omitempty` fields**

Seven payload fields use `omitempty`: `Memo`, `Reason`, `TaskID`, `DeliveryMethod`, `SuccessCriteria`, `AssuranceLane`, `MaxDeliveryTime`, `ParentCommitID`, `CategoryHint`, `BranchID`, `EvidenceRef`, `CooldownDuration`, `ResultNote`, `ResultURI`. With `omitempty`, an empty string (`""`) and an absent field produce different JSON (`{"memo":""}` vs no key). After a deserialize/re-serialize round-trip, the empty string becomes absent (omitted), changing canonical bytes. This is safe only if these fields are never set to their zero values intentionally. Document which zero values are semantically valid.

**2. `SettlementPayload.Attestations` ordering**

`SortAttestations()` sorts by `VoterID` for deterministic serialization, but this must be called before marshaling. If any code path marshals without sorting, the canonical bytes will differ. Consider making sorting automatic (in a custom MarshalJSON or in the event creation path) rather than requiring callers to remember.

**3. No `TaskCancelled` event type or payload**

The Python SDK has a `cancel_task()` method that hits `POST /v1/tasks/{id}/cancel`, but there is no `EventTypeTaskCancelled` constant or `TaskCancelledPayload` struct. Task cancellation is either (a) handled purely at the API layer without a DAG event, or (b) missing. If cancellation should be auditable on-chain, a DAG event type is needed before freeze.

**4. Verdict field type inconsistency**

- `VerificationPayload.Verdict` is `bool` (true/false)
- `VerificationVotePayload.Verdict` is `string` (SettlementVerdict: "accepted"/"rejected"/"adjusted")
- `SettlementPayload.Verdict` is `string` (same)

The same concept ("verdict") has two different types across the protocol. Not a correctness bug (they're on different event types) but a source of confusion. Consider renaming one to avoid ambiguity (e.g., `Outcome` for the SettlementVerdict string).

**5. `VerificationVotePayload.Timestamp` is wall-clock `int64`**

This is the only payload field using wall-clock time aside from `DelegationPayload.ExpiresAt`. The Event already has `CausalTimestamp` (Lamport clock). If this wall-clock timestamp is used for ordering or deduplication, it's a source of clock-skew bugs. If it's purely informational, document it as such and consider making it optional.

### THINGS THAT ARE ALREADY SOLID

**1. `json.RawMessage` for Payload storage**

The Payload field uses `json.RawMessage`, which preserves exact bytes across serialization/deserialization boundaries. This was a deliberate fix for a prior non-determinism bug and is the correct design. Events created by `event.New()` pre-marshal the payload once at creation time, and those exact bytes are used for all subsequent operations (canonical hash, network relay, storage).

**2. Well-typed payload structs for all 24 event types**

Every event type has a concrete, named payload struct with explicit field types and JSON tags. No event type uses a raw `map[string]interface{}` as its payload. This is a strong foundation for protocol freeze.

**3. Canonical hash exclusion set is correct**

The three fields excluded from the hash (ID, Signature, SettlementState) are precisely the fields that must be excluded:
- ID: circular
- Signature: circular
- SettlementState: mutable

No fields that should be immutable are excluded.

**4. CausalRefs normalization**

`event.New()` normalizes `nil` CausalRefs to `[]EventID{}` (empty slice, not null). This ensures `json.Marshal` produces `"causal_refs":[]` rather than `"causal_refs":null`, which would be a canonical bytes divergence.

**5. Validator lifecycle payloads have comprehensive validation**

All 8 validator lifecycle payload types have explicit `Validate()` methods that check required fields and semantic constraints. These are called during DAG ingestion via `ExtractLifecycleEvent`.

**6. Cross-language canonicalization is verified**

The JCS implementation (`internal/auth/canonical.go`) is tested against hardcoded cross-language vectors (`internal/auth/canonical_test.go`, `sdk/python/tests/test_canonical_crosslang.py`). Both Go and Python produce identical canonical bytes for all test vectors. However, note that JCS is used for TX-V1 signing, NOT for EventID computation — these are separate canonicalization paths.

**7. SettlementPayload has deterministic attestation ordering**

`SortAttestations()` sorts by VoterID, ensuring deterministic serialization. The mechanism is correct; the risk (noted above) is only that callers must remember to call it.

**8. Integer µAET denomination throughout**

All monetary values (`Amount`, `ClaimedValue`, `Budget`, `StakeAmount`, `BondedStake`, `VerifiedValue`, `SlashAmount`, `RemainingStake`, `ComputeCost`, `SpendingLimit`) use `uint64` with µAET denomination. No floating-point money. This is correct and eliminates an entire class of rounding bugs.

---

## Summary of Action Items (Priority Order)

| # | Action | Severity | Effort |
|---|---|---|---|
| 1 | Replace 4x float64 fields with uint32 basis points | **Blocks freeze** | Medium |
| 2 | Add schema_version to Event envelope | **Blocks freeze** | Low |
| 3 | Replace `DelegationPayload.ExpiresAt` time.Time with int64 Unix seconds | **Blocks freeze** | Low |
| 4 | Change `RegistrationPayload.PublicKey` from []byte to hex string | **Blocks freeze** | Low |
| 5 | Document or switch EventID canonicalization (struct order vs JCS) | **Blocks freeze** | Low (document) / High (switch) |
| 6 | Add EventTypeTaskCancelled + payload if cancellation is on-chain | Pre-mainnet | Low |
| 7 | Make `SortAttestations()` automatic | Pre-mainnet | Low |
| 8 | Audit `omitempty` fields for zero-value semantics | Pre-mainnet | Low |
| 9 | Align verdict field naming across event types | Pre-mainnet | Low |
| 10 | Clarify `VerificationVotePayload.Timestamp` purpose | Pre-mainnet | Low |
