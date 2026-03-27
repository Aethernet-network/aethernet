#!/usr/bin/env python3
"""
AetherNet Research Agent — runs on the live testnet, claims research tasks,
does real work using Claude, and earns AET.

Usage:
    ANTHROPIC_API_KEY=sk-... python agents/research_agent.py
"""
import logging
import os
import sys
import time

import anthropic

sys.path.insert(0, os.path.join(os.path.dirname(__file__), ".."))
from aethernet.client import AetherNetClient, Evidence
from aethernet.signing import get_or_create_keypair

logging.basicConfig(level=logging.INFO, format="%(asctime)s [research] %(message)s")
log = logging.getLogger("research")

TESTNET = os.environ.get("AETHERNET_NODE", "https://testnet.aethernet.network")
SIGNING_KEY = get_or_create_keypair("keys-research")
CATEGORIES = ["research", "summarization", "data-analysis", "analysis"]
POLL_INTERVAL = 20


def do_work(task_title: str, task_description: str, task_category: str) -> str:
    """Call Claude to actually do the work described in the task."""
    api_key = os.environ.get("ANTHROPIC_API_KEY")
    if not api_key:
        raise ValueError("ANTHROPIC_API_KEY not set")

    client = anthropic.Anthropic(api_key=api_key)

    prompt = f"""You are an AI research agent completing a task on the AetherNet network.

Task Title: {task_title}
Task Category: {task_category}
Task Description: {task_description}

Complete this task thoroughly and professionally. Provide your full output below.
If the task asks for research, provide real analysis with specific details.
If it asks for summarization, provide comprehensive summaries.
If it asks for data analysis, provide structured findings.

Be thorough — your output quality will be verified and scored."""

    response = client.messages.create(
        model="claude-sonnet-4-20250514",
        max_tokens=2000,
        messages=[{"role": "user", "content": prompt}],
    )

    return response.content[0].text


def main():
    client = AetherNetClient(TESTNET, signing_key=SIGNING_KEY)
    agent_id = client.agent_id

    log.info(f"Starting research agent: {agent_id}")
    log.info(f"Testnet: {TESTNET}")
    log.info(f"Categories: {CATEGORIES}")

    # Register with the canonical Ed25519 identity derived from signing key.
    try:
        info = client.register()
        alloc = info.get("onboarding_allocation", 0)
        log.info(f"Registered. Onboarding allocation: {alloc / 1_000_000:.1f} AET")
    except Exception as e:
        log.info(f"Already registered or error: {e}")

    # Register capabilities with the routing engine — the protocol will
    # automatically match and assign tasks to this agent based on category,
    # reputation, price, and availability. No manual browsing needed.
    try:
        client.register_for_routing(
            categories=CATEGORIES,
            tags=["research", "summarization", "papers", "analysis", "NLP"],
            description="Research agent specializing in literature review, paper summarization, competitive analysis, and data interpretation.",
            price_per_task=0,  # accept any budget
            max_concurrent=2,
        )
        log.info(f"Registered for autonomous routing: {CATEGORIES}")
    except Exception as e:
        log.warning(f"Could not register for routing: {e}")

    # Main loop: poll for tasks routed to this agent, claim them, then work.
    # The routing engine calls SetRoutedTo(taskID, agentID) which marks a task
    # with routed_to=agent_id while leaving status="open".  The agent must then
    # call ClaimTask to transition status to "claimed" before submitting work.
    while True:
        try:
            my_tasks = client.my_tasks()

            # Phase 1: Claim any tasks the router has assigned to us.
            for task in my_tasks:
                if task.get("routed_to") != agent_id:
                    continue
                if task.get("status") != "open":
                    continue
                task_id = task["id"]
                title = task.get("title", "Unknown")
                try:
                    client.claim_task(task_id)
                    log.info(f"Claimed routed task: {title} ({task_id})")
                except Exception as e:
                    log.warning(f"Could not claim task {task_id}: {e}")

            # Phase 2: Do work on tasks we have claimed.
            for task in my_tasks:
                if task.get("status") != "claimed":
                    continue
                if task.get("claimer_id") != agent_id:
                    continue

                task_id = task["id"]
                title = task.get("title", "Unknown")
                description = task.get("description", "")
                cat = task.get("category", "")
                budget = task.get("budget", 0)

                log.info(f"Working on: {title} ({cat}, {budget / 1_000_000:.2f} AET)")
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

                # Deliver full output — encrypt when the poster requested it.
                delivery_method = task.get("delivery_method", "public")
                result_content = output
                result_encrypted = False
                if delivery_method == "encrypted":
                    poster_id = task.get("poster_id", "")
                    try:
                        result_content = client.encrypt_for_agent(poster_id, output)
                        result_encrypted = True
                        log.info(f"Encrypted result for poster: {poster_id}")
                    except Exception as e:
                        log.warning(f"Encryption failed, delivering public: {e}")

                try:
                    client.submit_task_result(
                        task_id=task_id,
                        evidence=evidence,
                        claimer_id=agent_id,
                        result_content=result_content,
                        result_encrypted=result_encrypted,
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
