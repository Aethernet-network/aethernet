# Repo-Wide `escrow.Hold` Caller Audit — 2026-04-15 (Part E Precondition)

**Type**: Read-only audit. No code changes. The artifact is this checklist.
**Parent**: `docs/plans/2026-04-15-settlement-consensus-integrity-fix.md` (F3-B fix design v3-final, commit `1a7f096`). Specifically §6 (Part E — Escrow API hardening) and §9 (sequencing).
**Purpose**: enumerate and classify every call site of `escrow.Hold` in the repository so Part E implementation can switch each site deterministically to either `RegisterEscrow` (production) or `FundAndRegisterEscrowForTest` (test-helpers module). Part E cannot proceed without this checklist approved.

---

## Executive Summary

Total `escrow.Hold` call sites found in the repository: **29**.

- **Category 1 — Production, switch to `RegisterEscrow`**: **3** call sites. One is the F1 double-debit root cause (applicator.go:310); two are catch-up paths on peer/deferred-settlement scenarios that require DAG lookup to identify the canonical funding Transfer.
- **Category 2 — Test, switch to `FundAndRegisterEscrowForTest`**: **24** call sites across 7 test files. Ten of them live inside `internal/escrow/*_test.go` (same-package tests of the escrow implementation itself) — noted as a module-boundary complexity for Part E.
- **Category 3 — Unknown, BLOCKING**: **2** call sites. One is the `s.protoClient == nil` fallback in the production API server (`internal/api/server.go:1507`) — reachable only when the API server is wired without a protocol client, i.e., from tests. The other is the standalone marketplace binary (`internal/marketplace/server.go:355`) which operates its own application-layer escrow not backed by any DAG Transfer event.

**Audit status: incomplete; founder decision required on the two Category 3 entries before Part E implementation begins.** Each requires an architectural call about whether the code path is retained, removed, or redesigned — none can be auto-reclassified from static analysis.

Completeness verified by two independent greps returning identical counts (29 matches; see §Completeness Verification). Every match is classified below.

---

## Category 1 — Production, switch to `RegisterEscrow`

