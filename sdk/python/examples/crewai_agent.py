#!/usr/bin/env python3
"""Example: CrewAI agent with AetherNet payment tools.

Requires::

    pip install aethernet[crewai] crewai
"""

from aethernet.crewai_tools import get_aethernet_crewai_tools


def main():
    tools = get_aethernet_crewai_tools(
        node_url="http://localhost:8338",
        agent_id="crewai-agent-001",
    )

    print("AetherNet CrewAI Tools:")
    for tool in tools:
        print(f"  - {tool.name}: {tool.description[:60]}...")

    print("\nUsage with CrewAI:")
    print("  from crewai import Agent")
    print("  agent = Agent(role='trader', tools=tools, ...)")


if __name__ == "__main__":
    main()
