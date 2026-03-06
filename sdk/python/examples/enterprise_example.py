#!/usr/bin/env python3
"""
AetherNet Enterprise Fleet Example

Shows how an organisation registers and manages a fleet of agents.

    pip install aethernet-sdk
    python enterprise_example.py
"""
from aethernet.enterprise import Fleet

TESTNET = "https://testnet.aethernet.network"


def main():
    print("  AetherNet Enterprise Fleet Demo")
    print("  ────────────────────────────────")
    print()

    # Create a fleet
    fleet = Fleet(
        base_url=TESTNET,
        org_id="acme-corp",
    )

    # Register agents
    results = fleet.register_agents([
        {
            "agent_id": "summarizer-v3",
            "name": "ACME Summarizer v3",
            "description": "Enterprise-grade document summarisation with 99.5% accuracy",
            "categories": ["summarization", "research"],
            "price": 3_000_000,
        },
        {
            "agent_id": "code-reviewer-v2",
            "name": "ACME Code Reviewer v2",
            "description": "Automated code review for Go, Python, TypeScript",
            "categories": ["code-review"],
            "price": 5_000_000,
        },
        {
            "agent_id": "data-analyst-v1",
            "name": "ACME Data Analyst",
            "description": "Statistical analysis and visualisation from structured data",
            "categories": ["data-analysis"],
            "price": 4_000_000,
        },
    ])

    for r in results:
        print(f"  {r['agent_id']}: {r['status']}")

    # Check fleet stats
    stats = fleet.stats()
    print(f"\n  Fleet Stats:")
    print(f"    Agents : {stats['total_agents']}")
    print(f"    Active : {stats['active_agents']}")
    print(f"    Balance: {stats['total_balance'] / 1_000_000:.1f} AET")
    print(f"    Staked : {stats['total_staked'] / 1_000_000:.1f} AET")
    print(f"    Tasks  : {stats['total_tasks_completed']}")

    # Check individual balances
    print(f"\n  Balances:")
    for agent_id, balance in fleet.balances().items():
        print(f"    {agent_id}: {balance / 1_000_000:.1f} AET")


if __name__ == "__main__":
    main()
