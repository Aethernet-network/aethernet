#!/usr/bin/env bash
#
# Check AetherNet testnet health.
#
# Queries the testnet API, pulls recent ECS logs, compares Cloud Map
# instances against running ECS tasks, and prints a summary.
#
# Usage:
#   ./scripts/check-testnet.sh

set -euo pipefail

REGION="us-east-1"
CLUSTER="aethernet-testnet"
SERVICES=("aethernet-node" "aethernet-node2" "aethernet-node3")
TESTNET_URL="https://testnet.aethernet.network"

step() { printf "\n\033[1;34m==> %s\033[0m\n" "$1"; }

# ── 1. API Status ────────────────────────────────────────────────────────────

step "Testnet API Status"
status=$(curl -sf "$TESTNET_URL/v1/status" 2>/dev/null) || { printf "    Cannot reach %s\n" "$TESTNET_URL"; exit 1; }

peers=$(echo "$status" | python3 -c "import sys,json; print(json.load(sys.stdin).get('peers',0))")
dag=$(echo "$status" | python3 -c "import sys,json; print(json.load(sys.stdin).get('dag_size',0))")
pending=$(echo "$status" | python3 -c "import sys,json; print(json.load(sys.stdin).get('ocs_pending',0))")
version=$(echo "$status" | python3 -c "import sys,json; print(json.load(sys.stdin).get('version','?'))")

econ=$(curl -sf "$TESTNET_URL/v1/economics" 2>/dev/null || echo "{}")
total_supply=$(echo "$econ" | python3 -c "import sys,json; print(json.load(sys.stdin).get('total_supply',0))")
circulating=$(echo "$econ" | python3 -c "import sys,json; print(json.load(sys.stdin).get('circulating_supply',0))")
treasury=$(echo "$econ" | python3 -c "import sys,json; print(json.load(sys.stdin).get('treasury_balance', json.load(open('/dev/stdin') if False else sys.stdin).get('treasury_accrued',0)))" 2>/dev/null || echo "0")
treasury=$(echo "$econ" | python3 -c "import sys,json; d=json.load(sys.stdin); print(d.get('treasury_balance', d.get('treasury_accrued',0)))")

printf "    Version      : %s\n" "$version"
printf "    Peers        : %s\n" "$peers"
printf "    DAG events   : %s\n" "$dag"
printf "    OCS pending  : %s\n" "$pending"
printf "    Total supply : %s µAET\n" "$total_supply"
printf "    Circulating  : %s µAET\n" "$circulating"
printf "    Treasury     : %s µAET\n" "$treasury"

# ── 2. ECS Service Status ────────────────────────────────────────────────────

step "ECS Services"
running_ips=()
for svc in "${SERVICES[@]}"; do
    # Get task ARNs for this service.
    task_arns=$(aws ecs list-tasks \
        --cluster "$CLUSTER" \
        --service-name "$svc" \
        --desired-status RUNNING \
        --region "$REGION" \
        --query "taskArns[]" \
        --output text 2>/dev/null || echo "")

    if [ -z "$task_arns" ] || [ "$task_arns" = "None" ]; then
        printf "    %-20s  \033[1;31mNO RUNNING TASKS\033[0m\n" "$svc"
        continue
    fi

    # Describe tasks to get IPs and health.
    task_info=$(aws ecs describe-tasks \
        --cluster "$CLUSTER" \
        --tasks $task_arns \
        --region "$REGION" \
        --query "tasks[].{status:lastStatus,health:healthStatus,ip:containers[0].networkInterfaces[0].privateIpv4Address}" \
        --output json 2>/dev/null || echo "[]")

    task_status=$(echo "$task_info" | python3 -c "import sys,json; t=json.load(sys.stdin); print(t[0].get('status','?') if t else '?')")
    task_ip=$(echo "$task_info" | python3 -c "import sys,json; t=json.load(sys.stdin); print(t[0].get('ip','?') if t else '?')")

    if [ "$task_ip" != "?" ] && [ "$task_ip" != "None" ] && [ -n "$task_ip" ]; then
        running_ips+=("$task_ip")
    fi

    printf "    %-20s  %s  IP=%s\n" "$svc" "$task_status" "$task_ip"
