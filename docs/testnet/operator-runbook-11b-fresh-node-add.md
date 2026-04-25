# Operator runbook — F5 5B testnet criterion 11b: fresh-node-add mid-flight

**Goal:** validate genesis-replay convergence with stacked-defer cases.
Add a fresh node to a running cluster, replay from genesis, observe
materialization-lag deferral correctly handles
`CountAncestorsByType` + `IsAncestor` + `ReadAtAnchor` simultaneously
without retry-storm thrash.

**Plan ref:** `docs/plans/implementation/f5-phase-5b-plan-v3.md` §7
criterion 11b + ChatGPT v2 review §12.5 (retry-storm amplification
hidden-error candidate).

**Scope assumption:** the running cluster is already deployed per Phase
3 of the F5 5B testnet verification (NOT the frozen reference cluster
`f4-combined-0e93f48`).

---

## Step 1 — pre-flight checks

Before adding a fresh node:

```bash
# Verify cluster health
curl -s https://testnet.aethernet.network/v1/status | jq

# Verify all current nodes are running the F5 5B image
for ip in <NODE_IPS>; do
  ssh -i ~/.ssh/aethernet.pem ubuntu@$ip 'docker ps --format "{{.Image}} {{.Status}}"'
done
```

Required state:
- Cluster has at least 1 EpochBoundary commit (so the fresh node's
  replay must catch up across an epoch transition — strongest test of
  stacked-defer)
- At least one in-flight settlement round in progress at the moment of
  fresh-node bootstrap (so the replay race is non-trivial)
- DAG size > 100 events (otherwise materialization is too fast to
  observe deferral)

If the cluster doesn't meet these conditions, generate load via
`cmd/aet-loadtest`:

```bash
go run ./cmd/aet-loadtest --target https://testnet.aethernet.network \
    --agents 20 --transfers 100 --tasks 30 --duration 120s
```

Wait for at least one EpochBoundary commit (visible in `dag_size`
growth + `peers` gauge).

---

## Step 2 — provision the fresh node

### Option A: ECS scale-up (3-node-existing topology)

```bash
# Scale up an additional task in the existing cluster
aws ecs update-service \
    --cluster aethernet-testnet \
    --service aethernet-node \
    --desired-count 2 \
    --region us-east-1
```

Wait for ECS rollout (~90s); the new task picks up the same task
definition (image + env vars + peer config).

### Option B: EC2 launch (5-node CLAUDE.md path)

If founder chose 5-node topology and one of the spare instances
(`aethernet-node-4` / `aethernet-node-5`) is to be used:

```bash
# SSH to spare instance
ssh -i ~/.ssh/aethernet.pem ubuntu@<SPARE_IP>

# Pull the F5 5B image
aws ecr get-login-password --region us-east-1 \
    | docker login --username AWS --password-stdin 435998721364.dkr.ecr.us-east-1.amazonaws.com
docker pull 435998721364.dkr.ecr.us-east-1.amazonaws.com/aethernet:<F5_5B_TAG>

# Wipe local state to force genesis replay
sudo rm -rf /data/aethernet/aethernet.db /data/aethernet/blobs

# DO NOT wipe /data/aethernet/node_keys/ or /data/aethernet/validator-manifest.json
# (those are persistent identity)

# Start the node, pointing at running peers
docker run -d \
    --name aethernet \
    --restart=always \
    -v /data/aethernet:/data \
    -p 8337:8337 -p 8338:8338 \
    -e AETHERNET_PEER="<RUNNING_NODE_1_IP>:8337,<RUNNING_NODE_2_IP>:8337,<RUNNING_NODE_3_IP>:8337" \
    -e AETHERNET_LISTEN="0.0.0.0:8337" \
    -e AETHERNET_API=":8338" \
    435998721364.dkr.ecr.us-east-1.amazonaws.com/aethernet:<F5_5B_TAG> \
    start --enable-admin-api
```

The `--enable-admin-api` flag is REQUIRED for criterion 12 cross-node
byte-equality verification (the `/v1/admin/ledger-snapshot` endpoint
needs to be served).

---

## Step 3 — observe genesis replay

Watch the fresh node tail logs in real time:

```bash
ssh -i ~/.ssh/aethernet.pem ubuntu@<FRESH_NODE_IP> \
    'docker logs -f aethernet 2>&1 | grep -E "replay|defer|materialization"'
```

Expected log signatures during replay:

1. **Sync from peers** — the fresh node fetches DAG events from existing
   peers via the F4 sync protocol.
2. **Per-event replay** — events apply in canonical order; recognition
   fabric re-derives projections.
