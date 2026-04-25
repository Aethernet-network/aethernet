# F5 Phase 5B testnet verification report

**Status:** scaffold (Phase 1.4 prep). Populated during Phase 3 (deploy)
+ Phase 4 (report).

**Branch under verification:** `feat/f5-5b-derivation`
**Reference frozen cluster:** `f4-combined-0e93f48` (3-validator EC2 — DO NOT TOUCH)
**Plan ref:** `docs/plans/implementation/f5-phase-5b-plan-v3.md` §7 success criteria 11a + 11b + 12

---

## 1. Cluster topology

(populated during Phase 2 topology decision + Phase 3 deploy)

- Topology choice: __ (3-node ECS / 5-node EC2 / hybrid)
- Cluster identifier: __
- Node IPs:
  - node-1: __
  - node-2: __
  - node-3: __
  - node-4 (if 5-node): __
  - node-5 (if 5-node): __
- ALB / load balancer: __
- Image deployed: __ (ECR tag)
- Image SHA: __
- Task definitions / EC2 instance types: __

## 2. Run profile

- Deploy timestamp: __
- Verification window start: __
- Verification window end: __
- Total duration: __
- Traffic profile: __
  - Concurrent agents: __
  - Tasks posted: __
  - Settlements observed: __
  - EpochBoundary commits observed: __

## 3. Per-criterion observations

### 3.1 Criterion 11a — Crash-mid-apply self-heal

**Plan ref:** Plan v3 §3.3 (crash-position table) + §7 criterion 11a.
**Hook:** `AETHERNET_CRASH_AFTER_NTH_RECORD` env-flag (see commit
`testnet(escrow): crash-injection feature flag`). Set to N to
deterministically exit after records `0..N-1` are fully applied; record
N is untouched at crash time.

#### Setup
(populated during run)
- Round selected: __ (round_id, task_id)
- Round derivation produces __ records (worker, validators, treasury,
  gen-ledger ancestors)
- Crash position chosen: N=__ (records 0..N-1 fully applied; record N
  not started)
- Node selected for crash: __

#### Observations
| Step | Observation | Evidence (log line / ledger hash) |
|------|-------------|-----------------------------------|
| (a) Restart node re-derives same canonical records (D-1) | __ | __ |
| (b) Ledger ErrDuplicateEntry returns benign for already-applied records | __ | __ |
| (c) Paid-flag projection catches up via skip-optimization READ on remaining records | __ | __ |
| (d) Final ledger state byte-identical across all nodes | __ | __ |

#### Result
- [ ] PASS  / [ ] FAIL  / [ ] HALT-TRIGGERED

#### Notes
__

---

### 3.2 Criterion 11b — Genesis-replay convergence with stacked-defer cases

**Plan ref:** Plan v3 §7 criterion 11b + ChatGPT v2 review §12.5
secondary hidden-error candidate (retry-storm amplification).
**Runbook:** `docs/testnet/operator-runbook-11b-fresh-node-add.md`

#### Setup
(populated during run)
- Fresh node bootstrap method: __ (ECS scale-up / EC2 launch / hybrid)
- Genesis-replay window start: __
- Materialization-lag window: __
- Stacked-defer trigger round: __ (round_id, task_id)
- Round simultaneously requires:
  - [ ] CountAncestorsByType deferral (canonical-epoch sub-spec)
  - [ ] IsAncestor deferral (V-1 W activation; expected to be skip
    today since `ReputationActivationEventID` is empty per
    `internal/settlement/derivation/FORWARD_NOTES.md` §1)
  - [ ] IsAncestor deferral (Quality activation; expected to be skip
    today since `QualityActivationEventID` is empty)
  - [ ] ReadAtAnchor deferral (gen-ledger ancestry; tests Fix #1
    anchor-scoped depth path under realistic DAG topology)

#### Observations
| Phase | Observation | Evidence |
|-------|-------------|----------|
| Materialization-lag deferral observed for CountAncestorsByType | __ | __ |
| Materialization-lag deferral observed for ReadAtAnchor | __ | __ |
| Stacked-defer cascade resolved without retry-storm thrash | __ | __ |
| Final convergence: fresh node reaches byte-identical ledger | __ | __ |

#### Result
- [ ] PASS  / [ ] FAIL  / [ ] HALT-TRIGGERED

#### Notes
__

---

### 3.3 Criterion 12 — Cross-node byte-equality on N-node deploy

**Plan ref:** Plan v3 §7 criterion 12.
**Substrate:** `/v1/admin/ledger-snapshot` endpoint + `aet invariants check` CLI.

