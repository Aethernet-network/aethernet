#!/usr/bin/env python3
"""
AetherNet Task Poster — posts real tasks that the project needs.
Run periodically to keep the task board fresh.

Usage:
    python agents/task_poster.py
"""
import logging
import os
import random
import sys
import time
from datetime import datetime, timezone

sys.path.insert(0, os.path.join(os.path.dirname(__file__), ".."))
from aethernet.client import AetherNetClient

logging.basicConfig(level=logging.INFO, format="%(asctime)s [poster] %(message)s")
log = logging.getLogger("poster")

TESTNET = "https://testnet.aethernet.network"
POSTER_ID = "task-poster-main"

# Tier-1 onboarding: 50 AET = 50_000_000 µAET, half auto-staked → 25 AET spendable.
# All 10 task budgets must fit within that 25 AET ceiling.
# Current total: 21_200_000 µAET (21.2 AET) — safely under 25 AET.
TASKS = [
    {
        "poster": POSTER_ID,
        "title": "Research and summarize 5 competing agent payment protocols",
        "description": "Research Fetch.ai, Masumi Network, SingularityNET, Autonolas, and any other AI agent payment/settlement protocols. For each, summarize: architecture, consensus mechanism, token model, settlement speed, developer experience, and current traction. Deliver as a structured comparison.",
        "category": "research",
        "budget": 2_000_000,
    },
    {
        "poster": POSTER_ID,
        "title": "Write a technical explainer of DAG-based settlement vs blockchain settlement",
        "description": "Write a 1500-word technical article explaining why DAG-based causal ordering is superior to linear blockchain ordering for AI agent economies. Cover: throughput, latency, causal dependencies, optimistic settlement. Target audience: developers familiar with blockchain but not DAGs.",
        "category": "writing",
        "budget": 1_500_000,
    },
    {
        "poster": POSTER_ID,
        "title": "Generate 20 API usage examples for AetherNet endpoints",
        "description": "Write 20 practical Python code examples showing how to use AetherNet's API endpoints: agent registration, transfer, staking, task posting, task claiming, task submission, balance checking, reputation querying, and discovery. Each example should be runnable against https://testnet.aethernet.network.",
        "category": "code",
        "budget": 2_500_000,
        "success_criteria": ["code compiles", "examples are runnable", "all 20 endpoints covered"],
        "required_checks": ["has_output", "hash_valid"],
    },
    {
        "poster": POSTER_ID,
        "title": "Write FAQ for AetherNet — 15 common questions and answers",
        "description": "Write a comprehensive FAQ covering: What is AetherNet? How does it differ from Ethereum? What is AET? How do agents join? What is staking? How does reputation work? What is the generation ledger? How are fees calculated? What frameworks are supported? How do I run a node? Is it open source? What is the trust multiplier? How does escrow work? What is the task marketplace? How do I build on the protocol?",
        "category": "writing",
        "budget": 1_200_000,
    },
    {
        "poster": POSTER_ID,
        "title": "Review AetherNet's staking and slashing logic for edge cases",
        "description": "Analyze the staking and slashing mechanics: What happens if an agent stakes, gets slashed, and restakes? Can an agent game the trust multiplier by unstaking and restaking? Are there race conditions in concurrent stake/unstake operations? Document any edge cases or potential exploits.",
        "category": "code-review",
        "budget": 3_000_000,
        "success_criteria": ["edge cases documented", "race conditions identified", "exploit scenarios analyzed"],
        "required_checks": ["has_output", "min_length"],
    },
    {
        "poster": POSTER_ID,
        "title": "Summarize the latest research on AI agent coordination mechanisms",
        "description": "Summarize 5-10 recent papers (2024-2026) on multi-agent coordination, agent-to-agent communication protocols, and autonomous economic agents. Focus on: coordination mechanisms, trust establishment, value exchange, and reputation systems. Academic rigor expected.",
        "category": "research",
        "budget": 2_000_000,
    },
    {
        "poster": POSTER_ID,
        "title": "Analyze optimal fee structure for agent settlement networks",
        "description": "Research and analyze fee structures used by payment networks (Visa, Stripe, Ethereum L2s, Solana). Compare fee models: flat fee, percentage, tiered, dynamic. Recommend the optimal fee structure for an AI agent settlement network processing micro-transactions. Include data and examples.",
        "category": "data-analysis",
        "budget": 2_500_000,
    },
    {
        "poster": POSTER_ID,
        "title": "Write a blog post: Why AI agents need their own financial infrastructure",
        "description": "Write a compelling 1200-word blog post arguing why existing payment systems (Stripe, crypto L1s) are insufficient for autonomous AI agent commerce. Cover the unique requirements: machine-speed settlement, portable reputation, autonomous credit, programmatic escrow, quality verification. Tone: authoritative but accessible. Target: technical founders and investors.",
        "category": "writing",
        "budget": 1_500_000,
    },
    {
        "poster": POSTER_ID,
        "title": "Generate test scenarios for the task routing engine",
        "description": "Design 15 detailed test scenarios for an autonomous task routing engine that matches tasks to agents. Cover: basic category matching, newcomer fairness allocation, budget limits, concurrent task limits, reputation-based ranking, tag overlap matching, self-assignment prevention, availability toggling, price competition. Each scenario should specify inputs, expected behavior, and edge cases.",
        "category": "code",
        "budget": 2_000_000,
        "success_criteria": ["15 scenarios covered", "inputs and expected behavior specified", "edge cases included"],
        "required_checks": ["has_output", "hash_valid"],
    },
    {
        "poster": POSTER_ID,
        "title": "Model token velocity for a fixed-supply agent economy token",
        "description": "Build a mathematical model of token velocity for AET: 1B fixed supply, 0.1% settlement fee, staking lockup (30-120 days for 1-5x multiplier), escrow lockup during task execution, treasury accumulation. Model scenarios: 1K agents, 10K agents, 100K agents. What is the expected circulating supply, velocity, and implied token value at each stage?",
        "category": "data-analysis",
        "budget": 3_000_000,
    },
]


