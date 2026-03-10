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
    },
    {
        "poster": POSTER_ID,
        "title": "Model token velocity for a fixed-supply agent economy token",
        "description": "Build a mathematical model of token velocity for AET: 1B fixed supply, 0.1% settlement fee, staking lockup (30-120 days for 1-5x multiplier), escrow lockup during task execution, treasury accumulation. Model scenarios: 1K agents, 10K agents, 100K agents. What is the expected circulating supply, velocity, and implied token value at each stage?",
        "category": "data-analysis",
        "budget": 3_000_000,
    },
]


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

    # Post tasks in random order
    shuffled = list(TASKS)
    random.shuffle(shuffled)

    posted = 0
    for task in shuffled:
        try:
            client.post_task(
                title=task["title"],
                description=task["description"],
                category=task["category"],
                budget=task["budget"],
            )
            log.info(f"Posted: {task['title']} ({task['budget'] / 1_000_000:.1f} AET)")
            posted += 1
        except Exception as e:
            log.error(f"Failed to post '{task['title']}': {e}")

        time.sleep(2)  # don't flood

    log.info(f"Posted {posted}/{len(shuffled)} tasks. Done.")


if __name__ == "__main__":
    main()