| # | File:Line | Enclosing function / call chain | `fundingTransferRef` source | Current bug impact (removed by switch) |
|---|---|---|---|---|
| C1-1 | `internal/settlement/applicator.go:310` | `Applicator.applyTransfer()` inside `case "escrow-lock"` branch at `applicator.go:306`. Fired from `Applicator.Apply()` at `applicator.go:137` via `SettlementConsumer.Consume()` (`internal/recognition/settlement_consumer.go:65`) on every `EventTypeSettlement` event that targets an `escrow-lock` Transfer. | **Directly available**: the canonical `*event.Event` is passed in as the `target` parameter to `applyTransfer` at `applicator.go:287`. Pass `target.ID` (or the `targetID event.EventID` already in scope at :289) as `fundingTransferRef`. | `RecordFromSync(target)` at `applicator.go:291` already promoted the canonical Transfer to the ledger in Settled state. `Hold` at :310 then invokes `TransferFromBucket` at `escrow.go:138`, producing the synthetic `txf:bucket:<poster>:escrow:<taskID>:<amount>:<seq>` entry observed on every node in the 2026-04-15 F1 audit. **Switching to `RegisterEscrow(target.ID)` writes metadata only; the second `TransferFromBucket` stops firing; the F1 double-debit is closed on this path.** |
| C1-2 | `internal/settlement/verification_consensus_settler.go:102` | `VerificationConsensusSettler.Settle()` in the "escrow not locked → catch up" branch at `:100–104`. Fired from `TaskVerificationConsensusConsumer.Consume()` (`task_verification_consensus_consumer.go:100`) on every finalized `EventTypeTaskVerificationConsensus` event — runs on all 5 testnet nodes per the F3-B settlement-divergence audit (`docs/audits/2026-04-15-settlement-divergence-investigation.md`). | **NOT directly available at call site.** No `*event.Event` holding the canonical escrow-lock Transfer is in scope — the settler fires on `TaskVerificationConsensus`, not on the `Settlement` event for the escrow-lock Transfer. Resolution requires a DAG query for `(FromAgent=payload.PosterID → ToAgent="escrow:"+payload.TaskID, Reason="escrow-lock")` to locate the Transfer EventID. Per v3-final plan §6.2: "If validation fails synchronously (the Transfer is not yet projected locally), RegisterEscrow returns an error; the calling consumer treats this as a prerequisite failure and is deferred per Part D." The Part E implementer must either add this lookup helper OR defer the catch-up via Part D prerequisite gating. | When fired, `Hold` at :102 runs `TransferFromBucket` for an amount already moved by the canonical Transfer. In observed testnet behavior (F3-B reproduction on commit `d3ee95e`), this path executes after the applicator's Hold on Nodes 2/4/5, compounding with the F1 double-debit to produce the per-node 1-vs-2× worker-payout divergence. **Switching to `RegisterEscrow(lookedUpTransferRef)` eliminates the duplicate transfer on this path; Part D's deferred-prerequisite handling covers the "Transfer not yet projected locally" case.** |
| C1-3 | `cmd/node/main.go:1634` | `stack.settlementApp.SetTaskSettler(func(payload settlement.TaskSettlementPayload) error { ... })` closure, escrow-catch-up branch at `:1628–1639`. Fired from the settlement applicator's task-settler callback when a `TaskSettlement` event applies and the escrow is not yet locked on this node. Comment at `:1628–1632` explicitly names the catch-up scenarios: "On the posting node, escrow was locked via canonical Transfer (prompt 2). On peer nodes, the SettlementApplicator's applyTransfer registered the escrow entry when the escrow-lock transfer settled. If for any reason the escrow doesn't exist yet (e.g. deferred settlement), create it now." | **NOT directly available at call site.** Same shape as C1-2: the closure receives `payload settlement.TaskSettlementPayload` which has `PosterID`, `TaskID`, `Budget` — but no direct reference to the canonical escrow-lock Transfer event. Resolution requires the same DAG lookup as C1-2: query for the `(PosterID → escrow:TaskID, Reason=escrow-lock)` Transfer. Same Part D deferral path applies if the Transfer is not locally projected. | Legacy path (from prompt 2, predating the current applicator's escrow-lock branch). Currently fires as a third catch-up alongside C1-1 and C1-2. In the F3-B divergence pattern, whether this fires or short-circuits depends on whether the task already reached a terminal status via the new verification-consensus path (guard at `:1613–1622`). When it does fire, it produces the same synthetic `TransferFromBucket` duplicate. **Switching to `RegisterEscrow` eliminates the duplicate; the guard at :1613–1622 continues to short-circuit on the new path, so this catch-up only runs for legacy/edge-case settlement.** |

**Category 1 implementation notes**:

- C1-1 is the trivial switch — `target.ID` is at hand. Implementer can change the call in the same commit that adds `RegisterEscrow` to the escrow package.
- C1-2 and C1-3 both need a Transfer-lookup helper. Part E §6.2 defines the contract: `RegisterEscrow` validates `fundingTransferRef` against the DAG and returns an error on missing/mismatched. The Part E implementer decides whether the helper lives in `internal/escrow/`, `internal/settlement/`, or is inlined at each call site; this audit does not specify.
- All three Category 1 sites write through the same escrow `entries` map and `esc:<taskID>` BadgerDB key, so the semantic change is uniform: persist `EscrowEntry` metadata + record `FundingTransferRef`, skip `TransferFromBucket`.

---

## Category 2 — Test, switch to `FundAndRegisterEscrowForTest`

24 call sites across 7 `_test.go` files. All live in test packages or test files and exist because the test fixture needs both the funds-moved side effect and the metadata registration side effect in a single in-process call (no DAG, no protocol client, no recognition fabric).

### By file

