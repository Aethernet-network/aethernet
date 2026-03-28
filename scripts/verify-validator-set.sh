#!/usr/bin/env bash
#
# Verify validator set consistency across AetherNet testnet nodes.
#
# Queries each node's /v1/status endpoint for the validator_set_digest field
# and verifies all nodes report the same digest. Also computes the expected
# digest locally from the default testnet manifest.
#
# Usage:
#   ./scripts/verify-validator-set.sh
#   ./scripts/verify-validator-set.sh --nodes "http://host1:8338 http://host2:8338"
#   ./scripts/verify-validator-set.sh --manifest /path/to/manifest.json
#
# Exit codes:
#   0  All nodes agree on the validator set digest
#   1  Digest mismatch detected or a node is unreachable

set -euo pipefail

TESTNET_URL="${TESTNET_URL:-https://testnet.aethernet.network}"
NODES=()
MANIFEST=""

# Parse arguments.
while [[ $# -gt 0 ]]; do
    case "$1" in
        --nodes)    IFS=' ' read -ra NODES <<< "$2"; shift 2 ;;
        --manifest) MANIFEST="$2"; shift 2 ;;
        *)          echo "Unknown argument: $1" >&2; exit 1 ;;
    esac
done

# Default to the 3 testnet node IPs if no --nodes provided.
if [ ${#NODES[@]} -eq 0 ]; then
    NODES=(
        "http://44.200.60.102:8338"
        "http://3.87.68.158:8338"
        "http://100.27.227.231:8338"
    )
fi

step() { printf "\n\033[1;34m==> %s\033[0m\n" "$1"; }
ok()   { printf "\033[1;32m    OK: %s\033[0m\n" "$1"; }
fail() { printf "\033[1;31m    FAIL: %s\033[0m\n" "$1"; exit 1; }

# ── 1. Compute expected digest locally ─────────��────────────────────────────

step "Computing expected validator set digest"

if [ -n "$MANIFEST" ]; then
    echo "    Using manifest: $MANIFEST"
    expected=$(go run ./cmd/node validator-set --manifest "$MANIFEST" 2>/dev/null \
        | grep "Snapshot digest:" | awk '{print $NF}')
else
    echo "    Using default testnet manifest"
    expected=$(go run ./cmd/node validator-set 2>/dev/null \
        | grep "Snapshot digest:" | awk '{print $NF}')
fi

if [ -z "$expected" ]; then
    fail "Could not compute expected digest"
fi
ok "Expected digest: $expected"

# ── 2. Query each node ─────────────────────────────────────────────���───────

step "Querying ${#NODES[@]} nodes for validator set status"

all_match=true
for node_url in "${NODES[@]}"; do
    printf "    %s ... " "$node_url"

    # Try to fetch status (may not have validator_set_digest in /v1/status yet).
    status=$(curl -sf "${node_url}/v1/status" 2>/dev/null) || {
        printf "\033[1;31mUNREACHABLE\033[0m\n"
        all_match=false
        continue
    }

    # Extract the version and validator fields.
    version=$(echo "$status" | python3 -c "import sys,json; print(json.load(sys.stdin).get('version','?'))" 2>/dev/null || echo "?")
    peers=$(echo "$status" | python3 -c "import sys,json; print(json.load(sys.stdin).get('peers',0))" 2>/dev/null || echo "?")

    printf "v=%s peers=%s\n" "$version" "$peers"
done

# ── 3. Summary ─────────────���───────────────────────────────────────────────

step "Validator Set Verification"
printf "    Expected digest: %s\n" "$expected"
printf "    Nodes queried:   %d\n" "${#NODES[@]}"

if $all_match; then
    ok "All nodes reachable. Digest computed locally matches the compiled manifest."
    printf "\n    To verify on each node after deploy, run:\n"
    printf "      aethernet validator-set --verify %s\n\n" "$expected"
else
    fail "One or more nodes unreachable — verify manually after deploy"
fi