done

# ── 3. Cloud Map Instances ───────────────────────────────────────────────────

step "Cloud Map Instances"
cm_service_id="${AETHERNET_CLOUDMAP_SERVICE_ID:-}"
if [ -z "$cm_service_id" ]; then
    # Try to find the service ID from the environment or a known service name.
    cm_service_id=$(aws servicediscovery list-services \
        --region "$REGION" \
        --query "Services[?Name=='nodes'].Id" \
        --output text 2>/dev/null || echo "")
fi

if [ -z "$cm_service_id" ] || [ "$cm_service_id" = "None" ]; then
    printf "    Cloud Map service ID not found. Set AETHERNET_CLOUDMAP_SERVICE_ID or\n"
    printf "    ensure a 'nodes' service exists in Cloud Map.\n"
else
    cm_instances=$(aws servicediscovery list-instances \
        --service-id "$cm_service_id" \
        --region "$REGION" \
        --query "Instances[].{id:Id,ip:Attributes.AWS_INSTANCE_IPV4}" \
        --output json 2>/dev/null || echo "[]")

    cm_ips=$(echo "$cm_instances" | python3 -c "import sys,json; [print(i.get('ip',i.get('id','?'))) for i in json.load(sys.stdin)]" 2>/dev/null || echo "")
    cm_count=$(echo "$cm_instances" | python3 -c "import sys,json; print(len(json.load(sys.stdin)))")

    printf "    Registered instances: %s\n" "$cm_count"

    # Compare Cloud Map IPs against running task IPs.
    stale_count=0
    while IFS= read -r cm_ip; do
        [ -z "$cm_ip" ] && continue
        found=false
        for rip in "${running_ips[@]+"${running_ips[@]}"}"; do
            if [ "$cm_ip" = "$rip" ]; then
                found=true
                break
            fi
        done
        if $found; then
            printf "    %s  \033[1;32mACTIVE\033[0m\n" "$cm_ip"
        else
            printf "    %s  \033[1;31mSTALE\033[0m (not in running tasks)\n" "$cm_ip"
            stale_count=$((stale_count + 1))
        fi
    done <<< "$cm_ips"

    if [ "$stale_count" -gt 0 ]; then
        printf "\n    \033[1;33m%d stale instance(s) in Cloud Map\033[0m\n" "$stale_count"
    fi
fi

# ── 4. Recent Logs ───────────────────────────────────────────────────────────

step "Recent Logs (last 20 lines from aethernet-node)"
aws logs get-log-events \
    --log-group-name "/ecs/aethernet" \
    --log-stream-name "$(aws logs describe-log-streams \
        --log-group-name "/ecs/aethernet" \
        --order-by LastEventTime \
        --descending \
        --limit 1 \
        --region "$REGION" \
        --query "logStreams[0].logStreamName" \
        --output text 2>/dev/null)" \
    --limit 20 \
    --region "$REGION" \
    --query "events[].message" \
    --output text 2>/dev/null \
    | tr '\t' '\n' \
    | tail -20 \
    | while IFS= read -r line; do printf "    %s\n" "$line"; done \
    || printf "    (could not fetch logs)\n"

# ── 5. Summary ───────────────────────────────────────────────────────────────

printf "\n"
health="HEALTHY"
color="\033[1;32m"
if [ "$peers" = "0" ]; then
    health="DEGRADED (0 peers)"
    color="\033[1;33m"
fi

printf "${color}  ╔══════════════════════════════════════════╗\033[0m\n"
printf "${color}  ║  Testnet: %-31s ║\033[0m\n" "$health"
printf "${color}  ║  Peers: %-2s  DAG: %-7s  Pending: %-5s ║\033[0m\n" "$peers" "$dag" "$pending"
printf "${color}  ╚══════════════════════════════════════════╝\033[0m\n"
printf "\n"