| # | File:Line | Test or fixture | In `_test.go`? | Module-move complexity |
|---|---|---|---|---|
| C2-1 | `internal/settlement/verification_consensus_settler_test.go:40` | `setupSettler` test-helper fixture | ✓ | Straightforward — just an import swap to the test-helpers module. |
| C2-2 | `internal/settlement/verification_consensus_settler_test.go:329` | `TestDistributeByQualityFallback` / table test setup | ✓ | Same as C2-1. |
| C2-3 | `internal/integration/e2e_test.go:205` | `TestE2E_DAGConvergence` harness setup | ✓ | Straightforward. |
| C2-4 | `internal/integration/e2e_test.go:326` | `TestE2E_ValidatorConvergence` setup | ✓ | Straightforward. |
| C2-5 | `internal/integration/e2e_test.go:769` | `TestE2E_FullLifecycle` setup | ✓ | Straightforward. |
| C2-6 | `internal/integration/e2e_test.go:1173` | `TestE2E_BurstLoad` shared-escrow fixture | ✓ | Straightforward. |
| C2-7 | `internal/integration/e2e_test.go:1398` | `TestE2E_Partition` setup | ✓ | Straightforward. |
| C2-8 | `internal/integration/projection_escrow_test.go:33` | `TestEscrow_HoldsOnTransferOptimistic` (step-2 projection integration test; the `IntegrationTestRef` referenced by `escrow.Projection`) | ✓ | Straightforward. Note: this test's symbol name is referenced from production code via `internal/escrow/projection.go`'s `Projection()` constructor's `IntegrationTestRef` field. Moving the test file within the same import path preserves the reference; moving to a different path breaks the projection-lint's PR-3 check. Implementation should keep the test in `internal/integration/` even after the helper swap. |
| C2-9 | `internal/escrow/escrow_test.go:30` | `TestEscrow_Empty` | ✓ | **Same-package test** (see below). |
| C2-10 | `internal/escrow/escrow_test.go:46` | `TestHold` | ✓ | Same-package test. This test literally exercises the `Hold` method's contract; moving it to a separate module changes what is being tested. |
| C2-11 | `internal/escrow/escrow_test.go:72` | `TestHold_InsufficientBalance` | ✓ | Same-package test. |
| C2-12 | `internal/escrow/escrow_test.go:87` | `TestRelease` setup | ✓ | Same-package test. |
| C2-13 | `internal/escrow/escrow_test.go:109` | `TestRefund` setup | ✓ | Same-package test. |
| C2-14 | `internal/escrow/escrow_test.go:131` | `TestEscrow_TotalEscrowed` setup | ✓ | Same-package test. |
| C2-15 | `internal/escrow/escrow_test.go:132` | `TestEscrow_TotalEscrowed` setup (continued) | ✓ | Same-package test. |
| C2-16 | `internal/escrow/escrow_test.go:133` | `TestEscrow_TotalEscrowed` setup (continued) | ✓ | Same-package test. |
| C2-17 | `internal/escrow/idempotency_test.go:25` | `TestHold_IdempotentUnderDuplicate` | ✓ | Same-package test. |
| C2-18 | `internal/escrow/idempotency_test.go:79` | `TestHold_DistinctTasks` | ✓ | Same-package test. |
| C2-19 | `internal/autovalidator/auto_recovery_test.go:61` | `TestAutoValidator_RecoveryFromRestart` setup | ✓ | Straightforward. |
| C2-20 | `internal/autovalidator/auto_test.go:125` | `TestAutoValidator_HappyPath` setup | ✓ | Straightforward. |
| C2-21 | `internal/autovalidator/auto_test.go:257` | `TestAutoValidator_Dispute` setup | ✓ | Straightforward. |
| C2-22 | `internal/autovalidator/auto_test.go:329` | `TestAutoValidator_Timeout` setup | ✓ | Straightforward. |
| C2-23 | `internal/autovalidator/auto_test.go:401` | `TestAutoValidator_MultiFamily` setup | ✓ | Straightforward. |
| C2-24 | `internal/autovalidator/auto_test.go:486` | `TestAutoValidator_Recovery` setup | ✓ | Straightforward. |

### Module-boundary complexity: same-package tests in `internal/escrow/`