#### Setup
(populated during run)
- Sustained settlement load: __ (settlements per minute)
- Multi-epoch verification: at least __ EpochBoundary commits during
  run
- Cross-node-invariant monitor: `aet invariants check --url
  https://testnet.aethernet.network --peers
  http://172.31.x.y:8338,...` — frequency __ minutes

#### Observations

##### Per-round byte-equality
| Round | Settlement records | Cross-node hash equality | Notes |
|-------|--------------------|-----------------------------|-------|
| __    | __                  | __                          | __    |

##### EpochBoundary canonical convergence
| Epoch | First-canonical-EpochBoundary EventID | Same on all N nodes? | LK dedup observation |
|-------|---------------------------------------|----------------------|----------------------|
| __    | __                                     | __                   | __                   |

##### CountAncestorsByType cross-node parity
| Sample descendant | Type queried | Counts (per node) | Equal? |
|---|---|---|---|
| __ | __ | __ | __ |

##### PayoutRecord canonical_id byte-equality
| Settlement (round_id, task_id) | Records | canonical_id values across nodes | D-1 + U-1 |
|---|---|---|---|
| __ | __ | __ | __ |

#### Result
- [ ] PASS  / [ ] FAIL  / [ ] HALT-TRIGGERED

#### Notes
__

---

## 4. Cross-cuts

### 4.1 Item 1 composite enforcement under load

Verifies the multi-AI Item 1 mechanical-enforcement composite holds at
runtime — no construction-site bypasses canonical-frozen activation
EventID semantics; lint flags don't fire (build-time guarantee, but
verify no runtime workaround pattern emerges).

| Check | Observation |
|-------|-------------|
| All `derivation.NewDerivationInputs` calls succeed validation at startup | __ |
| No runtime panics from `isActivated` in stacked-defer scenarios | __ |
| No reflection / unsafe-typing patterns observed in settler logs | __ |

### 4.2 Fix #1 anchor-scoped depth verification

Verifies the multi-AI Fix #1 holds: gen-ledger weight calculations use
anchor-scoped BFS depth (not unrestricted-BFS depth). Canonical depth-
source pinned via `TestReadAtAnchor_DepthIsAnchorScopedNotShortestPath`
at unit layer; testnet verifies under realistic DAG topology.

| Check | Observation |
|-------|-------------|
| Gen-ledger payouts on settlements with both in-scope and out-of-scope ancestors converge across nodes | __ |
| `quality / depth²` weighting uses BFS-tracked depth (depth ≥ 1 always; never depth=0 from a different BFS) | __ |
| No gen-ledger weight divergence detected across nodes | __ |

---

## 5. Halt-trigger evaluation

Per Plan v3 §5 + sub-spec §9 + §1.4 admission cross-check, the
following halt-triggers are monitored throughout the testnet window:

- [ ] Cross-node byte-equality failure on any round → halt
- [ ] Deferral loop divergence (a node retries indefinitely without
      converging) → halt
- [ ] Stacked-defer retry-storm thrash → halt
- [ ] EpochBoundary admission cross-check rejecting valid events under
      load → halt
- [ ] LogicalKeyConsumer dedup failing under multi-emit pressure → halt
- [ ] Gen-ledger weight divergence across nodes (Fix #1 regression) →
      halt
- [ ] DerivationInputs construction-time validation panic in production
      (constructor caught a misuse) → halt and surface

**Triggered:** none / __ (specify trigger and time)

---

## 6. Deferred-defect appendix

Items that surfaced during testnet that weren't caught at unit / race-
test phase. Each item gets a forward note here for later work.

### 6.1 (placeholder for surfaced items)
__

---

## 7. Forward notes carrying to F5 5B completion gate

Per founder direction (NOT pre-testnet scope; documented architectural
carries):

- **ReputationActivationEventID const-flip V-1 hole at upgrade time** —
  locked Reputation-and-Consensus-Integrity workstream owns resolution.
  Reference: `internal/settlement/derivation/FORWARD_NOTES.md` §1.
- **EpochBoundary signer canonical-validator-snapshot binding deferral**
  — depends on locked workstream's snapshot infrastructure. Reference:
  `internal/settlement/derivation/FORWARD_NOTES.md` §2.
- **Out-of-set validator key participation extension** — same scope as
  signer-snapshot-binding.

---

## 8. Final disposition

- [ ] All criteria PASSED — F5 5B testnet verification complete; ready
      for F5 5B completion gate report.
- [ ] One or more criteria FAILED — investigation required before
      retry.
- [ ] HALT TRIGGERED — root cause analysis + fix before re-attempt.

**Sign-off:**
- Verification engineer: __
- Architect review: __
- Founder approval: __
- Date: __
