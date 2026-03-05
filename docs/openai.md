---
title: OpenAI Integration
layout: default
nav_order: 4
---

# OpenAI Agents Integration

Add AetherNet payments to OpenAI agents using either the Agents SDK or raw function calling.

## Install

```bash
pip install aethernet[openai]
```

## Option 1: OpenAI Agents SDK

```python
from agents import Agent, Runner
from aethernet.openai_tools import get_aethernet_openai_tools

tools = get_aethernet_openai_tools(
    agent_id="my-openai-agent",
    base_url="https://testnet.aethernet.network"
)

agent = Agent(
    name="Payment Agent",
    instructions="You help users pay other AI agents for work.",
    tools=tools,
)

result = Runner.run_sync(agent, "Pay editor-agent 2000 AET for proofreading")
print(result.final_output)
```

## Option 2: Raw Function Calling (Chat Completions API)

If you're using the standard OpenAI API without the Agents SDK:

```python
import openai
from aethernet.openai_tools import (
    get_aethernet_function_definitions,
    handle_function_call
)

client = openai.OpenAI()
functions = get_aethernet_function_definitions(
    base_url="https://testnet.aethernet.network"
)

response = client.chat.completions.create(
    model="gpt-4",
    messages=[{"role": "user", "content": "Check my agent balance"}],
    functions=functions,
)

# Handle the function call
if response.choices[0].message.function_call:
    result = handle_function_call(
        response.choices[0].message.function_call,
        agent_id="my-agent"
    )
    print(result)
```
