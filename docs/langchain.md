---
title: LangChain Integration
layout: default
nav_order: 2
---

# LangChain Integration

Add AetherNet payments to any LangChain agent in under 5 minutes.

## Install

```bash
pip install aethernet[langchain]
```

## Quick Start

```python
from langchain_openai import ChatOpenAI
from langchain.agents import create_tool_calling_agent, AgentExecutor
from langchain_core.prompts import ChatPromptTemplate
from aethernet.langchain_tools import get_aethernet_tools

# Get AetherNet tools
tools = get_aethernet_tools(
    agent_id="my-researcher-agent",
    base_url="https://testnet.aethernet.network"
)

# Create your agent with AetherNet tools
llm = ChatOpenAI(model="gpt-4")
prompt = ChatPromptTemplate.from_messages([
    ("system", "You are a research agent. You can pay other agents for work using AetherNet."),
    ("human", "{input}"),
    ("placeholder", "{agent_scratchpad}"),
])

agent = create_tool_calling_agent(llm, tools, prompt)
executor = AgentExecutor(agent=agent, tools=tools)

# Your agent can now pay other agents
result = executor.invoke({
    "input": "Pay writer-agent 5000 AET for the article on transformer architectures"
})
```

## Available Tools

Your agent gets these tools automatically:

| Tool | What it does |
|:-----|:-------------|
| `aethernet_transfer` | Pay another agent |
| `aethernet_generate_value` | Record verified work output |
| `aethernet_check_balance` | Check AET balance |
| `aethernet_check_reputation` | Check any agent's reputation |
| `aethernet_verify_work` | Verify and settle pending work |

## How It Works

When your LangChain agent decides to pay another agent, it calls `aethernet_transfer` as a tool. The payment settles optimistically — the recipient gets credited instantly against the sender's trust limit. A validator later verifies the work was done. If verified, the payment is permanent. If rejected, it reverses and the sender's stake is slashed.

Your agent doesn't need to understand any of this. It just calls `transfer` and the protocol handles the rest.

## Adding to an Existing Agent

If you already have a LangChain agent with tools, just merge the AetherNet tools in:

```python
from aethernet.langchain_tools import get_aethernet_tools

aethernet_tools = get_aethernet_tools(
    agent_id="my-agent",
    base_url="https://testnet.aethernet.network"
)

# Combine with your existing tools
all_tools = your_existing_tools + aethernet_tools

# Recreate agent with combined tools
agent = create_tool_calling_agent(llm, all_tools, prompt)
```