3. **Materialization-lag deferrals** — when a TaskVerificationConsensus
   event commits BEFORE its CountAncestorsByType prerequisites have
   been replayed, the settler defers with one of:
   - `Cause=DeferredCauseDAGAncestorBFS` (gen-ledger BFS hit
     `dag.ErrEventNotFound`)
   - `Cause=DeferredCauseV1AncestorCheck` (V-1 activation IsAncestor
     deferred — should NOT fire today since
     `ReputationActivationEventID` is empty)
   - `Cause=DeferredCauseQualityLookup` (Quality.Lookup deferred —
     should NOT fire today since `QualityActivationEventID` is empty)
   - `Cause=DeferredCauseWLookup` (W.Lookup deferred — should NOT fire
     today since stub-W returns NeutralBP unconditionally)

Each deferral logs at `Info` level; expect ~tens to ~hundreds during a
moderately-sized replay.

---

## Step 4 — verify stacked-defer resolution

The "stacked-defer" test exercises the case where a single round's
derivation requires ALL THREE canonical-state primitives
simultaneously:

```bash
# Watch for round-id repeats with different DeferredCause values
ssh -i ~/.ssh/aethernet.pem ubuntu@<FRESH_NODE_IP> \
    'docker logs aethernet 2>&1 | grep -E "deferred.*round_id" | sort | uniq -c | sort -rn | head -20'
```

Expected: each round either (a) settles cleanly with
`Status=StatusDerived`, or (b) defers ≤ a small number of times before
converging. A round retrying indefinitely (>100 retries without
convergence) is a **HALT-TRIGGER** — capture the round_id + log excerpt
and stop the run.

Specific stacked-defer cases to monitor:
- Round R defers with `DeferredCauseDAGAncestorBFS`, then on retry
  (after ancestor materializes) defers with `DeferredCauseV1AncestorCheck`,
  then on retry settles cleanly.
- Same round R defers multiple times in succession with the same cause
  before resolving — each retry waits for incremental materialization.

---

## Step 5 — verify final convergence

After replay completes (DAG size on fresh node matches existing
peers; deferral log lines stop):

```bash
# Compare cluster-wide ledger state via the snapshot endpoint
go run ./cmd/aet invariants check \
    --url http://<FRESH_NODE_IP>:8338 \
    --peers <RUNNING_NODE_1_IP>:8338,<RUNNING_NODE_2_IP>:8338,<RUNNING_NODE_3_IP>:8338 \
    --threshold 0
```

Expected output: `divergence: 0 µAET` across all peers; exit code 0.

If divergence is non-zero after replay completes, that's a
**HALT-TRIGGER**: D-1 violation. Capture the snapshot diffs and stop
the run.

---

## Step 6 — record observations

Update `docs/testnet/f5-5b-verification-report.md` §3.2 with:

- Number of deferrals observed (per cause)
- Largest stacked-defer chain (single round_id with multiple deferrals)
- Time from fresh-node-bootstrap to convergence
- Final cross-node ledger-snapshot equality (`aet invariants check`
  output)
- Any retry-storm thrash observed (round retrying >10 times)

---

## Halt-triggers specific to criterion 11b

Per Plan v3 §5 + sub-spec §9 + §1.4:

- **Deferral loop divergence**: a node retries indefinitely without
  converging → halt
- **Stacked-defer retry-storm thrash**: a round retries >100 times in
  rapid succession → halt
- **Cross-node byte-equality failure** after replay completes → halt

If any halt-trigger fires, capture:
- Round ID
- Last 100 log lines from the fresh node
- Snapshot diffs from `aet invariants check --json`
- DAG size on fresh node + each existing peer

Surface to founder before retrying.

---

## Cleanup

After verification completes, scale the cluster back to the chosen
testnet topology:

### Option A cleanup
```bash
aws ecs update-service \
    --cluster aethernet-testnet \
    --service aethernet-node \
    --desired-count 1 \
    --region us-east-1
```

### Option B cleanup
The fresh EC2 instance can be left running for follow-up runs OR
stopped via `aws ec2 stop-instances --instance-ids <ID>` to save cost.

The frozen reference cluster (`aethernet-validator-1/2/3` running
`f4-combined-0e93f48`) is NOT touched by this runbook.

---

## Future-work notes

- Automated harness for criterion 11b: this runbook is currently
  manual. Future workstream could build an `aet testnet add-fresh-node`
  command that automates the bootstrap + observation cycle.
- Replay-time observability: log lines for deferral cause are present
  but not aggregated. A `aet stats deferrals` subcommand could
  summarize cause-frequency over a time window.
