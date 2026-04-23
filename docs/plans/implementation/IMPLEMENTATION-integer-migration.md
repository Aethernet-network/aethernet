# Implementation Sequence — Canonical Distribution Integer Migration

**Location**: `docs/plans/implementation/2026-04-20-integer-migration-IMPLEMENTATION.md`
**Status**: Ready to execute; Part A is the first Claude Code session
**Derives from**: `docs/plans/2026-04-20-canonical-distribution-integer-migration-v2.md` (approved)
**Base commit**: `603bd9b` (main, post-F3-B merge)
**Feature branch**: `feat/canonical-distribution-integer-migration`

---

## How to use this document

This document sequences the v2 plan into 7 discrete Claude Code sessions (Parts A–G). Each Part is a separate session with its own prompt file in this directory. Parts must be executed in order; founder checkpoints gate progress between Parts.

The discipline is the same as the F3-B workstream:

1. Open a new Claude Code session from the repo root.
2. Paste the Part's prompt file contents.
3. Claude Code enters **plan mode** — produces an implementation plan, asks clarifying questions, does NOT write code.
4. Founder reviews the plan, approves or kicks back.
5. On approval, Claude Code implements.
6. Claude Code runs local verification (tests, lint, build).
7. Claude Code produces a §-style completion report.
8. Founder reviews the report, approves merge of the Part's commits into the feature branch, proceeds to next Part.

Parts do NOT merge to `main` individually. They all land on `feat/canonical-distribution-integer-migration`, and only after Part F (testnet cutover rehearsal) passes does the full branch merge to main.

---

## Part index

| Part | Commits | Shape | Session prompt |
|------|---------|-------|----------------|
| A | commit-1 | `protocolmath` primitive package | `IMPLEMENTATION-PART-A.md` |
| B | commits 2–5 | Settlement + generation ledger migrations (shadow-gated) | `IMPLEMENTATION-PART-B.md` |
| C | commit-6 | AST lint + runtime assertion for canonical payload float-freedom | `IMPLEMENTATION-PART-C.md` |
| D | commit-7 | Heterogeneous hardware Docker test rig | `IMPLEMENTATION-PART-D.md` |
| E | commits 8–9 | Cutover event type + shadow metric surface | `IMPLEMENTATION-PART-E.md` |
| F | commit-10 | Testnet cutover rehearsal + §10-style verification | `IMPLEMENTATION-PART-F.md` |
| G | commit-11 | Docs + handoff update + merge to main | `IMPLEMENTATION-PART-G.md` |

---

## Part dependencies

- **Part A** must complete and be merged into the feature branch before Part B starts. Part B's settler rewrite calls `protocolmath.Allocate`; if the primitive is broken, Part B has nothing to build against.
- **Part B** must complete before Parts C, D, E. C, D, E can happen in any order, but each needs B's feature-gated integer path to exist so that the lint, test rig, and cutover event are wiring against real code.
- **Part F** must be last before G. F is the live-testnet verification that gates the entire workstream. If F fails, the branch does not merge; we go back to design.
- **Part G** is purely administrative (docs + merge); it happens after F passes.

Recommended serial order: A → B → C → D → E → F → G. Parallelizing C/D/E adds coordination overhead for marginal time savings; serial is cleaner.

---

## Founder checkpoints (between Parts)

At each checkpoint, the founder reviews:

1. The Part's completion report produced by Claude Code.
2. The actual diffs of the commits on the feature branch.
3. Any deviations from the Part's prompt (Claude Code is required to flag deviations; they should not exist unless the founder approved them mid-session).
4. Whether the Part's local verification (tests, lint, build) passed.

Checkpoint outcomes:

- **Approve**: proceed to next Part.
- **Kick back**: Claude Code revises in the same session (preferred) or a new session (if context is lost).
- **Pause for architect review**: the workstream returns to this architect session for plan revision. Rare, but the option exists if the Part surfaces something the plan didn't anticipate.

---

## When things go wrong

**If Claude Code produces a plan that deviates from the Part prompt**: kick it back, cite the prompt section it missed. Do not negotiate on scope during Claude Code sessions — scope is locked in v2 of the plan. If Claude Code identifies a genuine plan defect, pause the session and return to this architect session.

**If tests fail in a Part**: Claude Code should debug and fix in the same session. If the failure reveals a plan defect (not an implementation bug), pause and return to architect.

**If shadow-mode observation (between Parts B and F) reveals unexpectedly large deltas**: do not flip the cutover. Pause the workstream, return to architect session, analyze the delta distribution, revise the plan or the analyzer-family boundary audit as needed.

**If Part F testnet rehearsal fails**: do not merge. Return to architect session. The workstream is blocked until the failure is understood and the plan revised.

---

## Artifacts produced by this workstream

By the time Part G completes, the following exist in the repo:

**New code:**
- `internal/protocolmath/` — shared deterministic allocation primitive
- `internal/event/lint/` (or extension) — AST lint for canonical payload float-freedom
- `internal/jcs/canonical_assert.go` (or equivalent) — runtime assertion wrapper
- Cutover event type (name TBD in Part E) in `internal/event/`
- Cross-architecture test rig in `test/cross-arch/` or equivalent CI structure

**Modified code:**
- `internal/settlement/verification_consensus_settler.go` — settler uses `protocolmath`, feature-gated
- `internal/settlement/generation_ledger_calculator.go` — generation ledger uses `protocolmath`, feature-gated
- `internal/taskverification/reputation.go` — `ValidatorQScore` returns `BasisPoints`
- `cmd/node/main.go` — wiring updates
- Canonical event registry — new cutover event type registered

**Tests:**
- `protocolmath` unit suite (determinism, permutation, conservation, invariant, overflow, ceiling)
- Shadow-delta tests at settler and generation-ledger callsites
- AST lint tests in `internal/event/lint/`
- Runtime assertion tests
- Cross-architecture corpus replay tests (Docker buildx x86 + arm64)
- Live testnet §10-style verification report (not a test file; a completion artifact in `docs/verification/`)

**Docs:**
- Updated `docs/lessons.md` with findings and patterns learned
- Updated handoff document with workstream completion and Step 4 unblock
- Protocol-upgrade changelog entry for the cutover event
- This implementation sequence document, retained as historical record

---

## Ready to start

Part A is ready. The Claude Code prompt is in `IMPLEMENTATION-PART-A.md`. Open a new Claude Code session, paste the Part A prompt, expect plan mode first. After you approve the plan, Claude Code implements. After Part A's commits land on the feature branch and you approve the checkpoint, Part B begins.

---

**End of implementation sequence document.**
