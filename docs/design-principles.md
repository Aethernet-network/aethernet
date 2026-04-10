# AetherNet Design Principles

**Location**: `docs/design-principles.md`
**Status**: Standing meta-rules. Read at the start of every architect chat, every design review, and before writing any non-trivial implementation prompt. Cited from CLAUDE.md as required reading alongside `docs/lessons.md`.

This document is upstream of `CLAUDE.md` and `docs/lessons.md`. CLAUDE.md tells you the engineering standard and the workflow discipline. lessons.md tells you specific patterns learned the hard way. This document tells you the *principles* those rules and patterns are derived from. When a new situation arises that no specific rule covers, these principles are how you decide.

If a proposed design, prompt, or fix violates one of these principles, it is wrong even if no specific rule in CLAUDE.md or lessons.md prohibits it. The principles are the appellate court. Drift across chats is prevented by every chat reading this file and refusing to violate it.

This document is short on purpose. Each principle is one sentence followed by the reasoning. If a principle needs more than a paragraph to defend, it is probably two principles, or it is a specific rule that belongs in CLAUDE.md instead.

---

## 1. The thesis is load-bearing

AetherNet exists to make AI work verifiably trustworthy at scale through structurally independent compound verification. Every architectural decision must serve this thesis directly. A feature that is convenient but weakens compound verification is rejected. A feature that is hard but strengthens compound verification is built.

The thesis is not negotiable for shipping speed, developer convenience, or implementation simplicity. The whole point is that other systems compromise on verification because verification is hard, and AetherNet is the system that doesn't compromise. If we compromise, there is no reason for AetherNet to exist.

## 2. Machine speed is the standard

Every protocol path is designed for machine-to-machine coordination at machine speed. Human-tolerance timeouts (seconds, minutes, hours of slack) are a smell. The standard is: validators respond in milliseconds, rounds finalize in seconds, settlements apply immediately on consensus.

When a static timeout appears in a design, ask why. If the answer is "to give validators time to do work," the protocol must instead let validators *communicate* that they are doing work, with state and ETA, so the round can wait adaptively and the timeout can shrink to a backstop. Silence must be a failure signal, not a default. Static timeouts are what you reach for when you don't want to design the protocol; we are here to design the protocol.

## 3. Validators communicate state, not just verdicts

Verdicts are terminal events. The space between events — fetching data, running an analyzer, computing a score, waiting on a dependency — is durative state, and the protocol must be able to express it. Any long-running validator operation must emit state updates that other nodes can read.

This is what makes principle 2 actually work. You cannot have machine-speed coordination if half the participants are silent and the other half are guessing. State updates are how silence becomes meaningful: a validator that has not voted *and* has not emitted a state update is failing, and the round can act on that quickly. A validator that has not voted *but* is emitting "fetching blob, ETA 8s" is making progress, and the round waits adaptively.

## 4. Compound verification requires structural independence on every axis

Verification gets stronger over time because each verification is structurally independent of every other one. Independence is not socially assumed; it is protocol-computed and protocol-enforced. Every axis on which independence is measured — analyzer methodology, validator-contributor relationship, temporal correlation, network topology — must have explicit machinery to detect, measure, and enforce it.

If a design appears to satisfy compound verification but the independence is assumed rather than computed, it is single-validator scoring with extra steps. The audit at `docs/multi-validator-scoring-audit.md` is the cautionary tale.

## 5. The protocol is the source of truth

Application state is a projection of the DAG, never the other way around. TaskManager, OCS pending sets, validator seats, settlement applied sets, reputation stores — every one of these is rebuildable from the DAG. If application state and DAG state ever diverge, the DAG wins.

This means: never let an application-layer cache become authoritative. Never let a local-only mutation determine consensus state. Never let a state machine transition fire from anywhere except a recognition consumer responding to a committed event. The DAG is the ledger; everything else is a view.

## 6. Generalize the primitive, not the fix

When a problem appears in one event type, one consumer, or one pipeline stage, the question is: *what is the underlying primitive that should solve this for every case it could appear in?* Per-instance fixes accumulate into special cases that the next person reading the code cannot understand. General primitives compose.

The vote materialization stall (commit `c6defe8`) was correctly fixed for vote events, then re-fixed three more times for other event types before someone generalized to semantic parents. The cost of generalizing on the first iteration is small; the cost of debugging the same root cause N times in production is enormous. The default answer to "should this fix be specific or general" is general.

## 7. Reuse mechanism, separate concern

When a new subsystem needs transport, persistence, signing, identity, or any other infrastructure that already exists, it reuses the *mechanism* but operates on its own *channel*. Reuse means the wire format, connection pool, framing, and rate limiting are shared. Separation means the new subsystem has its own message types, its own queue, its own backpressure, and its own failure mode.

The wrong move is to extend an existing channel to do double duty — that creates entanglement and prevents either concern from evolving. The wrong move is also to reinvent infrastructure that already exists — that creates parallel systems that drift. The right move is one transport implementation, many channels.

## 8. No human-in-the-loop in any protocol path

The protocol coordinates machines. Humans set policy, deploy code, observe behavior, and intervene in extremis, but no protocol path waits for a human decision. If a design suggests human involvement — an admin approving something, a moderator reviewing something, a manual escalation — it is wrong and must be redesigned to either decide automatically or fail explicitly.

Disputes auto-resolve. Slashing auto-applies after a challenge window. Calibration auto-completes. There is no admin console that can override consensus. The protocol either decides or it surfaces a failure that humans can investigate after the fact, but humans never sit in the path.

## 9. Persist before publish