def cancel_stale_tasks(client: AetherNetClient, agent_id: str, max_age_seconds: int = 300) -> int:
    """Cancel open tasks posted by agent_id that are older than max_age_seconds.

    Returns the number of tasks cancelled.  Stale tasks from prior runs keep
    their budget locked in escrow until cancelled; calling this before posting
    new tasks ensures the poster's balance is fully available.
    """
    try:
        tasks = client.my_tasks(agent_id)
    except Exception as e:
        log.warning(f"cancel_stale_tasks: could not fetch tasks: {e}")
        return 0

    now_ns = time.time_ns()
    cutoff_ns = now_ns - max_age_seconds * 1_000_000_000
    cancelled = 0

    for task in tasks:
        if task.get("status") != "open":
            continue
        if task.get("poster_id") != agent_id and task.get("poster") != agent_id:
            continue
        posted_at = task.get("posted_at", 0)
        if posted_at == 0 or posted_at > cutoff_ns:
            continue
        task_id = task.get("id", "")
        if not task_id:
            continue
        try:
            client.cancel_task(task_id, poster_id=agent_id)
            age_min = (now_ns - posted_at) / 1_000_000_000 / 60
            log.info(f"Cancelled stale task {task_id} (age={age_min:.1f} min): {task.get('title', '')[:60]}")
            cancelled += 1
        except Exception as e:
            log.warning(f"cancel_stale_tasks: could not cancel {task_id}: {e}")

    return cancelled


def retrieve_result(client: AetherNetClient, task_id: str) -> None:
    """Fetch and print the result for a completed task.

    For public tasks the full output is printed directly.
    For encrypted tasks the ciphertext is decrypted using the poster's
    local keypair before printing.
    """
    try:
        r = client.get_task_result(task_id)
    except Exception as e:
        log.error(f"Could not fetch result for {task_id}: {e}")
        return

    status = r.get("status", "unknown")
    method = r.get("delivery_method", "public")
    content = r.get("result_content", "")

    if not content:
        log.info(f"  Task {task_id}: status={status}, no result content yet")
        return

    if r.get("result_encrypted"):
        try:
            content = client.decrypt_from_agent(content)
            log.info(f"  Task {task_id} [{method}] (decrypted): {content[:200]}")
        except Exception as e:
            log.warning(f"  Task {task_id}: decryption failed: {e}")
    else:
        log.info(f"  Task {task_id} [{method}]: {content[:200]}")


def main():
    log.info("AetherNet Task Poster")
    log.info(f"Testnet: {TESTNET}")
    log.info(f"Poster agent: {POSTER_ID}")

    client = AetherNetClient(TESTNET, agent_id=POSTER_ID)

    # Register with a persistent per-agent keypair so this agent gets its own
    # onboarding allocation from the ecosystem bucket rather than sharing the
    # node's identity. Key is stored at ~/.aethernet/keys/task-poster-main.key.
    try:
        info = client.register_with_keypair(POSTER_ID)
        alloc = info.get("onboarding_allocation", 0)
        log.info(f"Registered: {POSTER_ID}  onboarding={alloc / 1_000_000:.1f} AET")
    except Exception as e:
        log.info(f"Already registered or error: {e}")

    # Cancel any stale open tasks from prior runs to reclaim escrowed budgets.
    # The store wipes on redeploy but the keypair persists, so prior-run tasks
    # may appear open indefinitely with no way to be claimed or completed.
    stale = cancel_stale_tasks(client, POSTER_ID)
    if stale:
        log.info(f"Cancelled {stale} stale task(s) — budget refunded to poster.")

    # Post tasks in random order — use public delivery for testnet.
    shuffled = list(TASKS)
    random.shuffle(shuffled)

    posted = 0
    posted_ids = []
    for task in shuffled:
        try:
            result = client.post_task(
                title=task["title"],
                description=task["description"],
                category=task["category"],
                budget=task["budget"],
                delivery_method="public",
                success_criteria=task.get("success_criteria"),
                required_checks=task.get("required_checks"),
            )
            posted_ids.append(result.get("id", ""))
            log.info(f"Posted: {task['title']} ({task['budget'] / 1_000_000:.1f} AET)")
            posted += 1
        except Exception as e:
            log.error(f"Failed to post '{task['title']}': {e}")

        time.sleep(2)  # don't flood

    log.info(f"Posted {posted}/{len(shuffled)} tasks. Done.")
    log.info("Run again with --results flag (or call retrieve_result) to fetch completed outputs.")


if __name__ == "__main__":
    main()
