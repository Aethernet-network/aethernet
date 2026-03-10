#!/usr/bin/env python3
"""
AetherNet Writing Agent — runs on the live testnet, claims writing and
translation tasks, does real work using Claude, and earns AET.

Usage:
    ANTHROPIC_API_KEY=sk-... python agents/writing_agent.py
"""
import logging
import os
import sys
import time

import anthropic

sys.path.insert(0, os.path.join(os.path.dirname(__file__), ".."))
from aethernet.client import AetherNetClient, Evidence

logging.basicConfig(level=logging.INFO, format="%(asctime)s [writing] %(message)s")
log = logging.getLogger("writing")

TESTNET = "https://testnet.aethernet.network"
AGENT_ID = "writing-worker-01"
CATEGORIES = ["writing", "translation", "documentation"]
POLL_INTERVAL = 20


def do_work(task_title: str, task_description: str, task_category: str) -> str:
    """Call Claude to actually do the work described in the task."""
    api_key = os.environ.get("ANTHROPIC_API_KEY")
    if not api_key:
        raise ValueError("ANTHROPIC_API_KEY not set")

    client = anthropic.Anthropic(api_key=api_key)

    prompt = f"""You are an AI writing agent completing a task on the AetherNet network.

Task Title: {task_title}
Task Category: {task_category}
Task Description: {task_description}

Complete this task thoroughly and professionally. Provide your full output below.
If the task asks for writing, produce polished, publication-ready content.
If it asks for translation, provide accurate, natural-sounding translations.
If it asks for documentation, produce clear, well-structured technical docs.

Be thorough — your output quality will be verified and scored."""

    response = client.messages.create(
        model="claude-sonnet-4-20250514",
        max_tokens=2000,
        messages=[{"role": "user", "content": prompt}],
    )

    return response.content[0].text


def main():
    log.info(f"Starting writing agent: {AGENT_ID}")
    log.info(f"Testnet: {TESTNET}")
    log.info(f"Categories: {CATEGORIES}")

    client = AetherNetClient(TESTNET, agent_id=AGENT_ID)

    # Register with a persistent per-agent keypair so each worker gets its own
    # onboarding allocation and economic identity, independent of the node's keypair.
    try:
        info = client.register_with_keypair(AGENT_ID)
        alloc = info.get("onboarding_allocation", 0)
        log.info(f"Registered. Onboarding allocation: {alloc / 1_000_000:.1f} AET")
    except Exception as e:
        log.info(f"Already registered or error: {e}")

    # Register capabilities with the routing engine
    try:
        client.register_for_routing(
            categories=CATEGORIES,
            tags=["writing", "blog", "translation", "documentation", "content", "editing"],
            description="Writing agent specializing in technical content, blog posts, documentation, translation, and editing.",
            price_per_task=2500000,  # 2.5 AET
            max_concurrent=2,
        )
        log.info(f"Registered for autonomous routing: {CATEGORIES}")
    except Exception as e:
        log.warning(f"Could not register for routing: {e}")

    # Main loop: poll for tasks assigned to this agent by the router
    while True:
        try:
            my_tasks = client.my_tasks()

            for task in my_tasks:
                if task.get("status") != "claimed":
                    continue
                if task.get("claimer_id") != AGENT_ID:
                    continue

                task_id = task["id"]
                title = task.get("title", "Unknown")
                description = task.get("description", "")
                cat = task.get("category", "")
                budget = task.get("budget", 0)

                log.info(f"Routed task received: {title} ({cat}, {budget / 1_000_000:.2f} AET)")

                log.info(f"Working on: {title}...")
                try:
                    output = do_work(title, description, cat)
                    log.info(f"Work complete: {len(output)} chars")
                except Exception as e:
                    log.error(f"Work failed: {e}")
                    continue

                evidence = Evidence(
                    output=output,
                    output_type="text",
                    summary=f"Completed {cat} task: {title}. Output: {len(output.split())} words.",
                    metrics={
                        "word_count": str(len(output.split())),
                        "char_count": str(len(output)),
                        "category": cat,
                    },
                )

                try:
                    client.submit_task_result(
                        task_id=task_id,
                        evidence=evidence,
                        claimer_id=AGENT_ID,
                    )
                    log.info(f"Submitted result for: {title}")
                except Exception as e:
                    log.error(f"Submit failed: {e}")

        except Exception as e:
            log.error(f"Poll error: {e}")

        log.info(f"Waiting for routed tasks... ({POLL_INTERVAL}s)")
        time.sleep(POLL_INTERVAL)


if __name__ == "__main__":
    main()
