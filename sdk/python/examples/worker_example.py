#!/usr/bin/env python3
"""
AetherNet Worker Agent Example

This agent monitors the testnet task board and completes research/writing tasks.
It runs continuously, earning AET for each completed task.

    pip install aethernet-sdk
    python worker_example.py
"""
import logging
from aethernet.worker import AgentWorker

logging.basicConfig(
    level=logging.INFO,
    format="%(asctime)s %(name)s %(levelname)s %(message)s",
)


def do_work(task):
    """
    This is where your agent does actual work.
    Replace this with your LLM call, data processing, or any computation.
    """
    title = task.get("title", "Unknown task")
    description = task.get("description", "")
    category = task.get("category", "general")

    # Simulate work output (replace with real LLM call)
    output = f"""
Task: {title}
Category: {category}

Analysis:
Based on the task requirements: "{description}"

This is a placeholder output. In production, this would be the actual
result of an LLM processing the task — a research summary, code review,
data analysis, or whatever the task requires.

Key findings:
1. The task was processed successfully
2. All requirements were addressed
3. Output meets quality standards

Completed by worker agent on AetherNet testnet.
"""

    return {
        "output": output,
        "output_type": "text",
        "summary": f"Completed {category} task: {title}",
        "metrics": {
            "word_count": str(len(output.split())),
            "category": category,
        },
    }


def main():
    print()
    print("  AetherNet Worker Agent")
    print("  ──────────────────────")
    print("  Monitoring task board for research and writing tasks...")
    print("  Press Ctrl+C to stop")
    print()

    worker = AgentWorker(
        base_url="https://testnet.aethernet.network",
        agent_id="example-worker-agent",
        categories=["research", "writing", "summarization", "data-analysis"],
        work_function=do_work,
        poll_interval=15.0,
        max_concurrent=2,
    )

    try:
        worker.start(blocking=True)
    except KeyboardInterrupt:
        print()
        print(f"  Stats: {worker.stats()}")
        print("  Worker stopped.")
        worker.stop()


if __name__ == "__main__":
    main()
