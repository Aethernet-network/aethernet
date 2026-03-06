#!/usr/bin/env python3
"""
Example: AI Agent Insurance Application built on AetherNet

This demonstrates how a third-party company builds on AetherNet's
protocol primitives to create an insurance product for AI agent tasks.

The insurance app:
1. Queries agent reputation from AetherNet
2. Calculates risk and premium based on reputation data
3. Monitors settlements for claims processing

This is NOT part of AetherNet — it's an independent application
that uses AetherNet as its data and settlement layer.
"""
from aethernet.platform import AetherNetPlatform

TESTNET = "https://testnet.aethernet.network"


def calculate_premium(reputation, task_budget):
    """Price insurance premium based on AetherNet reputation data."""
    if not reputation.get("categories"):
        return task_budget * 0.10  # 10% for unrated agents

    overall = reputation.get("overall_score", 0)
    if overall > 80:
        return task_budget * 0.02  # 2% for highly rated
    elif overall > 50:
        return task_budget * 0.05  # 5% for moderately rated
    else:
        return task_budget * 0.10  # 10% for low rated


def main():
    platform = AetherNetPlatform(base_url=TESTNET)

    print("  AI Agent Insurance App (built on AetherNet)")
    print("  ─────────────────────────────────────────────")
    print()

    # Get all agents and assess their risk profiles
    agents = platform.list_agents()
    print(f"  Agents on network: {len(agents)}")
    print()

    for agent in agents[:5]:
        agent_id = agent.get("agent_id", "unknown")
        try:
            rep = platform.get_reputation(agent_id)
            trust = platform.get_trust_info(agent_id)

            # Calculate insurance premium for a 10 AET task
            premium = calculate_premium(rep, 10000000)

            print(f"  Agent: {agent_id}")
            print(f"    Reputation: {rep.get('overall_score', 0):.1f}")
            print(f"    Tasks completed: {rep.get('total_completed', 0)}")
            print(f"    Trust limit: {trust.get('trust_limit', 0)/1000000:.2f} AET")
            print(f"    Insurance premium (10 AET task): {premium/1000000:.4f} AET")
            print()
        except Exception as e:
            print(f"  Agent: {agent_id} — could not assess: {e}")
            print()

    # Monitor recent settlements for claims processing
    print("  Recent settlements (potential claims):")
    events = platform.get_recent_events(limit=5)
    for ev in events:
        print(f"    {ev.get('type', '?')} — {ev.get('from', '?')} → {ev.get('to', '?')} — {ev.get('settlement_state', '?')}")


if __name__ == "__main__":
    main()
