# Settlement-event selection race — characterization

**Discovered**: 2026-04-22, during Part F retry Phase C-sanity, on the 5-node AWS testnet.
**Frozen-state evidence**: `/tmp/part-f-retry-snapshots/` (deploy logs, ledger snapshots, activation responses, log greps preserved as-is).
**Branch context**: `feat/canonical-distribution-integer-migration` @ commit `d6196ed`. Testnet running `integer-migration-part-e1-d6196ed` since 2026-04-22 19:50 UTC.
**Scope**: this document characterizes a cross-node consensus-application bug. It does not design a fix.

---

## 1. Executive summary

When a single `TaskVerificationRound` finalizes near a vote-weight tie, every validator's local `DeadlineChecker` (or vote-consumer's supermajority-detection path) independently calls `EmitConsensusEvent(round, ...)` with that node's *local view* of `round.FinalVerdict` at finalization time. Because each validator computes its `FinalVerdict` from votes received in local fastpath order, and arrival order varies across nodes, **multiple semantically-distinct `TaskVerificationConsensus` events with conflicting `FinalVerdict` fields can exist in the DAG for the same `RoundID`**.

All 5 nodes ingest all of these events (DAG agrees byte-for-byte). On each node, the dispatcher's per-task mutex (`TVConsensusConsumer.taskMu(taskID)` at `internal/dispatch/tv_consensus_consumer.go:63-66`) ensures that exactly one such event invokes the settler locally — but **which one** is determined by per-node fastpath arrival order. The first event past the per-task mutex sets the task to terminal status; subsequent events with different verdicts short-circuit on the task-status-terminal pre-check (`tv_consensus_consumer.go:144-149`) and produce no further mutation.

Result: per node, settlement is internally consistent (the event the local mutex chose is the event that ran). **Across nodes, settlement diverges**: Node A applies an "accept" event, Node B applies a "reject" event for the same task. The ledger forks. F3-B's per-task mutex serializes Apply *locally* but does not select a *cluster-uniform* event, so the deduplication is per-node, not per-cluster.

The bug is a violation of the implicit invariant **"given the same DAG of canonical events, all correct nodes must compute the same projected ledger state."** It has been live on this testnet at least since the F3-B-supposed-fix shipped (and possibly longer); ~50 AET of pre-existing validator-stake divergence on the cluster suggests the same mechanism affecting other event types over a longer timeline.

---

## 2. The evidence — direct log lines

All times UTC, 2026-04-22. Captured from `sudo docker logs aethernet` on each node, retrieved at 23:08 UTC immediately after the 3 sanity-task settlements.

### 2.1 Task `d0c0bd21517101726f1b4a3bd06cdaf2` (sanity_1, small/100,000 µAET) — *no race observed*

| Node | Settlement log line |
|---|---|
| 1 | `23:00:35 INFO verification_settler: accept settled task_id=d0c0bd21... budget=100000 worker=73000 validator_pool=23000 gen_ledger=2000 treasury=2000 agreeing_validators=2` |
| 2 | `23:00:35 INFO verification_settler: accept settled task_id=d0c0bd21... budget=100000 worker=73000 validator_pool=23000 gen_ledger=2000 treasury=2000 agreeing_validators=2` |
| 3 | `23:00:35 INFO verification_settler: accept settled task_id=d0c0bd21... budget=100000 worker=73000 validator_pool=23000 gen_ledger=2000 treasury=2000 agreeing_validators=2` |
| 4 | `23:00:35 INFO verification_settler: accept settled task_id=d0c0bd21... budget=100000 worker=73000 validator_pool=23000 gen_ledger=2000 treasury=2000 agreeing_validators=2` |
| 5 | `23:00:35 INFO verification_settler: accept settled task_id=d0c0bd21... budget=100000 worker=73000 validator_pool=23000 gen_ledger=2000 treasury=2000 agreeing_validators=2` |

Per-node TVConsensus event emit count: N1=1, N2=0, N3=1, N4=0, N5=1 — three TVConsensus events for this round. All three carried `FinalVerdict="pass"` (inferable from the unanimous "accept" settlement). Whichever event won each node's mutex, the settlement was identical. No divergence.

### 2.2 Task `77c2bdf19b5ca0f46b15d2c35dc31fd6` (sanity_2, small/100,000 µAET) — race observed

