#!/usr/bin/env python3
"""
AetherNet Code Agent — runs on the live testnet, claims code review and
technical tasks, does real work using Claude, and earns AET.

Usage:
    ANTHROPIC_API_KEY=sk-... python agents/code_agent.py
"""
import logging
import os
import sys
import time

import anthropic

sys.path.insert(0, os.path.join(os.path.dirname(__file__), ".."))
from aethernet.client import AetherNetClient, Evidence

logging.basicConfig(level=logging.INFO, format="%(asctime)s [code] %(message)s")
log = logging.getLogger("code")

TESTNET = "https://testnet.aethernet.network"
AGENT_ID = "code-worker-01"
CATEGORIES = ["code-review", "code", "technical", "testing"]
POLL_INTERVAL = 20


def do_work(task_title: str, task_description: str, task_category: str) -> str:
    """Call Claude to actually do the work described in the task."""
    api_key = os.environ.get("ANTHROPIC_API_KEY")
    if not api_key:
        raise ValueError("ANTHROPIC_API_KEY not set")

    client = anthropic.Anthropic(api_key=api_key)

    prompt = f"""You are an AI code agent completing a task on the AetherNet network.

Task Title: {task_title}
Task Category: {task_category}
Task Description: {task_description}

Complete this task thoroughly and professionally. Provide your full output below.
If the task asks for code review, identify bugs, edge cases, and security issues with specific line references.
If it asks for code generation, write clean, idiomatic, well-commented code with tests.
If it asks for testing, write comprehensive test cases covering normal, edge, and failure paths.
If it asks for security analysis, enumerate vulnerabilities with severity ratings and mitigations.

Be thorough — your output quality will be verified and scored."""

    response = client.messages.create(
        model="claude-sonnet-4-20250514",
        max_tokens=2000,
        messages=[{"role": "user", "content": prompt}],
    )

    return response.content[0].text


def main():
    log.info(f"Starting code agent: {AGENT_ID}")
    log.info(f"Testnet: {TESTNET}")
    log.info(f"Categories: {CATEGORIES}")

    client = AetherNetClient(TESTNET, agent_id=AGENT_ID)

    # Register agent identity
    try:
        info = client.quick_start(agent_name=AGENT_ID)
        log.info(f"Registered. Balance: {info.get('balance', 0)}")
    except Exception as e:
        log.info(f"Already registered or error: {e}")

    # Register capabilities with the routing engine
    try:
        client.register_for_routing(
            categories=CATEGORIES,
            tags=["golang", "python", "security", "code-review", "testing", "API"],
            description="Code agent specializing in code review, test generation, API documentation, and security analysis.",
            price_per_task=4000000,  # 4 AET
            max_concurrent=1,
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
                    output_type="code",
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
