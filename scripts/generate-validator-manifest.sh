#!/usr/bin/env bash
#
# Generate a validator manifest from the 3-node testnet's actual public keys.
#
# Each testnet node persists its Ed25519 keypair at /data/node_keys/identity.json.
# The node's agent ID (= hex-encoded public key) is returned by GET /v1/status.
# This script collects the 3 agent IDs and generates a manifest file that all
# nodes must load via AETHERNET_VALIDATOR_MANIFEST.
#
# Usage:
#   ./scripts/generate-validator-manifest.sh
#   ./scripts/generate-validator-manifest.sh --output /path/to/manifest.json
#   ./scripts/generate-validator-manifest.sh --nodes "http://host1:8338 http://host2:8338 http://host3:8338"
#
# Prerequisites: curl, python3 (for JSON parsing), go (for manifest generation)

set -euo pipefail

OUTPUT="validator-manifest.json"
NODES=()

while [[ $# -gt 0 ]]; do
    case "$1" in
        --output)  OUTPUT="$2"; shift 2 ;;
        --nodes)   IFS=' ' read -ra NODES <<< "$2"; shift 2 ;;
        *)         echo "Unknown argument: $1" >&2; exit 1 ;;
    esac
done

if [ ${#NODES[@]} -eq 0 ]; then
    NODES=(
        "http://44.200.60.102:8338"
        "http://3.87.68.158:8338"
        "http://100.27.227.231:8338"
    )
fi

step() { printf "\n\033[1;34m==> %s\033[0m\n" "$1"; }
ok()   { printf "\033[1;32m    OK\033[0m\n"; }
fail() { printf "\033[1;31m    FAIL: %s\033[0m\n" "$1"; exit 1; }

# ── 1. Collect agent IDs from all nodes ──────────────────────────────────────

step "Collecting agent IDs from ${#NODES[@]} nodes"

AGENT_IDS=()
for i in "${!NODES[@]}"; do
    node_url="${NODES[$i]}"
    printf "    Node %d (%s)... " "$i" "$node_url"

    status=$(curl -sf "$node_url/v1/status" 2>/dev/null) || fail "unreachable: $node_url"
    agent_id=$(echo "$status" | python3 -c "import sys,json; print(json.load(sys.stdin).get('agent_id',''))" 2>/dev/null)

    if [ -z "$agent_id" ] || [ "$agent_id" = "None" ]; then
        fail "no agent_id in status response from $node_url"
    fi

    AGENT_IDS+=("$agent_id")
    printf "%s\n" "${agent_id:0:16}..."
done
ok

# ── 2. Check for duplicate keys ──────────────────────────────────────────────

step "Checking for duplicate keys"
declare -A seen
for id in "${AGENT_IDS[@]}"; do
    if [[ -v "seen[$id]" ]]; then
        fail "duplicate agent_id: $id — nodes must have distinct keys"
    fi
    seen["$id"]=1
done
ok

# ── 3. Generate manifest JSON ────────────────────────────────────────────────

step "Generating manifest"

# Build the JSON using python3 for proper escaping.
python3 -c "
import json, sys

agent_ids = sys.argv[1:]
entries = []
for i, aid in enumerate(agent_ids):
    entries.append({
        'validator_id': f'seat-{aid[:16]}',
        'operator_agent_id': aid,
        'consensus_public_key': aid,
        'key_epoch': 1,
        'bonded_stake': 50000000000,  # 50,000 AET
        'initial_status': 'active',
        'reputation_baseline': 5000,
    })

manifest = {'entries': entries}
print(json.dumps(manifest, indent=2))
" "${AGENT_IDS[@]}" > "$OUTPUT"

printf "    Written to: %s\n" "$OUTPUT"
printf "    Seats: %d\n" "${#AGENT_IDS[@]}"
ok

# ── 4. Verify the manifest ───────────────────────────────────────────────────

step "Verifying manifest"
go run ./cmd/node validator-set --manifest "$OUTPUT" 2>/dev/null || fail "manifest verification failed"
ok

# ── 5. Compute digest ────────────────────────────────────────────────────────

step "Computing snapshot digest"
DIGEST=$(go run ./cmd/node validator-set --manifest "$OUTPUT" 2>/dev/null | grep "Snapshot digest:" | awk '{print $NF}')
printf "    Digest: %s\n" "$DIGEST"
ok

# ── 6. Summary ───────────────────────────────────────────────────────────────

printf "\n"
printf "\033[1;32m  Manifest generated: %s\033[0m\n" "$OUTPUT"
printf "\033[1;32m  Snapshot digest:    %s\033[0m\n" "$DIGEST"
printf "\n"
printf "  Next steps:\n"
printf "    1. Copy %s to all 3 nodes (e.g. S3, EFS, ConfigMap)\n" "$OUTPUT"
printf "    2. Set AETHERNET_VALIDATOR_MANIFEST=%s on all nodes\n" "$OUTPUT"
printf "    3. Deploy all 3 nodes simultaneously\n"
printf "    4. Verify: go run ./cmd/aet-e2e\n"
printf "\n"
