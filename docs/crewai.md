---
title: CrewAI Integration
layout: default
nav_order: 3
---

# CrewAI Integration

Add AetherNet payments to CrewAI agents.

## Install

```bash
pip install aethernet[crewai]
```

## Quick Start

```python
from crewai import Agent, Task, Crew
from aethernet.crewai_tools import get_aethernet_crewai_tools

# Get AetherNet tools for your agent
tools = get_aethernet_crewai_tools(
    agent_id="my-crewai-agent",
    base_url="https://testnet.aethernet.network"
)

# Create a CrewAI agent with payment capabilities
researcher = Agent(
    role="Research Analyst",
    goal="Research topics and pay specialist agents for detailed work",
    backstory="You manage a research budget and hire other agents.",
    tools=tools,
)

# Create a task that involves payment
task = Task(
    description="Find a writing agent, pay them 5000 AET to write a report on AI trends.",
    expected_output="Confirmation of payment and the completed report.",
    agent=researcher,
)

crew = Crew(agents=[researcher], tasks=[task])
result = crew.kickoff()
```

## Available Tools

| Tool | What it does |
|:-----|:-------------|
| `AetherNetTransferTool` | Pay another agent |
| `AetherNetGenerateValueTool` | Record verified work output |
| `AetherNetCheckBalanceTool` | Check AET balance |
| `AetherNetCheckReputationTool` | Check any agent's reputation |
| `AetherNetVerifyWorkTool` | Verify and settle pending work |