| Node | Settlement log line |
|---|---|
| 1 | `23:00:55 INFO verification_settler: accept settled task_id=77c2bdf1... budget=100000 worker=73000 validator_pool=23000 gen_ledger=2000 treasury=2000 agreeing_validators=4` |
| **2** | **`23:00:55 INFO verification_settler: reject settled task_id=77c2bdf1... budget=100000 poster=73000 treasury=4000 agreeing_validators=4`** |
| 3 | `23:00:55 INFO verification_settler: accept settled task_id=77c2bdf1... budget=100000 worker=73000 validator_pool=23000 gen_ledger=2000 treasury=2000 agreeing_validators=4` |
| 4 | `23:00:55 INFO verification_settler: accept settled task_id=77c2bdf1... budget=100000 worker=73000 validator_pool=23000 gen_ledger=2000 treasury=2000 agreeing_validators=4` |
| 5 | `23:00:55 INFO verification_settler: accept settled task_id=77c2bdf1... budget=100000 worker=73000 validator_pool=23000 gen_ledger=2000 treasury=2000 agreeing_validators=4` |

Per-node TVConsensus event emit count: N1=1, N2=1, N3=1, N4=1, N5=2 — six TVConsensus events for this round. At least one carried `FinalVerdict="fail"` (the one Node 2 selected); at least one carried `FinalVerdict="pass"` (the one Nodes 1, 3, 4, 5 selected).

Both settlement paths report `agreeing_validators=4`, which the protocol treats as "consensus reached." But the consensus is on different verdicts.

### 2.3 Task `4ba459c6f9393ed817db74230cc88e3b` (sanity_3, medium/10,000,000 µAET) — race observed at scale

| Node | Settlement log line |
|---|---|
| 1 | `23:01:00 INFO verification_settler: accept settled task_id=4ba459c6... budget=10000000 worker=7300000 validator_pool=2300000 gen_ledger=200000 treasury=200000 agreeing_validators=4` |
| **2** | **`23:01:00 INFO verification_settler: reject settled task_id=4ba459c6... budget=10000000 poster=7300000 treasury=400000 agreeing_validators=4`** |
| 3 | `23:01:00 INFO verification_settler: accept settled task_id=4ba459c6... budget=10000000 worker=7300000 validator_pool=2300000 gen_ledger=200000 treasury=200000 agreeing_validators=4` |
| 4 | `23:01:00 INFO verification_settler: accept settled task_id=4ba459c6... budget=10000000 worker=7300000 validator_pool=2300000 gen_ledger=200000 treasury=200000 agreeing_validators=4` |
| 5 | `23:01:00 INFO verification_settler: accept settled task_id=4ba459c6... budget=10000000 worker=7300000 validator_pool=2300000 gen_ledger=200000 treasury=200000 agreeing_validators=4` |

Per-node TVConsensus event emit count: N1=1, N2=2, N3=0, N4=2, N5=2 — seven TVConsensus events for this round. Same pattern: Node 2 selected a "fail"-verdict event; others selected "pass"-verdict events.

### 2.4 Vote-weight evidence for round `ec1c04b3...` (the 4ba459c6 round)