C2-9 through C2-18 (10 call sites across `internal/escrow/escrow_test.go` and `internal/escrow/idempotency_test.go`) live in the same Go package as the escrow implementation. They have privileged access to unexported identifiers. They also **test the `Hold` method's contract specifically** — several of these tests exist because `Hold` is the behavior under test (e.g., `TestHold`, `TestHold_InsufficientBalance`, `TestHold_IdempotentUnderDuplicate`).

Per v3-final §6.1: "`escrow.Hold` is replaced in production by `RegisterEscrow`. The combined fund-and-register helper (formerly `HoldForTest`) is renamed `FundAndRegisterEscrowForTest` and lives in a separate Go module structurally not importable from production packages."

Three implementation options for the same-package test case — design choice for Part E, NOT for this audit:

- **Option A**: move all 10 same-package tests to the test-helpers module and rewrite them to exercise `FundAndRegisterEscrowForTest` instead of `Hold` directly. Loses the "test the real method by name" directness; gains boundary purity.
- **Option B**: keep the same-package tests in `internal/escrow/` and have them call an unexported helper (e.g., `fundAndRegister`) that is the private implementation both `RegisterEscrow` (no-op on fund portion) and the test-helpers module's `FundAndRegisterEscrowForTest` (via sanctioned internal API) share. The public `Hold` name goes away; the private primitive is still testable inside the package.
- **Option C**: split the escrow package into `internal/escrow/` (public API: `RegisterEscrow`, `ReleaseNet`, `Refund`, `IsLocked`, `Get`) and `internal/escrow/internal/core/` (the private funds-moving primitive). Same-package tests become two suites: pure metadata tests in `escrow/`, fund-moving primitive tests in `escrow/internal/core/`. Test-helpers module composes them for integration use.

Flagged as implementation-level complexity. Audit does not prescribe an option.

### Other Category 2 notes

- All 14 non-escrow-package test call sites (C2-1 through C2-8, C2-19 through C2-24) are straightforward: import `internal/escrow_testhelpers` (the new module), call `FundAndRegisterEscrowForTest(tl, esc, taskID, poster, amount)` or similar, same semantics as the old `esc.Hold(...)` call. No circular-dependency risk — the test-helpers module depends on `internal/escrow` + `internal/ledger`, and tests in `internal/autovalidator`, `internal/integration`, and `internal/settlement` can freely depend on the test-helpers module at test build time.
- C2-8 (`projection_escrow_test.go:33`) is specifically the test referenced by the step-2 projection-registry's `escrow.Projection()` constructor as its `IntegrationTestRef`. The `internal/projections/lint` CI check (step 3, commit `c20e6df`) will fail if this symbol moves or is renamed. Part E implementer should preserve the test function's name and import path; the helper call inside the test body can change freely.

---

## Category 3 — Unknown (BLOCKING)

Two entries that cannot be cleanly classified from static analysis. Each requires a founder architectural decision before Part E implementation can proceed.

### C3-1 — `internal/api/server.go:1507` (`s.protoClient == nil` fallback)

**Call context**: inside `Server.handlePostTask`, lines `1499–1511`:
```go
if s.protoClient != nil {
    if _, err := s.protoClient.SubmitEscrowLock(crypto.AgentID(posterID), task.ID, req.Budget); err != nil {
        writeError(w, http.StatusBadRequest, err.Error())
        return
    }
} else {
    // Fallback for tests/single-node without protocol client.
    if err := s.escrowMgr.Hold(task.ID, crypto.AgentID(posterID), req.Budget); err != nil {
        writeError(w, http.StatusBadRequest, err.Error())
        return
    }
}
```

**Reachability**:
- `cmd/node/main.go:1243` always constructs `stack.protoClient = protocol.NewClient(...)`.
- `cmd/node/main.go:2422` always calls `apiSrv.SetProtocolClient(stack.protoClient)`, which sets `s.protoClient` at `internal/api/server.go:580`.
- In the deployed `aethernet` binary, `s.protoClient` is always non-nil. The `else` branch at :1505–1511 is never executed in production.
- The branch IS reachable from unit tests that construct an `api.Server` without calling `SetProtocolClient`. Test files under `internal/api/` exercise this path.

