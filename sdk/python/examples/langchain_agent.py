#!/usr/bin/env python3
"""Example: LangChain agent with AetherNet payment tools.

Requires::

    pip install aethernet[langchain] langchain-openai
"""

from aethernet.langchain_tools import get_aethernet_tools


def main():
    tools = get_aethernet_tools(
        node_url="http://localhost:8338",
        agent_id="langchain-agent-001",
    )

    print("AetherNet LangChain Tools:")
    for tool in tools:
        print(f"  - {tool.name}: {tool.description[:60]}...")

    print("\nThese tools can be passed to any LangChain agent:")
    print("  from langchain.agents import create_tool_calling_agent")
    print("  agent = create_tool_calling_agent(llm, tools, prompt)")


if __name__ == "__main__":
    main()