Excerpt of `task_verification_vote: vote applied` log lines for this round (Node 1's view, identical on others):

```
05adbeb pass score_bp=5601 family=statistical_structural pass_weight=50000000000  fail_weight=0
05adbeb fail score_bp=4874 family=embedding_similarity   pass_weight=0           fail_weight=50000000000
d4cfec  pass score_bp=7900 family=deterministic_heuristic pass_weight=100000000000 fail_weight=50000000000
d4cfec  fail score_bp=4874 family=embedding_similarity   pass_weight=100000000000 fail_weight=100000000000
5df098  pass score_bp=5601 family=statistical_structural pass_weight=150000000000 fail_weight=100000000000
5df098  fail score_bp=4874 family=embedding_similarity   pass_weight=150000000000 fail_weight=150000000000
741225  pass score_bp=7900 family=deterministic_heuristic pass_weight=200000000000 fail_weight=150000000000
741225  fail score_bp=4874 family=embedding_similarity   pass_weight=150000000000 fail_weight=200000000000   ← post-finalization audit
```

Final state: pass_weight = 150 vs fail_weight = 200 (after the last vote). Both crossed the supermajority threshold during the round at different moments, which is why multiple validators independently called `EmitConsensusEvent` with potentially different `FinalVerdict` snapshots.

### 2.5 Cross-node ledger consequence

`/v1/economics` per node, captured at 23:00 UTC, after the 3 sanity-task settlements:

| Node | treasury_balance |
|---|---:|
| 1 | 100,000,030,096,000 |
| **2** | **100,000,030,300,000** (+204,000 vs Nodes 1, 4, 5) |
| 3 | 100,000,030,098,000 (+2,000 fossil from prior testnet activity) |
| 4 | 100,000,030,096,000 |
| 5 | 100,000,030,096,000 |

`/v1/agents/<id>/balance` per node for poster and worker (operator and retry-worker wallets):

| Agent | N1 | N2 (delta) | N3 (delta) | N4 (delta) | N5 (delta) |
|---|---:|---:|---:|---:|---:|
| operator (poster) `68778249` | 49,846,638,000 | +7,446,000 | +73,000 | 0 | 0 |
| retry-worker (claimer) `e552da94` | 50,007,446,000 | −7,373,000 | 0 | 0 | 0 |

Reading Node 2's column: poster received +7.45M extra (sum of refunded budgets from the 2 reject-path settlements: 73K + 7,300K = 7,373K, plus 73K from the small-task accept that on Node 2 was actually a reject = 7,373K + 73K = 7,446K). Worker lost 7.45M (didn't receive payouts on the 2 reject-path tasks). Treasury accrued 204K extra (reject-path's higher treasury share: 4K + 400K - 2K - 200K = 202K, plus rounding).

**Self-consistent on Node 2**: budget = poster-refund + treasury-share for each reject-settled task. **Inconsistent across cluster**: Nodes 1, 3, 4, 5 paid the worker; Node 2 refunded the poster.

DAG sizes identical: 1108 events on every node.

---

## 3. The mechanism — file:line walkthrough

### 3.1 Where TVConsensus events are emitted

Two emitters, both of which run on every validator node:

**(a) `internal/taskverification/deadline_checker.go:399-423` — `applyFinalization`**:
```go
func (d *DeadlineChecker) applyFinalization(ctx context.Context, round *TaskVerificationRound, decision FinalizationDecision, now int64) {
    targetState := VerdictToState(decision.Verdict)
    if err := round.Transition(targetState, now); err != nil {
        return // already finalized
    }
    round.FinalVerdict = decision.Verdict
    round.FinalScoreBP = decision.FinalScoreBP
    if err := d.rounds.SaveRound(ctx, round); err != nil { ... return }
    EmitConsensusEvent(round, d.publisher, d.kp, d.validatorID, decision.Reason)
    ...
}
```

**(b) `EmitConsensusEvent` at `internal/taskverification/deadline_checker.go:428-503`**:
```go
func EmitConsensusEvent(round *TaskVerificationRound, publisher ConsensusPublisher, kp *crypto.KeyPair, validatorID crypto.AgentID, reason FinalizationReason) {
    ...
    payload := event.TaskVerificationConsensusPayload{
        Version:               1,
        RoundID:               string(round.RoundID),
        TaskID:                round.TaskID,
        ...
        FinalVerdict:          round.FinalVerdict.String(),  // ← per-node-local view
        FinalScoreBP:          round.FinalScoreBP,
        PassWeight:            round.PassWeight,
        FailWeight:            round.FailWeight,
        ...
    }
    ev, err := event.New(
        event.EventTypeTaskVerificationConsensus,
        []event.EventID{round.SubmissionEventID},
        json.RawMessage(payloadBytes),
        string(validatorID),  // ← author = local validator
        nil, 0,
    )
    crypto.SignEvent(ev, kp)
    publisher.Publish(ev)
}
```

Each validator's local `DeadlineChecker` (and the parallel supermajority-detection path in the vote consumer) calls `EmitConsensusEvent` with its own `validatorID` and its own snapshot of `round.FinalVerdict` — which was set at line 404 from `decision.Verdict`, computed locally in the calling code.

The `FinalizationDecision.Verdict` is computed from votes accumulated in `round.Votes`. Votes arrive via fastpath in non-deterministic order. If the supermajority threshold flips when a particular vote arrives, the decision flips with it.

Each emit creates a new canonical event with a unique content-addressed ID (different `validatorID` author + different `FinalVerdict` payload + different `FinalScoreBP` → different hash). All emits propagate to all nodes via fastpath.

### 3.2 Where the per-node selection happens

`internal/dispatch/tv_consensus_consumer.go:130-182` — `TVConsensusConsumer.Apply`:

```go
func (c *TVConsensusConsumer) Apply(ctx context.Context, ev *event.Event) error {
    payload, err := event.GetPayload[event.TaskVerificationConsensusPayload](ev)
    ...
    mu := c.taskMu(payload.TaskID)
    mu.Lock()
    defer mu.Unlock()

    // Pre-check 1: task already in a terminal status.
    if task, taskErr := c.taskMgr.Get(payload.TaskID); taskErr == nil {
        switch task.Status {
        case tasks.TaskStatusCompleted, tasks.TaskStatusRejected,
            tasks.TaskStatusDisputedResolved, tasks.TaskStatusCancelled:
            return nil  // ← short-circuit on terminal
        }
    }

    // Pre-check 2: escrow entry mid-settlement.
    if c.escrowMgr.HasSettlementStarted(payload.TaskID) {
        return nil
    }

    roundID := taskverification.RoundID(payload.RoundID)
    round, err := c.rounds.LoadRound(ctx, roundID)
    ...
    result, err := c.settler.Settle(ctx, &payload, round)  // ← uses THIS event's payload
    ...
}
```

The flow on each node when multiple TVConsensus events for the same task arrive:

1. Event A (verdict=pass) arrives via fastpath. Bus → admission router → `dispatcher.Admit` → `Interested()` matches → `Apply` invoked.
2. Apply takes `taskMu` for this taskID.
3. Pre-checks: task NOT terminal, escrow NOT started → proceed.
4. `settler.Settle(ctx, &payload, round)` runs with **payload from event A** → applies "accept" settlement, sets task to terminal status.
5. Mutex released.
6. Event B (verdict=fail) arrives next. Bus → router → `dispatcher.Admit` → `Apply` invoked.
7. Apply takes `taskMu` (now released).
8. Pre-check 1: task IS terminal → return nil. Event B never settles.

The "winner" is whichever event arrives at step 1 first on this node. Different nodes see different arrivals first. **Per-node winner ≠ cluster winner.** The integer/float path inside `settler.Settle` is irrelevant — the same code runs on the same payload that won the mutex. The divergence is in **which payload** runs.

### 3.3 The dispatcher does not select a cluster-uniform event

`internal/dispatch/dispatcher.go:99-158` — `Dispatcher.Admit`:

The dispatcher computes an admission key from the event's canonical bytes (`AdmissionKey(ev)`), reserves or loads the admission record, checks prerequisites, and invokes consumers. The admission key is **per-event** (by content hash), not per-(round, task). Two events with different payloads — even for the same `RoundID` — get different admission keys and are admitted independently.

The dispatcher's per-(event, consumer) admission machinery (C-1 through C-16 in the F3-B plan) deduplicates *per event*, not *per task or round*. It guarantees each event's `Apply` runs at most once successfully on this node — it does not guarantee that all 5 nodes pick the same event to apply.

The per-task mutex inside `TVConsensusConsumer.Apply` (introduced in F3-B commit-11) is the only cross-event dedup, and it operates on local state (`task.Status terminal`). That works to prevent local double-application. It does not coordinate across nodes.

---

## 4. Why F3-B did not close this

Read `docs/plans/2026-04-15-settlement-consensus-integrity-fix.md` and the corresponding F3-B work.

### 4.1 What F3-B identified and addressed

F3-B's framing in §1.1: "Layer 2 — F3-B double-settlement race. `TaskVerificationConsensusConsumer` receives the **same canonical event** via two paths on producer nodes (local publish + recognition fabric). The current `round.IsTerminal()` guard fails to deduplicate. Settler runs twice on producer nodes, once on peer nodes, divergent ledger state."

The framing centers on **same canonical event delivered twice via two paths on the producer node**. The dispatcher (Part C of F3-B) closed this by giving each canonical event a single per-(event, consumer) admission record, regardless of how many delivery paths the fabric used. F3-B commit-10/11 added the per-task mutex and the task-terminal pre-check to handle the case of **distinct events for the same task** by short-circuiting the second-and-later events at the task level.

F3-B's invariants C-1 (exactly-once successful application *per consumer per event*) and C-12 (per-(event, consumer) completion tracking) are about per-event identity. The plan's atomic-batch settlement (§4.5) is also per-event: a single canonical event's ledger mutations commit atomically.

### 4.2 What F3-B did not address

The framing assumed that "for a given round, there is *one* canonical TaskVerificationConsensus event" — duplicates were treated as a delivery-path multiplicity (same event, two paths) rather than a multi-emit possibility (different events, one round, conflicting payloads).

Concrete absences:

1. **No cross-event reconciliation for same-round events.** When two events with different `FinalVerdict` exist in the DAG for the same `RoundID`, F3-B has no rule for which one is canonical. The implicit rule is "first one a node admits and applies wins locally."
2. **No suppression of multi-emit at the producer.** `EmitConsensusEvent` is called by every validator's `DeadlineChecker` and supermajority-detection path. There's no guard against a node having already seen a TVConsensus event for the same round before emitting its own.
3. **No canonical selection rule based on event content.** The natural cluster-uniform tiebreaker for "the winning consensus event among multiple emits" would be lexicographic-min event ID, or earliest causal timestamp, or vote-weight-derived deterministic ordering — F3-B does not specify one. The dispatcher just admits each event as it arrives.
4. **Local task-terminal pre-check is per-node, not per-cluster.** The pre-check at `tv_consensus_consumer.go:144-149` is correct local idempotency; it has no mechanism to ensure all 5 nodes' "first event past the mutex" is the same event.

This is not a critique of F3-B's plan or implementation. F3-B was scoped to "Layer 2 double-settlement on the producer node" — the multi-validator-emit scenario was not in scope. The bug surfaced now because Part F's Phase C-sanity drove the system into a vote-weight-tie regime where multi-emit produced multiple distinct verdicts.

### 4.3 What F3-B's testnet verification didn't catch

F3-B's §10 verification involved 10 reject-path tasks across 5 nodes byte-identical. From the historical context (carried in this codebase's F3-B completion report, which described "full convergence"), every node settled every task identically. That was true for that corpus because:

- The corpus was small.
- Worker submissions in that corpus likely produced unambiguous verdicts (analyzers all converged on the same family-level pass/fail signal), so multi-emit with conflicting verdicts didn't fire.

Part F first-attempt's Phase C similarly showed 36/36 settlements byte-identical via shadow_delta, because the float and integer paths converged for those settlements — but shadow_delta only compares within-node float-vs-integer. It does not compare across nodes which event each node chose. The 36/36 cross-node match could have been coincidental: if all 5 nodes happened to admit the same event first for each task, the bug wouldn't fire.

The verification gap: **no F3-B or Part F test queried per-node ledger state for the same task and asserted byte-equality across nodes.** All verification was per-node correctness or per-event determinism. Nothing tested cross-node selection convergence.

---

## 5. The invariant being violated

The implicit invariant:

> **For any DAG D of canonical events shared across all correct nodes, the projected ledger state on every correct node must be byte-equal.**

This is a restatement of Principle 5 ("protocol is the source of truth"). It is the foundational consistency property of any DAG-based consensus protocol. The protocol's content-addressing scheme, deterministic projection model, and replay invariants all assume this property holds.

Documentation status:

- **Implicit in `docs/design-principles.md` Principle 5** — "ledger state is the DAG's projection" — but not stated as a formal cross-node invariant.
- **Implied in F3-B's atomic-batch settlement (§4.5)** — atomic per-event, but cross-event behavior unspecified.
- **Tested in F3-B's §10** — "10 reject-path tasks × 5 nodes byte-identical" — the assertion was made empirically without naming the invariant explicitly.
- **Not stated as an explicit testable invariant anywhere.** No CI test, no conformance suite, no plan document defines "cross-node ledger state byte-equality" as an enforceable property.