Any event that triggers external action — fee distribution, slashing, settlement, blob serving — must have its triggering state persisted *before* the event is published. If the publishing node crashes between persist and publish, the next startup re-evaluates the persisted state and re-publishes via idempotency. The reverse ordering (publish then persist) creates harder recovery: the network has acted on an event whose local state was lost.

Idempotency at every consumer makes re-publication safe. Persistence first makes recovery deterministic. Both are required.

## 10. Content addressing is the integrity model

Every blob, every event, every reference is identified by the hash of its canonical bytes. Integrity is free: fetch, hash, compare. Mismatch means drop and retry. There are no trust assumptions about who serves the bytes — only about whether the bytes hash correctly.

This means deterministic canonicalization is not optional. Anywhere there is map iteration, floating-point arithmetic, locale-dependent string handling, or non-deterministic serialization, content addressing breaks. Every serialization path must be byte-stable across runs, machines, and Go versions.

## 11. Integer canonical state, no exceptions

No `float64` in canonical replayed state. No `time.Time` in canonical replayed state. No `time.Now()` in canonical protocol logic. Timestamps are explicit `int64` unix seconds passed in by callers. Scores, weights, and shares are integer fixed-point in basis points or smaller scales.

Floats are non-deterministic across hardware. Wall-clock time is non-deterministic across nodes. Both are forbidden in any code path that affects what nodes agree on. They may exist in payload data that the protocol carries but does not interpret, but the moment the protocol *computes* on them, they must be converted to integers under an explicit, documented scaling rule.

## 12. Beauty is a correctness signal

If a fix feels hacky, it is wrong. If a design has a special case, ask whether the special case is a missing primitive. If the implementation is hard to explain, the architecture is probably wrong. The question to ask before shipping is not "does this work" but "would a staff engineer at a top-tier protocol company study this and learn from it."

This is not aesthetic preference. Ugly code is correlated with bugs because ugliness usually means a concept is in the wrong place. When the architecture is right, the code is short, the names are obvious, and the failure modes are easy to enumerate. When you find yourself writing a comment to explain why something is unintuitive, the something is the bug.

## 13. Tests are necessary, live testnet is sufficient

Unit tests and integration tests catch a class of bugs that the live testnet does not. The live testnet catches a class of bugs that tests do not. Both are required, in that order. A change is not done when tests pass; a change is done when it is verified working on the live 5-node testnet under the standard verification protocol.

The three bugs caught in the multi-validator suite (`0e6d8cc`, `1cfb8ed`, `3526599`) and the blob replication gap that blocked the accept path all passed unit tests and failed on the live testnet. Unit tests verify code; the testnet verifies the architecture.

## 14. The standard is permanent

This protocol is being built to outlast any feature, any sprint, any specific version. The decisions made now are load-bearing for everything built on top for years. The standard does not relax under deadline pressure, does not relax for "let's just ship this and fix it later," does not relax because the immediate task is small. The standard is the standard.

When in doubt, build it the way you would want to read it five years from now in production at scale. That is what AetherNet is.

## 15. Observable evidence beats self-reported claims

Any protocol decision that depends on validator state must be based on observable evidence — signed messages the network can cross-check, actual bytes flowing through the network, actual work products appearing at expected times — rather than validator self-reports. Self-reports are hints; they inform decisions but they do not authorize decisions.

A validator that claims "I am fetching the blob, ETA 8 seconds" is making a claim. The protocol should not wait 8 seconds because the validator said so; it should wait because the network can observe fetch traffic to that validator, because the validator's progress lease is monotonically advancing, because its history of claimed ETAs has matched actual delivery. A validator that claims "I will deliver in 3600 seconds" should not prolong consensus for 3600 seconds — the protocol clamps the claim against observable network norms and treats extreme claims as suspect behavior.

This principle exists because self-reported state is an attack surface. A byzantine validator that can extend rounds by lying about its state turns adaptive timing into a denial-of-service vector that is cheaper than any existing attack on the protocol. The defense is not to discard self-reports — they are useful hints that speed up honest cases — but to require that they be corroborated by observable evidence before they influence consensus-affecting decisions. When self-report and observable evidence disagree, observable evidence wins.

This principle was codified after the BlobSync design review cycle (April 2026), where a proposed `ValidatorRoundStateUpdate` mechanism would have allowed any single validator to stall rounds indefinitely by emitting fake "still working" state. The fix was not to remove state communication — communication is necessary for machine-speed coordination — but to require that state claims be backed by observable progress. The principle generalizes: anywhere the protocol is tempted to trust a participant's self-report about its own state, it must first ask *what evidence would prove this true, and does that evidence exist*.

---

## How to use this document

When designing: read the principles, then design. If your design satisfies all 14, proceed. If it violates one, redesign or articulate why this is the rare case where the principle does not apply (the bar for this is high; the principles are the principles).

When reviewing someone else's design (including a previous chat's design, including a ChatGPT or Grok review): score it against the principles. Flag violations specifically with the principle number.

When writing an implementation prompt: include the relevant principles as constraints. Claude Code reads CLAUDE.md and lessons.md at session start, but a prompt that explicitly cites principle 6 (generalize the primitive) or principle 3 (state, not silence) is a prompt that prevents drift at the implementation layer.

When updating this document: principles are added when a new architectural insight has proven itself across multiple decisions. Principles are not removed. If a principle turns out to be wrong, the correct response is to write a new principle that supersedes it and document the supersession; the old principle remains visible so future readers can see the reasoning.

The principles bind every chat, every implementation, every review, every commit. They are how AetherNet stays AetherNet across the inevitable chaos of building something this large.