**The ambiguity**: the call site is located in production code (`internal/api/server.go`), but its executable semantics are test-only. Per v3-final §6.4 E-1: "`FundAndRegisterEscrowForTest` lives in a Go module not imported by production. CI verifies the production binary's dependency tree excludes it." If this fallback stays as-is and calls `FundAndRegisterEscrowForTest`, the E-1 invariant is violated — a production package would import the test-helpers module.

**Three possible resolutions, each a founder decision**:

1. **Remove the fallback entirely.** `api.Server` always requires a wired `protoClient`. Tests that currently construct `api.Server` without one must either wire a protoClient or switch to a different entry point. Cleanest architecturally; requires updating the test helpers that currently rely on the fallback.

2. **Keep the fallback but make it fail loudly.** Replace the `else` branch with an error response indicating misconfiguration. Equivalent to option 1 for test purposes but preserves the public API shape.

3. **Keep the fallback and use a different mechanism.** The `else` branch calls a private `s.fundAndRegisterInternal(...)` that lives in the production escrow package as an unexported method, usable only when explicitly invoked from api/server.go's fallback. Preserves existing test behavior; violates the spirit of v3-final §6.1 ("structurally stronger than a build tag") by keeping a production-code path that performs the combined fund-and-register behavior.

**Evidence that would resolve**: founder decision on which resolution to pick. Static analysis alone cannot answer.

### C3-2 — `internal/marketplace/server.go:355` (marketplace application binary)

**Call context**: inside `Server.handleCreateTask`, the standalone marketplace binary's task-post handler. Lines `354–358`:
```go
if req.Budget > 0 && req.PosterID != "" {
    if err := s.escrowMgr.Hold(task.ID, crypto.AgentID(req.PosterID), req.Budget); err != nil {
        slog.Warn("marketplace: escrow hold failed", "task_id", task.ID, "err", err)
    }
}
```

**What the marketplace binary is**: per `cmd/marketplace/main.go:1–26`, it is a separate application binary that runs ABOVE a protocol node. Quoted verbatim from the header: "The marketplace is a separate application layer that sits above the protocol node. ... This binary is the reference implementation of 'an application built on AetherNet'. Third-party developers use the same SDK and the same public API to build their own applications without touching any internal protocol code."

The marketplace binary connects to a node via the Go SDK (read-only access to protocol state). Its `escrowMgr` is an application-local in-memory escrow — NOT backed by the DAG, NOT subject to BFT consensus, NOT connected to any canonical `Transfer` event.

**The ambiguity**: `RegisterEscrow(fundingTransferRef EventID)` requires a DAG-projected canonical `Transfer` event as its `fundingTransferRef` (per v3-final §6.2). The marketplace's in-memory escrow has no canonical Transfer. The v3-final design does not address the marketplace-binary case.

**Three possible resolutions, each a founder decision**:

1. **Out of scope for Part E.** The marketplace binary is a separate application; it doesn't participate in DAG consensus; its escrow is application-local bookkeeping. Leave `internal/marketplace/server.go:355` untouched. Note the exclusion explicitly in Part E's commit message and in a lint-exemption comment at the call site (or via a `// projections:lint ignore` pragma — though note that pragma is currently for projection-registry's lint, not a general-purpose escape). Accepted downside: the `Hold` symbol survives in the codebase, even if only called by the marketplace binary, which may conflict with the v3-final §6.1 intent of retiring the name entirely.

2. **Retire the marketplace binary.** If it's legacy / reference-only / not deployed, delete `cmd/marketplace/` and `internal/marketplace/` entirely. Simpler codebase; removes Category 3. Audit-level question: is the marketplace binary shipped and used, or is it dead code maintained alongside the node binary?

3. **Redesign the marketplace escrow to go through the protocol.** The marketplace binary submits tasks and escrows via the SDK's `PostTask` call, which hits the node's `/v1/tasks` API, which goes through `handlePostTask` and the `SubmitEscrowLock` / `Hold` path. This is effectively what happens today if a worker uses the marketplace — marketplace posts task → node API handler fires → canonical Transfer emitted → Part E's `RegisterEscrow` fires via C1-1. The marketplace's own `server.go:355` call is doing a DIFFERENT thing: application-layer bookkeeping about tasks the marketplace itself tracks, separate from protocol-layer escrow. If the marketplace's application-layer escrow is actually redundant with the protocol-layer escrow, remove it. If it's serving a real application purpose, it's out of scope for Part E (per resolution 1).

**Evidence that would resolve**: founder decision on whether the marketplace binary is (a) actively deployed production infrastructure, (b) reference/dev-only, or (c) retired. If (a), pick resolution 1 or 3. If (b), pick resolution 1 with a note. If (c), pick resolution 2.

### Audit status

**Audit incomplete; founder decision required on C3-1 and C3-2 before Part E implementation begins.**

Per the brief: "Block the audit's completion on resolving any Category 3 entries. Either reclassify them into Category 1 or Category 2 with evidence, or surface the ambiguity for founder decision."

This audit surfaces both C3-1 and C3-2 for founder decision. Once the founder picks resolutions, each Category 3 entry converts to either Category 1 (production switch) or Category 2 (test switch) or an explicit removal — at which point the checklist is complete and Part E implementation can begin.

---

## Completeness Verification

### Grep commands executed

**Primary search:**
```bash
grep -rn "\.Hold(" internal/ cmd/ pkg/ --include="*.go"
```
Returned 29 matches.

**Verification search** (entire repo, `.go` files only):
```bash
grep -rn "\.Hold(" --include="*.go" .
```
Returned 29 matches. Confirmed by `wc -l` count.

**Exclusion check** (anything outside `internal/`, `cmd/`, `pkg/`):
```bash
grep -rn "\.Hold(" --include="*.go" . | grep -v 'internal/' | grep -v 'cmd/' | grep -v 'pkg/'
```
Returned 0 matches. No `.Hold(` calls in `examples/`, `scripts/`, or any other top-level directory.

**Python SDK check**: `sdk/python/aethernet/` is Python, not Go, and does not expose an escrow-lock-via-Hold path. Not applicable.

### Classification tally

- Category 1: 3 entries (C1-1, C1-2, C1-3).
- Category 2: 24 entries (C2-1 through C2-24).
- Category 3: 2 entries (C3-1, C3-2).

**Total: 3 + 24 + 2 = 29.** Matches the grep total. Every call site is classified.

### Receiver-type verification

Spot-check confirms every `.Hold(` match in the repo is against an escrow manager / `*Escrow` type (receivers named `e`, `em`, `esc`, `sharedEsc`, `a.escrow`, `s.escrowMgr`, `stack.escrowMgr`). No `.Hold(` calls against unrelated types matched the pattern. Classification therefore covers all `escrow.Hold` calls in the repository.

---

## Founder Sign-Off

*(blank; awaiting founder review of the checklist before Part E implementation begins)*

**Sign-off checklist** for the founder:
- [ ] C3-1 (`internal/api/server.go:1507` test-fallback) resolution: ___________________
- [ ] C3-2 (`internal/marketplace/server.go:355` marketplace binary) resolution: ___________________
- [ ] Category 1 identified `fundingTransferRef` sources accepted (trivial for C1-1; DAG-lookup-required for C1-2 and C1-3; Part D deferral invoked when Transfer not locally projected).
- [ ] Category 2 same-package-test module-boundary option selected for `internal/escrow/*_test.go` (Option A / B / C per §Category 2).
- [ ] Category 2 projection-lint-reference preservation for C2-8 (keep `TestEscrow_HoldsOnTransferOptimistic` at its current symbol path to avoid breaking the step-3 lint's PR-3 check).

Once all items checked, the audit status flips from "incomplete" to "approved"; Part E implementation can begin per the v3-final §9 sequencing.