Scope of the violation:

- **Per-task verdict divergence** (this finding): ~277,000 µAET across the 3 sanity tasks = ~5% of the affected tasks' total budget mis-allocated cluster-wide.
- **Pre-existing 50 AET stake-state divergence (Divergence A)**: same mechanism is the leading hypothesis. ~50 AET = 0.005% of cluster total supply, but it has accumulated silently across many staking-related events of unknown volume.
- **Time scope**: at minimum since this rehearsal's Phase C-sanity (2026-04-22 23:00 UTC). At maximum since whenever multi-validator-emit-with-tied-weights first occurred on the testnet — possibly weeks.

---

## 6. Relationship to Divergence (A) — the pre-existing 50 AET stake divergence

Snapshot of cross-node validator-stake balances (from Diagnostic 1, captured 2026-04-22 23:00 UTC):

| Validator agent | N1 | N2 | N3 | N4 | N5 |
|---|---:|---:|---:|---:|---:|
| `d839e1` (Node 1's identity) | 25,006,597,462 | **50,006,787,130** | **50,006,595,462** | **50,006,601,294** | 25,006,597,462 |
| `741225d` (Node 2's identity) | 25,001,810,282 | 25,001,235,282 | 25,001,810,282 | 25,001,812,200 | 25,001,810,282 |
| `05adbeb` (Node 3's identity) | **50,008,393,378** | 25,008,545,044 | 25,008,393,378 | **50,008,399,128** | **50,008,393,378** |
| `d4cfec` (Node 4's identity) | 25,006,123,396 | 25,006,123,396 | 25,006,123,396 | 25,006,109,980 | 25,006,123,396 |
| `5df098` (Node 5's identity) | 25,004,273,482 | **50,004,303,148** | **50,004,273,482** | **50,004,275,398** | 25,004,273,482 |

The 25,000,000,000 µAET (25 AET) units fit the genesis validator stake amount (`startStack: genesis validator ready balance=750000000000 staked=50000000000` from each node's startup log — actually this is 50 AET stake; the 25 AET unit fits half-stake or unstake amount). Multiple validators show 25 AET appearing/disappearing across different nodes' views. Total cluster-level over-mintage: 50 AET.

**Hypothesis**: same selection-race mechanism applied to staking events. If a validator stake/unstake operation flows through a multi-emit canonical event scheme similar to TVConsensus (e.g., per-node detection of stake-state-change emits a corresponding event, multi-emit when validators see different state at slightly different moments), per-node first-event-wins selection produces per-node divergent stake state.

**I have not traced a specific stake event to the same level of rigor as the sanity-task settlement evidence above.** The Diagnostic-1 evidence is consistent with the hypothesis but not yet proof. Tracing requires:

- Identifying when staking activity happened (the persistent DAG predates current container logs by weeks; without DAG-event-level historical query infrastructure, the timeline is hard to reconstruct from docker logs alone).
- Locating multi-emit potential in the staking code path. F3-B's plan §11 explicitly says "slashing is out of scope per §11" — the staking path may have similar multi-emit shape to TVConsensus, or it may be different.

This is an open trace for future investigation. The architectural point — "the selection-race mechanism is general; it can affect any canonical event type with multi-emit potential, not just TaskVerificationConsensus" — stands regardless of whether (A) is the same mechanism or a related-but-distinct one.

---

## 7. Fix space — candidate architectural responses

Five candidate responses, each described with a correctness claim, complexity estimate, and tradeoff. **None is recommended here.** The architect session selects.

### 7.1 Single-emitter restriction

**Mechanism**: only one specific validator (e.g., the lexicographically-first active validator at the round's start, or a per-round elected leader) emits the TaskVerificationConsensus event for any given round. Other validators detect supermajority but do not emit.

**Correctness claim**: by construction, exactly one TVConsensus event per round exists in the DAG. Per-task mutex selection becomes trivial because there's only one event to select.

**Complexity**: medium. Requires deterministic election of "the emitter" per round. Vulnerable to the elected emitter being offline or byzantine — needs a fallback after a timeout (which reintroduces multi-emit potential).

**Tradeoff**: simple in the happy path; fragile under emitter failure. The protocol must either accept that a missing emitter delays settlement indefinitely, or accept fallback multi-emit (which reintroduces the problem at lower frequency).

### 7.2 Deterministic event-selection rule per round

**Mechanism**: when multiple TVConsensus events exist for the same `RoundID`, all nodes select the same one via a deterministic rule applied to the event content. Candidates: lexicographically-min event ID; earliest causal timestamp with event-ID tiebreaker; lowest pass_weight − fail_weight margin (the "first" finalizer); or a vote-weight-derived deterministic tiebreaker.

**Correctness claim**: every node, given the same DAG, selects the same event. Per-task mutex behavior becomes "select the canonical winner among events with this `RoundID`, apply only that one."

**Complexity**: medium. Requires changing `TVConsensusConsumer.Apply` (or a layer above it) from "first event wins my mutex" to "look at all events with this RoundID, pick the canonical winner, apply that one." Needs an index on (RoundID → events). Needs a deferral-and-reconsider mechanism: if a node admits event A first but a "smaller" event B arrives later, the node must un-apply A's settlement and apply B's instead, OR defer applying A until enough time has passed for all candidates to arrive.

**Tradeoff**: deferral introduces latency. Un-apply requires reversible settlement (currently absent — atomic-batch is forward-only per F3-B §4.5). Deterministic selection is the *correct* solution architecturally but the most invasive.

### 7.3 Round-level admission with consensus-derived verdict

**Mechanism**: change the dispatcher's admission key from per-event content-hash to per-(RoundID) admission. The "settlement" runs once per RoundID, regardless of how many TVConsensus events are emitted. The verdict applied is derived from the *votes themselves* (already in the round's local state), not from any single TVConsensus event's payload.

**Correctness claim**: settlement is a function of the round's vote-state, which is identical across nodes (votes are canonical events). All nodes compute the same verdict from the same vote-state. TVConsensus events become advisory — they trigger settlement-readiness on each node — but their `FinalVerdict` field is no longer authoritative.

**Complexity**: high. Requires changing the dispatcher's admission key shape from per-event to per-(consumer, key). Requires changing `verification_settler.Settle` to derive verdict from `round.Votes` instead of payload. Requires reasoning about race conditions where the round's vote-state on a node lags the TVConsensus event.

**Tradeoff**: cleanest architectural separation (event = trigger; round-state = source of truth). Most code change. Risks new bugs in vote-aggregation determinism (same selection-race could resurface in vote ingestion if `round.Votes` accumulates differently per node).

### 7.4 Cluster-uniform fastpath ordering

**Mechanism**: ensure that when multiple events for the same logical-key (e.g., RoundID) propagate via fastpath, all nodes admit them in the same order — by adding a deterministic per-key ordering layer above fastpath. The first event admitted per (RoundID, taskID) is the canonical one cluster-wide.

**Correctness claim**: per-node "first event wins" becomes equivalent to "deterministically-first event wins" cluster-wide.

**Complexity**: high. Requires a new ordering layer between fastpath and the dispatcher. Adds latency. May conflict with the fastpath's existing latency-optimizing assumptions.

**Tradeoff**: closes the bug at the transport layer rather than the consensus layer. Most disruptive to networking. Probably overengineered for the actual problem.

### 7.5 Suppress emit on non-author after observation

**Mechanism**: a validator only calls `EmitConsensusEvent` if it has not already observed a TVConsensus event for that round in the DAG. The first finalizer in the cluster emits; subsequent finalizers see the existing event and suppress their emit. Multi-emit becomes single-emit by collaboration.

**Correctness claim**: typically only one TVConsensus event per round in the DAG. Per-task mutex selection becomes trivial.

**Complexity**: low. A check before `EmitConsensusEvent`: "do I already have a TVConsensus event for round X?" If yes, return without emitting. Requires the round-state on each node to know about TVConsensus events for that round.

**Tradeoff**: not robust to fastpath race — two validators may simultaneously not-yet-see the other's emit and both emit. Reduces probability of multi-emit but doesn't eliminate it. Still requires a deterministic rule for the (rare) two-emit case.

### 7.6 Hybrid: 7.1 + 7.5 + deterministic fallback

**Mechanism**: deterministic single emitter per round (per 7.1); other validators suppress (per 7.5); if the elected emitter doesn't emit within a deadline, the next-deterministically-ranked validator becomes the emitter; tiebreak via lexicographic-min event ID if multiple emit (per 7.2 lite).

**Correctness claim**: same as 7.1 in the happy path; same as 7.2 in the multi-emit fallback.

**Complexity**: medium-high. Combines three mechanisms. Requires careful reasoning about the deadline parameter.

**Tradeoff**: best resilience, most coordination overhead. Most likely to handle byzantine and partition scenarios cleanly. Most complex to verify.

---

## 8. Implications for verification discipline

What verification invariants are missing that allowed this bug to ship through F3-B and Part F first-attempt:

### 8.1 Cross-node ledger byte-equality not asserted

F3-B's §10 said "10 reject-path tasks × 5 nodes byte-identical." The empirical claim was made but **the test infrastructure didn't programmatically compare per-node ledger state for each task**. It compared treasury bucket values (which happened to converge for that corpus because the corpus didn't trigger the multi-emit race). It did not compare per-recipient amounts per task per node.

Required: an automated test that, for every settled task in a verification corpus, queries every node's ledger for the per-recipient delta and asserts byte-equality across nodes.

### 8.2 Multi-emit detection not in any conformance suite

F3-B's conformance test suite (`internal/dispatch/conformance/`) tests per-(event, consumer) properties: duplicate live delivery, replay delivery, crash recovery, concurrent same-event delivery, content-hash discrimination, causal-prerequisite deferral. **None test "multiple distinct events with the same logical-key payload field."** A consumer that misbehaves under multi-emit passes the conformance suite.

Required: a conformance test for "multiple distinct canonical events admitted for the same business-key — does the consumer produce cross-node-uniform projected state?"

### 8.3 Vote-weight-tie corpus not in any verification

F3-B and Part F shadow corpora used worker submissions that produced unambiguous verdicts in practice. The bug only fires near vote-weight ties (where supermajority oscillates as votes arrive). The test corpus must include weighted-vote scenarios that produce non-trivial split outcomes.

Required: a synthetic test corpus where validator votes are constructed to produce known-tie weight scenarios, and the cross-node selection is measured.

### 8.4 No cross-node "consensus-uniformity" metric

There's no log line, no monitoring metric, no alert that fires when two nodes disagree on settlement state for the same task. The divergence accumulates silently; it took an explicit Phase C-sanity ledger-comparison query to surface it.

Required: a continuous monitoring check that periodically compares per-task settlement state across nodes and alerts on divergence.

---

## 9. Integer migration workstream status

Parts A through E.1 of the canonical-distribution-integer-migration workstream proved the following on this branch:

- **Part A**: `internal/protocolmath` allocator is correct in isolation (100% unit-test coverage including determinism, conservation, ceiling, and overflow tests).
- **Part B**: settler and gen-ledger have a working integer-canonical path; shadow-delta logging validates per-node float-vs-integer equivalence.
- **Part C**: canonical event payloads contain no float fields (AST lint + reflection test).
- **Part D**: integer arithmetic produces byte-identical output between x86 and ARM under QEMU emulation.
- **Part E**: `EventTypeIntegerMigrationActivation` event + dispatcher consumer + persistence adapter + startup-load.
- **Part E.1**: general recognition→dispatcher admission router that fixed Part F first-attempt's wiring gap.

These were **all proven** by their respective test suites and the Part F retry's Phase B-verify + Phase D evidence. The integer math is correct. The activation mechanism works. The bug-class Part E.1 closed is genuinely closed.

What the workstream **did not prove**, and could not prove in isolation:

- That the protocol it operates within produces cross-node-uniform settlement events.
- That replacing float with integer arithmetic in `verification_settler.Settle` removes a divergence source that was actually firing.

The selection-race characterized in this document is **upstream of the integer migration**. It would produce cross-node ledger divergence regardless of which arithmetic the settler uses. The integer migration was correct in itself, but the divergence it was designed to prevent (per Part B's "latent non-determinism" hypothesis about float-path remainder-absorption) is dwarfed by — and will continue to manifest cluster-wide despite — the consensus-event selection race.

**Merge-readiness assessment**:

- The integer-migration code itself is merge-ready: well-tested, well-documented, no regressions in unit tests, no protocol-layer impact in shadow mode. Once activated, it is correct on a single node.
- **The integer-migration workstream cannot honestly claim it eliminates cross-node settlement divergence on the live cluster**, because that divergence has a separate root cause (the selection race) which integer arithmetic does not address.
- Merging the branch as-is would land working code that solves a smaller problem than the merge's framing implies. This is a documentation/messaging issue, not a code-correctness issue.

**Recommended posture**: do not merge until either (a) the selection-race is fixed and the integer migration is re-verified end-to-end on a fixed cluster, OR (b) the merge is explicitly framed as "shadow-mode integer path proven; activation deferred pending consensus-event selection fix." Option (b) is unusual and risky because the activation event in the DAG would be inert until the selection-race fix ships, and if the selection-race fix ever causes a node to admit a different historical TVConsensus event than its current selection, settlement state would silently change.

---

## 10. Open questions

Items noted but not resolved in this document. They feed future workstream design.

### 10.1 The SourceReplay / addFromStore architectural gap

`internal/recognition/types.go:35` defines `SourceReplay` as a `CommitSource` value. It is **never used** in production code. `internal/dag/dag.go:392-395` shows `addFromStore` fires the post-commit hook with `replay=true` if the hook is set — but `LoadFromStore` (line 248-272) creates a fresh DAG and runs `addFromStore` for all persisted events BEFORE `SetOnCommit` is wired (the hook is attached later in `cmd/node/main.go:2089` inside `startStack`).

Consequence: historical DAG state is invisible to bus consumers registered in a newer binary version. A Part E-style activation event committed under the old binary, persisted, and seen on a node restart in the new binary stays inert indefinitely (the admission router never sees it during startup). This is what made Part F retry's Sub-scenario A unattainable.

This is not a bug in any specific consumer. It is an architectural gap in the consumer-registration / DAG-replay seam. It manifests for any new consumer added after a network has accumulated DAG history.

Fix space (out of scope for this document):

- Wire a `replayHistoricalToBusConsumers()` pass after `SetOnCommit` is attached, walking the DAG and emitting commits with `source=SourceReplay, replay=true`. Per-consumer `MarkRecognizedOnce` ensures idempotency for previously-seen consumers; new consumers see all historical events.
- Define a startup contract that any new consumer added after a network has been live MUST be backfilled via this pass before the bus starts accepting live commits.

This is referenced in the Part F retry plan (`docs/plans/implementation/part-f-retry-plan.md` §5) and in the Part F retry completion report's "Discoveries for Part G" section.

### 10.2 How long has the cluster been in a forked state?

Pre-existing 50 AET stake-state divergence implies the cluster has been silently divergent for some unknown duration. Without a historical per-node ledger snapshot, the timeline can't be reconstructed from current state. Estimating the bound: F3-B was merged 2026-04-15 (commit `603bd9b`). Part F first-attempt deploy on 2026-04-22 around 12:40 UTC (per first-attempt deploy logs). The drift accumulated somewhere between those two dates — on the order of a week.

### 10.3 Does the selection race affect non-canonical-replayed-state computations?

The bug we observed is in canonical replayed state (settlement). The cluster also has non-canonical state (e.g., autovalidator scoring caches, projection registry timing). Whether per-node selection differences in canonical events propagate to non-canonical state in observable ways is unanswered.

### 10.4 What other event types have multi-emit potential?

`grep -rn "publisher.Publish\|emitDAGEvent" internal/ cmd/` would show the full set of canonical event emitters. Each emit site needs review against the question "can multiple validators independently emit semantically-distinct events with the same logical-key payload field?" Candidates worth checking specifically: `Slashing*`, `PrerequisiteWithholding`, `Delegation`. Settlements were the obvious case; others may be silent.

### 10.5 What existing testnet artifacts depend on the divergent state?

If the testnet has been in a forked state for ~1 week, applications consuming testnet data (downstream agents, API consumers, dashboards) have been seeing different views from different nodes (depending on which node the ALB routed them to). Whether any consumer relies on a specific node's view, and how to reconcile, is an open question for any operational continuity assessment.

---

**End of characterization.**

This document is read-only ground truth for architect-session review. No implementation, no fix proposal, no merge decision. The frozen testnet state and the captured artifacts in `/tmp/part-f-retry-snapshots/` remain available for further evidence-gathering as the architect session proceeds.
