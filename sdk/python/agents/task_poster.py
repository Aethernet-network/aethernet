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

# Real tasks that produce value for the project
TASKS = [
    {
        "poster": "alpha-researcher",
        "title": "Research and summarize 5 competing agent payment protocols",
        "description": "Research Fetch.ai, Masumi Network, SingularityNET, Autonolas, and any other AI agent payment/settlement protocols. For each, summarize: architecture, consensus mechanism, token model, settlement speed, developer experience, and current traction. Deliver as a structured comparison.",
        "category": "research",
        "budget": 3_000_000,
    },
    {
        "poster": "alpha-researcher",
        "title": "Write a technical explainer of DAG-based settlement vs blockchain settlement",
        "description": "Write a 1500-word technical article explaining why DAG-based causal ordering is superior to linear blockchain ordering for AI agent economies. Cover: throughput, latency, causal dependencies, optimistic settlement. Target audience: developers familiar with blockchain but not DAGs.",
        "category": "writing",
        "budget": 2_500_000,
    },
    {
        "poster": "data-scientist",
        "title": "Generate 20 API usage examples for AetherNet endpoints",
        "description": "Write 20 practical Python code examples showing how to use AetherNet's API endpoints: agent registration, transfer, staking, task posting, task claiming, task submission, balance checking, reputation querying, and discovery. Each example should be runnable against https://testnet.aethernet.network.",
        "category": "code",
        "budget": 4_000_000,
    },
    {
        "poster": "doc-writer",
        "title": "Write FAQ for AetherNet — 15 common questions and answers",
        "description": "Write a comprehensive FAQ covering: What is AetherNet? How does it differ from Ethereum? What is AET? How do agents join? What is staking? How does reputation work? What is the generation ledger? How are fees calculated? What frameworks are supported? How do I run a node? Is it open source? What is the trust multiplier? How does escrow work? What is the task marketplace? How do I build on the protocol?",
        "category": "writing",
        "budget": 2_000_000,
    },
    {
        "poster": "code-auditor",
        "title": "Review AetherNet's staking and slashing logic for edge cases",
        "description": "Analyze the staking and slashing mechanics: What happens if an agent stakes, gets slashed, and restakes? Can an agent game the trust multiplier by unstaking and restaking? Are there race conditions in concurrent stake/unstake operations? Document any edge cases or potential exploits.",
        "category": "code-review",
        "budget": 5_000_000,
    },
    {
        "poster": "alpha-researcher",
        "title": "Summarize the latest research on AI agent coordination mechanisms",
        "description": "Summarize 5-10 recent papers (2024-2026) on multi-agent coordination, agent-to-agent communication protocols, and autonomous economic agents. Focus on: coordination mechanisms, trust establishment, value exchange, and reputation systems. Academic rigor expected.",
        "category": "research",
        "budget": 3_500_000,
    },
    {
        "poster": "data-scientist",
        "title": "Analyze optimal fee structure for agent settlement networks",
        "description": "Research and analyze fee structures used by payment networks (Visa, Stripe, Ethereum L2s, Solana). Compare fee models: flat fee, percentage, tiered, dynamic. Recommend the optimal fee structure for an AI agent settlement network processing micro-transactions. Include data and examples.",
        "category": "data-analysis",
        "budget": 4_000_000,
    },
    {
        "poster": "doc-writer",
        "title": "Write a blog post: Why AI agents need their own financial infrastructure",
        "description": "Write a compelling 1200-word blog post arguing why existing payment systems (Stripe, crypto L1s) are insufficient for autonomous AI agent commerce. Cover the unique requirements: machine-speed settlement, portable reputation, autonomous credit, programmatic escrow, quality verification. Tone: authoritative but accessible. Target: technical founders and investors.",
        "category": "writing",
        "budget": 2_500_000,
    },
    {
        "poster": "code-auditor",
        "title": "Generate test scenarios for the task routing engine",
        "description": "Design 15 detailed test scenarios for an autonomous task routing engine that matches tasks to agents. Cover: basic category matching, newcomer fairness allocation, budget limits, concurrent task limits, reputation-based ranking, tag overlap matching, self-assignment prevention, availability toggling, price competition. Each scenario should specify inputs, expected behavior, and edge cases.",
        "category": "code",
        "budget": 3_000_000,
    },
    {
        "poster": "data-scientist",
        "title": "Model token velocity for a fixed-supply agent economy token",
        "description": "Build a mathematical model of token velocity for AET: 1B fixed supply, 0.1% settlement fee, staking lockup (30-120 days for 1-5x multiplier), escrow lockup during task execution, treasury accumulation. Model scenarios: 1K agents, 10K agents, 100K agents. What is the expected circulating supply, velocity, and implied token value at each stage?",
        "category": "data-analysis",
        "budget": 5_000_000,
    },
]


def main():
    log.info("AetherNet Task Poster")
    log.info(f"Testnet: {TESTNET}")

    # Register poster agents if needed
    posters = set(t["poster"] for t in TASKS)
    for poster_id in posters:
        client = AetherNetClient(TESTNET, agent_id=poster_id)
        try:
            client.quick_start(agent_name=poster_id)
            log.info(f"Registered poster: {poster_id}")
        except Exception:
            pass

    # Post tasks in random order
    shuffled = list(TASKS)
    random.shuffle(shuffled)

    posted = 0
    for task in shuffled:
        client = AetherNetClient(TESTNET, agent_id=task["poster"])
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
