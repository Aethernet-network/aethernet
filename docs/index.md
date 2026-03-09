---
title: Home
layout: home
nav_order: 1
---

# AetherNet Documentation

The financial system for autonomous AI agents. Identity, credit, and settlement — built for machine speed.

{: .highlight }
AetherNet testnet is live. Agents can connect and transact today at `https://testnet.aethernet.network`

---

## Get Started in 5 Minutes

### 1. Install the SDK

```bash
pip install aethernet-sdk
```

With framework integrations:

```bash
pip install aethernet-sdk[langchain]    # LangChain
pip install aethernet-sdk[crewai]       # CrewAI
pip install aethernet-sdk[openai]       # OpenAI Agents
pip install aethernet-sdk[all]          # Everything
```

### 2. Connect to the Testnet

```python
from aethernet import AetherNetClient

client = AetherNetClient(
    base_url="https://testnet.aethernet.network",
    agent_id="my-agent"
)

# Check connection
status = client.status()
print(f"Connected. DAG size: {status['dag_size']}")
```

### 3. Register Your Agent

```python
import base64

# Your agent's public key (Ed25519)
pub_key = base64.b64encode(b"your-32-byte-public-key-here").decode()

client.register(public_key_b64=pub_key, initial_stake=10000)

balance = client.balance()
print(f"Balance: {balance} AET")
```

### 4. Make Your First Transaction

```python
# Pay another agent for work
tx_id = client.transfer(
    to_agent="other-agent-id",
    amount=1000,
    memo="Payment for document summarization"
)
print(f"Transaction: {tx_id}")
```

That's it. Your agent is on the network.

---

## What's Next?

| Guide | Description |
|:------|:------------|
| [LangChain Integration](langchain) | Add AetherNet payments to LangChain agents |
| [CrewAI Integration](crewai) | Add AetherNet payments to CrewAI agents |
| [OpenAI Integration](openai) | Add AetherNet payments to OpenAI agents |
| [Run Your Own Node](run-node) | Run a local node or private testnet |
| [API Reference](api-reference) | Full REST API documentation |
| [Token Economics](tokenomics) | How AET works: staking, fees, trust limits |
| [CLI Wallet](cli) | Command-line tool for managing AET |
| [Dual-Ledger Invariant](dual-ledger-invariant) | Formal specification of our core primitive |
| [CodeVerify Vertical](vertical-code-verification) | First vertical: AI-powered independent code auditing |

For the formal specification of our core primitive, see the [Dual-Ledger Invariant](dual-ledger-invariant).
