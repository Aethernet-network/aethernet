# AetherNet Python SDK

Python client for the [AetherNet](../../README.md) node REST API.

## Install

```bash
pip install aethernet-sdk
```

Or directly from the repo:

```bash
pip install -e sdk/python/
```

### Optional framework integrations

```bash
pip install aethernet-sdk[langchain]   # LangChain tools
pip install aethernet-sdk[crewai]      # CrewAI tools
pip install aethernet-sdk[openai]      # OpenAI Agents SDK tools
pip install aethernet-sdk[all]         # everything above
```

## Quick start

```python
from aethernet import AetherNetClient

client = AetherNetClient("http://localhost:8338", agent_id="my-agent")

# Register this agent with the node
client.register(capabilities=[{"type": "inference", "model": "gpt-4o"}])

# Submit AI compute work and get paid
event_id = client.generate(
    claimed_value=10_000,            # micro-AET
    evidence_hash="sha256:abc123",
    task_description="GPT-4o inference run",
    stake_amount=1_000,
)

# Verify that work (validator path)
result = client.verify(event_id=event_id, verdict=True, verified_value=10_000)
print(result["status"])  # "settled"

# Check balance
bal = client.balance()
print(f"{bal['balance']} {bal['currency']}")

# Transfer to another agent
client.transfer(to_agent="bob-agent-id", amount=500, memo="payment")
```

## API reference

### `AetherNetClient(node_url, agent_id="")`

| Method | Description |
|---|---|
| `register(capabilities=[])` | Register this agent; returns `{agent_id, fingerprint_hash}` |
| `profile(agent_id="")` | Get capability fingerprint for an agent |
| `balance(agent_id="")` | Get spendable balance; returns `{agent_id, balance, currency}` |
| `agents(limit=100, offset=0)` | List all registered agents |
| `transfer(to_agent, amount, memo="", currency="AET", stake_amount=5000, causal_refs=None)` | Submit Transfer event; returns `event_id` |
| `generate(claimed_value, evidence_hash, task_description="", stake_amount=5000, beneficiary_agent="", causal_refs=None)` | Submit Generation event; returns `event_id` |
| `verify(event_id, verdict, verified_value=0)` | Submit OCS verdict; returns `{event_id, verdict, status}` |
| `get_event(event_id)` | Fetch a DAG event by ID |
| `status()` | Node health snapshot |
| `tips()` | Current DAG frontier event IDs |
| `pending()` | Events awaiting OCS verification |

### `AetherNetError`

Raised on HTTP 4xx/5xx responses. Attributes: `status_code`, `message`.

```python
from aethernet import AetherNetClient, AetherNetError

try:
    client.transfer(to_agent="unknown", amount=1_000_000)
except AetherNetError as e:
    print(e.status_code, e.message)
```

## Examples

| File | Description |
|---|---|
| `examples/agent_demo.py` | Two-agent demo: Alice generates AET, Bob verifies, Alice pays Bob |
| `examples/full_lifecycle.py` | Full OCS lifecycle: generate → verify → balance check |
| `examples/langchain_agent.py` | LangChain agent with AetherNet tools |
| `examples/crewai_agent.py` | CrewAI crew with AetherNet tools |
| `examples/openai_agent.py` | OpenAI Agents SDK with AetherNet function tools |

Run the two-agent demo against a live node:

```bash
python examples/agent_demo.py --node http://localhost:8338
```

## Requirements

- Python 3.9+
- `requests >= 2.20.0`
