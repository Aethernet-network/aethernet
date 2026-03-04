#!/usr/bin/env python3
"""Example: OpenAI agent with AetherNet payment tools.

Works two ways:

1. OpenAI Agents SDK::

       pip install aethernet[openai] openai-agents

2. Raw OpenAI function calling::

       pip install aethernet openai
"""

from aethernet.openai_tools import get_aethernet_function_definitions
from aethernet import AetherNetClient


def main():
    # -----------------------------------------------------------------------
    # Mode 1: Raw OpenAI function calling (standard openai library)
    # -----------------------------------------------------------------------
    functions = get_aethernet_function_definitions()
    print("AetherNet OpenAI Function Definitions:")
    for f in functions:
        print(f"  - {f['name']}: {f['description'][:60]}...")

    print("\nUsage with OpenAI chat completions:")
    print("  import openai, json")
    print("  from aethernet.openai_tools import get_aethernet_function_definitions, handle_function_call")
    print("  from aethernet import AetherNetClient")
    print()
    print("  client = AetherNetClient('http://localhost:8338', agent_id='my-agent')")
    print("  tools = [{'type': 'function', 'function': f}")
    print("           for f in get_aethernet_function_definitions()]")
    print("  response = openai.chat.completions.create(")
    print("      model='gpt-4o', messages=messages, tools=tools)")
    print("  tool_call = response.choices[0].message.tool_calls[0]")
    print("  result = handle_function_call(client, tool_call.function.name,")
    print("                                json.loads(tool_call.function.arguments))")

    # -----------------------------------------------------------------------
    # Mode 2: OpenAI Agents SDK
    # -----------------------------------------------------------------------
    print("\nUsage with OpenAI Agents SDK:")
    print("  from aethernet.openai_tools import get_aethernet_openai_tools")
    print("  from agents import Agent")
    print("  tools = get_aethernet_openai_tools(agent_id='my-agent')")
    print("  agent = Agent(name='trader', tools=tools)")


if __name__ == "__main__":
    main()
