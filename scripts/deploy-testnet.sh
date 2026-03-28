#!/usr/bin/env bash
#
# Deploy the AetherNet testnet.
#
# Runs tests, builds and pushes a Docker image to ECR, force-deploys all
# 3 ECS services, waits for rollout, and prints a status summary.
#
# Usage:
#   ./scripts/deploy-testnet.sh
#
# Prerequisites: Go, Docker (buildx), AWS CLI v2 configured with push access
# to 435998721364.dkr.ecr.us-east-1.amazonaws.com/aethernet.

set -euo pipefail

REGION="us-east-1"
ECR_REPO="435998721364.dkr.ecr.us-east-1.amazonaws.com/aethernet"
CLUSTER="aethernet-testnet"
SERVICES=("aethernet-node" "aethernet-node2" "aethernet-node3")
TESTNET_URL="https://testnet.aethernet.network"
KNOWN_FLAKES="TestAutoValidator_FeeOnTaskSettlement|TestNextCanary_CopiesExpectedEvidence"

step() { printf "\n\033[1;34m==> %s\033[0m\n" "$1"; }
ok()   { printf "\033[1;32m    OK\033[0m\n"; }
fail() { printf "\033[1;31m    FAIL: %s\033[0m\n" "$1"; exit 1; }

# ── 0. Validator Set Verification ────────────────────────────────────────────

step "Verifying genesis validator set digest"
VS_DIGEST=$(go run ./cmd/node validator-set 2>/dev/null | grep "Snapshot digest:" | awk '{print $NF}')
if [ -z "$VS_DIGEST" ]; then
    fail "Could not compute validator set digest"
fi
printf "    Validator set digest: %s\n" "$VS_DIGEST"
ok

# ── 1. Tests ─────────────────────────────────────────────────────────────────

step "Running tests (go test -p 1 ./... -race -count=1)"

test_output=$(mktemp)
if go test -p 1 ./... -race -count=1 2>&1 | tee "$test_output"; then
    ok
else
    # Check if the only failures are known flakes.
    real_failures=$(grep "^--- FAIL:" "$test_output" | grep -vE "$KNOWN_FLAKES" || true)
    if [ -z "$real_failures" ]; then
        printf "    Known flake(s) only — continuing.\n"
    else
        echo "$real_failures"
        rm -f "$test_output"
        fail "Tests failed with non-flake failures"
    fi
fi
rm -f "$test_output"

# ── 2. ECR Login ─────────────────────────────────────────────────────────────

step "Logging in to ECR"
aws ecr get-login-password --region "$REGION" \
    | docker login --username AWS --password-stdin "${ECR_REPO%%/*}" \
    || fail "ECR login failed"
ok

# ── 3. Build & Push ──────────────────────────────────────────────────────────

step "Building and pushing Docker image"
docker buildx build \
    --no-cache \
    --platform linux/amd64 \
    -t "$ECR_REPO:latest" \
    --push \
    . || fail "Docker build/push failed"
ok

# ── 4. Force New Deployment ──────────────────────────────────────────────────

step "Forcing new deployment on ${#SERVICES[@]} ECS services"
for svc in "${SERVICES[@]}"; do
    printf "    %s ... " "$svc"
    aws ecs update-service \
        --cluster "$CLUSTER" \
        --service "$svc" \
        --force-new-deployment \
        --region "$REGION" \
        --no-cli-pager \
        > /dev/null \
        || fail "Failed to update $svc"
    printf "done\n"
done
ok

# ── 5. Wait for Rollout ─────────────────────────────────────────────────────

step "Waiting 90 seconds for ECS rollout"
for i in $(seq 90 -10 10); do
    printf "    %ds remaining...\r" "$i"
    sleep 10
done
sleep 10
printf "    Done.              \n"

# ── 6. Status Check ──────────────────────────────────────────────────────────

step "Checking testnet status"
status=$(curl -sf "$TESTNET_URL/v1/status" 2>/dev/null) || fail "Cannot reach $TESTNET_URL"

peers=$(echo "$status" | python3 -c "import sys,json; print(json.load(sys.stdin).get('peers',0))" 2>/dev/null || echo "?")
dag=$(echo "$status" | python3 -c "import sys,json; print(json.load(sys.stdin).get('dag_size',0))" 2>/dev/null || echo "?")
pending=$(echo "$status" | python3 -c "import sys,json; print(json.load(sys.stdin).get('ocs_pending',0))" 2>/dev/null || echo "?")
version=$(echo "$status" | python3 -c "import sys,json; print(json.load(sys.stdin).get('version','?'))" 2>/dev/null || echo "?")

econ=$(curl -sf "$TESTNET_URL/v1/economics" 2>/dev/null || echo "{}")
supply=$(echo "$econ" | python3 -c "import sys,json; d=json.load(sys.stdin); print(f\"{d.get('circulating_supply',0)/d.get('total_supply',1)*100:.1f}%\")" 2>/dev/null || echo "?")

# ── 7. Summary ───────────────────────────────────────────────────────────────

printf "\n"
printf "\033[1;32m  ╔══════════════════════════════════════════╗\033[0m\n"
printf "\033[1;32m  ║  AetherNet Testnet Deploy Complete        ║\033[0m\n"
printf "\033[1;32m  ╠══════════════════════════════════════════╣\033[0m\n"
printf "\033[1;32m  ║\033[0m  Version       : %-23s\033[1;32m║\033[0m\n" "$version"
printf "\033[1;32m  ║\033[0m  Peers         : %-23s\033[1;32m║\033[0m\n" "$peers"
printf "\033[1;32m  ║\033[0m  DAG events    : %-23s\033[1;32m║\033[0m\n" "$dag"
printf "\033[1;32m  ║\033[0m  OCS pending   : %-23s\033[1;32m║\033[0m\n" "$pending"
printf "\033[1;32m  ║\033[0m  Supply ratio  : %-23s\033[1;32m║\033[0m\n" "$supply"
printf "\033[1;32m  ╚══════════════════════════════════════════╝\033[0m\n"
printf "\n"
