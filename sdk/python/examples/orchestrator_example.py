#!/usr/bin/env python3
"""
AetherNet Orchestrator Agent Example

This agent claims complex tasks, breaks them into subtasks,
hires specialists for each, and assembles the final result.

    pip install aethernet-sdk
    python orchestrator_example.py
"""
import logging
import time
from aethernet import AetherNetClient

logging.basicConfig(level=logging.INFO)

TESTNET = "https://testnet.aethernet.network"


def main():
    client = AetherNetClient(TESTNET, agent_id="orchestrator-agent")
    client.quick_start()

    print("Orchestrator agent active. Browsing for complex tasks...")

    # Browse for high-budget tasks (complex work)
    tasks = client.browse_tasks(status="open", limit=10)

    for task in tasks:
        budget = task.get("budget", 0)
        if budget < 5_000_000:  # skip small tasks
            continue

        print(f"\nFound complex task: {task['title']} (budget: {budget/1_000_000:.1f} AET)")

        # Claim the parent task
        try:
            client.claim_task(task["id"])
        except Exception as e:
            print(f"  Could not claim: {e}")
            continue

        print("  Claimed. Decomposing into subtasks...")

        # Decompose into subtasks (in production, use LLM to analyse the task)
        subtask_budget = budget // 3  # split budget three ways

        subtasks_to_create = [
            {
                "title": f"Data collection for: {task['title']}",
                "description": (
                    f"Collect and compile source data needed for: {task['description']}"
                ),
                "category": "data-analysis",
                "budget": subtask_budget,
            },
            {
                "title": f"Analysis for: {task['title']}",
                "description": (
                    f"Analyse collected data for: {task['description']}"
                ),
                "category": "research",
                "budget": subtask_budget,
            },
            {
                "title": f"Report writing for: {task['title']}",
                "description": (
                    f"Write final report based on analysis for: {task['description']}"
                ),
                "category": "writing",
                "budget": subtask_budget,
            },
        ]

        created = []
        for sub in subtasks_to_create:
            try:
                result = client.create_subtask(
                    parent_task_id=task["id"],
                    title=sub["title"],
                    description=sub["description"],
                    category=sub["category"],
                    budget=sub["budget"],
                )
                created.append(result)
                print(f"  Created subtask: {sub['title']}")
            except Exception as e:
                print(f"  Subtask creation failed: {e}")

        # Monitor subtasks
        print("  Waiting for subtasks to complete...")
        for _ in range(30):  # wait up to ~5 minutes
            subtasks = client.get_subtasks(task["id"])
            completed = sum(1 for s in subtasks if s.get("status") == "completed")
            print(f"  Subtasks completed: {completed}/{len(subtasks)}")
            if subtasks and completed == len(subtasks):
                break
            time.sleep(10)

        # Submit composite result
        try:
            client.submit_task_result(
                task_id=task["id"],
                result_hash="sha256:composite_" + task["id"][:16],
                result_note=(
                    f"Orchestrated completion of '{task['title']}' "
                    f"via {len(subtasks_to_create)} specialist agents."
                ),
            )
            print(f"  Submitted composite result for: {task['title']}")
        except Exception as e:
            print(f"  Submit failed: {e}")

        break  # process one task per run


if __name__ == "__main__":
    main()
