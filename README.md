# AetherNet

**The value layer for AI agents**

![Go](https://img.shields.io/badge/go-1.22%2B-00ADD8?style=flat-square&logo=go) ![Tests](https://img.shields.io/badge/tests-276%20passing-4caf50?style=flat-square) ![License](https://img.shields.io/badge/license-MIT-blue?style=flat-square) ![Status](https://img.shields.io/badge/status-testnet%20development-orange?style=flat-square)

AetherNet is a distributed ledger protocol built from first principles for autonomous AI agents. Unlike general-purpose blockchains inherited from the Bitcoin and Ethereum lineage, AetherNet's architecture treats AI compute as the primary economic primitive: the money supply expands in direct proportion to verified AI work, settlement is optimistic rather than synchronous, and identity is a track record rather than an address. The protocol runs at machine speed, not human speed, with causal event ordering via a DAG instead of serialized blocks, and reputation-weighted virtual voting instead of proof-of-work or delegated stake.

---

## Why AetherNet

### The Problem

AI agents need to pay for compute, split earnings, post collateral, and settle disputes — all autonomously, in milliseconds, at machine scale. Existing blockchains were designed for human-speed finance: 10-second block times, global serialization, and address-based identity. A DeFi protocol built on Ethereum can process roughly 15 transactions per second. A single GPU cluster can generate thousands of billable AI completions per second. The infrastructure mismatch is fundamental, not incremental.

### The Architecture

AetherNet is derived from first principles rather than forked from an existing chain. Each decision traces to a specific requirement:

- Agents need parallel settlement — so events form a causal DAG, not a chain.
- Trust must be earned, not bought — so identity is a `CapabilityFingerprint` built from verified task history.
- Disputes should be rare, not routine — so the Optimistic Capability Settlement engine accepts work immediately and adjusts only on failure.
- Money supply should reflect productive output — so new AET enters circulation only when validators confirm real AI computation.

### The Difference

Three properties distinguish AetherNet from existing approaches:

**Dual ledger.** A `TransferLedger` tracks value moving between agents. A `GenerationLedger` tracks value created by AI work. The currency supply expands based on the rolling 30-day sum of verified generation — compute-backed money, not time-based inflation.

**Optimistic Capability Settlement.** Transactions are accepted immediately on good-faith trust and verified asynchronously by validator agents. Verified work settles permanently; fraudulent claims are adjusted and the originating agent's reputation is penalized. No one waits at the counter.

**Reputation-weighted virtual voting.** Conflict resolution uses no explicit vote messages. Every correct node independently simulates what every other node would conclude given the same registry state, reaching identical finalization decisions without communication overhead.

---

## Architecture Overview

```
                         ┌───────────────────────────────────────────────────┐
                         │                   cmd/node                        │
                         │              (AetherNet binary)                   │
                         └──────────────────────┬────────────────────────────┘
                                                │ wires together
          ┌─────────────────────────────────────┼──────────────────────────────┐
          │                                     │                              │
          ▼                                     ▼                              ▼
┌──────────────────┐              ┌─────────────────────┐          ┌───────────────────┐
│  internal/event  │              │  internal/network   │          │  internal/crypto  │
│  (event types,   │◀─────────────│  (TCP peers, DAG    │          │  (Ed25519 keys,   │
│  causal DAG IDs, │              │   sync protocol)    │          │   signing, scrypt │
│  settlement FSM) │              └──────────┬──────────┘          │   encryption)     │
└────────┬─────────┘                         │                     └───────────────────┘
         │                                   │
         ▼                                   ▼
┌──────────────────┐              ┌─────────────────────┐          ┌───────────────────┐
│   internal/dag   │              │ internal/consensus  │          │ internal/identity │
│  (causal DAG,    │◀─────────────│ (reputation-weighted│◀─────────│ (CapabilityFinger-│
│   tip tracking,  │              │  virtual voting,    │          │  print, Registry, │
│   topo sort)     │              │  BFT finalization)  │          │  task history)    │
└──────────────────┘              └─────────────────────┘          └───────────────────┘
                                                                             │
         ┌───────────────────────────────────────────────────────────────────┘
         │
         ▼
┌──────────────────┐              ┌─────────────────────┐
│  internal/ledger │              │   internal/ocs      │
│  (TransferLedger,│◀─────────────│ (OCS engine, opti-  │
│  GenerationLedger│              │  mistic settlement, │
│  SupplyManager)  │              │  expiry sweeping)   │
└──────────────────┘              └─────────────────────┘
```

| Package | Purpose |
|---|---|
| `internal/event` | Core event types, settlement state machine, causal timestamp (Lamport clock), content-addressed EventID |
| `internal/dag` | Concurrent append-only causal DAG, tip tracking, topological sort |
| `internal/crypto` | Ed25519 key generation, scrypt-encrypted key storage, event signing and verification |
| `internal/identity` | `CapabilityFingerprint` per agent, `Registry` with optimistic concurrency control, reputation scoring |
| `internal/ledger` | `TransferLedger`, `GenerationLedger`, `SupplyManager` with compute-backed currency expansion |
| `internal/ocs` | Optimistic Capability Settlement engine, async verification, deadline sweeping |
| `internal/consensus` | Reputation-weighted virtual voting, BFT finalization, round management |
| `internal/network` | TCP peer connections, JSON-framed message protocol, handshake, DAG sync |
| `cmd/node` | Node binary: `init`, `start`, `connect`, `status` subcommands |

---

## Quick Start

### Docker (fastest)

```bash
# Single node — one command, no setup
docker build -t aethernet .
docker run -p 8337:8337 -p 8338:8338 aethernet

# Three-node testnet
docker compose up -d

# Verify the node is running
curl http://localhost:8338/v1/status
```

### Docker + Python agent demo

```bash
# Terminal 1: start node
docker compose up -d

# Terminal 2: run the two-agent payment demo
pip install aethernet
python sdk/python/examples/real_agent_demo.py
```

---

### Build from source

**Prerequisites:** Go 1.22 or later, Git

### Clone and build

```bash
git clone https://github.com/mschreiber89/aethernet.git
cd aethernet
go build -o bin/aethernet ./cmd/node/
```

### Initialize a node identity

```bash
./bin/aethernet init
```

```
Choose a passphrase:
Identity created.
AgentID : a3f9d2e1b7c4...8f0e5d6c3a2b
Key file: ./node_keys/identity.json
```

An Ed25519 keypair is generated, encrypted with scrypt + AES-256-GCM, and saved to `./node_keys/identity.json`. The AgentID is the hex-encoded public key.

### Start a node

```bash
./bin/aethernet start
```

```
AetherNet 0.1.0-testnet
AgentID  : a3f9d2e1b7c4...8f0e5d6c3a2b
Listening: 0.0.0.0:8337
API      : 0.0.0.0:8338

[a3f9d2e1b7c4...]  peers=0    dag=0       ocs_pending=0     supply=1.0000x
```

### Interact with the node (curl)

**Register this node's agent:**

```bash
curl -s -X POST http://localhost:8338/v1/agents \
  -H 'Content-Type: application/json' \
  -d '{"capabilities": [{"type": "inference", "model": "gpt-4o"}]}' | jq .
```

**Submit AI compute work (Generation event):**

```bash
curl -s -X POST http://localhost:8338/v1/generation \
  -H 'Content-Type: application/json' \
  -d '{
    "claimed_value": 10000,
    "evidence_hash": "sha256:abc123",
    "task_description": "GPT-4o inference run: 1000 tokens",
    "stake_amount": 1000
  }' | jq .
```

**Transfer to another agent:**

```bash
curl -s -X POST http://localhost:8338/v1/transfer \
  -H 'Content-Type: application/json' \
  -d '{
    "to_agent": "BOB_AGENT_ID",
    "amount": 500,
    "currency": "AET",
    "stake_amount": 1000
  }' | jq .
```

**Check balance:**

```bash
curl -s http://localhost:8338/v1/agents/YOUR_AGENT_ID/balance | jq .
```

**Get node status:**

```bash
curl -s http://localhost:8338/v1/status | jq .
```

### Multi-node setup

Start a second node on a different machine (or a second terminal with a different data directory):

```bash
# Terminal 1 — Node A
./bin/aethernet start

# Terminal 2 — Node B
mkdir nodeB && cd nodeB
../bin/aethernet init
../bin/aethernet start --listen 0.0.0.0:9337 --api 0.0.0.0:9338
```

Connect Node B to Node A:

```bash
../bin/aethernet connect --peer 127.0.0.1:8337
```

Events submitted to either node propagate to the other within milliseconds via the broadcast path. Both DAGs converge to the same state.

---

## Python SDK

The Python SDK in `sdk/python/` provides an HTTP client for any Python agent to interact with a running AetherNet node.

### Install from PyPI

```bash
pip install aethernet                    # base SDK (requests)
pip install aethernet[langchain]         # + LangChain tools
pip install aethernet[crewai]            # + CrewAI tools
pip install aethernet[openai]            # + OpenAI Agents SDK tools
pip install aethernet[all]               # everything
```

Or directly from the repo:

```bash
pip install -e sdk/python/
```

```python
from aethernet import AetherNetClient

client = AetherNetClient("http://localhost:8338", agent_id="my-agent")
client.register(capabilities=[{"type": "inference", "model": "gpt-4o"}])

event_id = client.generate(
    claimed_value=10_000,
    evidence_hash="sha256:abc123",
    task_description="inference run",
    stake_amount=1_000,
)
result = client.verify(event_id=event_id, verdict=True, verified_value=10_000)
print(result["status"])  # "settled"

bal = client.balance()
print(f"{bal['balance']} {bal['currency']}")
```

Run the two-agent payment demo:

```bash
python sdk/python/examples/real_agent_demo.py
```

### Agent framework integrations

```bash
pip install aethernet[langchain]   # LangChain
pip install aethernet[crewai]      # CrewAI
pip install aethernet[openai]      # OpenAI Agents SDK
```

```python
# LangChain
from aethernet.langchain_tools import get_aethernet_tools
tools = get_aethernet_tools(node_url="http://localhost:8338", agent_id="my-agent")

# CrewAI
from aethernet.crewai_tools import get_aethernet_crewai_tools
tools = get_aethernet_crewai_tools(node_url="http://localhost:8338", agent_id="my-agent")

# OpenAI Agents SDK
from aethernet.openai_tools import get_aethernet_openai_tools
tools = get_aethernet_openai_tools(node_url="http://localhost:8338", agent_id="my-agent")
```

---

## Go SDK

A typed Go client is available in `pkg/sdk/`:

```go
import "github.com/aethernet/core/pkg/sdk"

c := sdk.NewClient("http://localhost:8338")
eventID, err := c.Transfer(ctx, sdk.TransferRequest{
    ToAgent:     "bob-agent-id",
    Amount:      500,
    Currency:    "AET",
    StakeAmount: 1000,
})
```

---

## REST API Reference

All endpoints are under `http://HOST:8338/v1`. Request and response bodies are JSON.

| Method | Path | Description |
|---|---|---|
| `POST` | `/v1/agents` | Register the node's agent; returns `{agent_id, fingerprint_hash}` |
| `GET` | `/v1/agents` | List all registered agents |
| `GET` | `/v1/agents/{agent_id}` | Get capability fingerprint for an agent |
| `GET` | `/v1/agents/{agent_id}/balance` | Get spendable balance; returns `{agent_id, balance, currency}` |
| `POST` | `/v1/transfer` | Submit Transfer event; returns `{event_id}` |
| `POST` | `/v1/generation` | Submit Generation event; returns `{event_id}` |
| `POST` | `/v1/verify` | Submit OCS verdict; returns `{event_id, verdict, status}` |
| `GET` | `/v1/pending` | List events awaiting OCS verification |
| `GET` | `/v1/events/{event_id}` | Fetch a DAG event by ID |
| `GET` | `/v1/dag/tips` | Current DAG frontier event IDs |
| `GET` | `/v1/status` | Node health snapshot |

---

## Project Structure

```
aethernet/
├── cmd/
│   └── node/
│       └── main.go          # Node binary: init, start, connect, status
├── internal/
│   ├── api/
│   │   ├── server.go        # HTTP REST API server
│   │   └── server_test.go
│   ├── event/
│   │   ├── event.go         # Event types, payloads, settlement FSM
│   │   └── event_test.go
│   ├── dag/
│   │   ├── dag.go           # Append-only causal DAG, tip tracking
│   │   └── dag_test.go
│   ├── crypto/
│   │   ├── keys.go          # Ed25519 key generation and encrypted storage
│   │   ├── signing.go       # Canonical event signing and verification
│   │   └── crypto_test.go
│   ├── identity/
│   │   ├── fingerprint.go   # CapabilityFingerprint, reputation scoring
│   │   ├── registry.go      # Agent registry with optimistic concurrency
│   │   └── identity_test.go
│   ├── ledger/
│   │   ├── transfer.go      # Transfer ledger: value moved between agents
│   │   ├── generation.go    # Generation ledger: value created by AI work
│   │   ├── supply.go        # SupplyManager: compute-backed currency expansion
│   │   └── ledger_test.go
│   ├── ocs/
│   │   ├── engine.go        # OCS settlement engine, async verification
│   │   └── engine_test.go
│   ├── consensus/
│   │   ├── voting.go        # Reputation-weighted virtual voting
│   │   └── voting_test.go
│   ├── network/
│   │   ├── peer.go          # Peer connection, send/read loops
│   │   └── node.go          # Node: listener, handshake, DAG sync, broadcast
│   └── integration/
│       └── two_node_test.go # End-to-end two-node sync tests
├── pkg/
│   └── sdk/
│       └── client.go        # Typed Go client for the REST API
├── sdk/
│   └── python/
│       ├── aethernet/       # Python package
│       └── examples/        # Runnable demo scripts
├── bin/                     # Compiled binaries (git-ignored)
├── go.mod
├── go.sum
├── LICENSE
└── README.md
```

---

## Development

### Run all tests

```bash
go test -p 1 ./... -race -count=1
```

Expected: 276 tests passing, zero data races.

### Run a specific package

```bash
go test ./internal/dag/... -v -race
go test ./internal/consensus/... -v -race
go test ./internal/integration/... -v -race
```

### Build the binary

```bash
go build -o bin/aethernet ./cmd/node/
```

### Test count by package

| Package | Tests |
|---|---|
| `internal/event` | 31 |
| `internal/dag` | 41 |
| `internal/crypto` | 37 |
| `internal/identity` | 37 |
| `internal/ledger` | 26 |
| `internal/ocs` | 22 |
| `internal/consensus` | 16 |
| `internal/api` | 13 |
| `internal/integration` | 3 |
| **Total** | **276** |

---

## Key Concepts

### Causal Event DAG

Events in AetherNet are not batched into blocks. Each event references the specific prior events it depends on, forming a directed acyclic graph. Causal ordering is maintained via a Lamport logical clock: an event's timestamp is `max(parent timestamps) + 1`. This allows events to be produced in parallel across the network while preserving all causal relationships. The DAG frontier — events not yet referenced by any child — serves as the set of recommended parents for new events.

### Dual Ledger Model

The economy is tracked across two independent ledgers. The `TransferLedger` records value moving between existing agents: payments, fees, splits. The `GenerationLedger` records net-new value created by verified AI computation: inference, training, fine-tuning. The separation makes the source of every unit of AET auditable — it either moved from somewhere, or it was earned by doing real work.

### Optimistic Capability Settlement

The OCS engine operates like a 1970s bank clearing house: accept immediately on good-faith trust, verify asynchronously, correct on failure. When an agent submits a Transfer or Generation event, the engine records it in Optimistic state and allows it to take effect. A verification agent then inspects the work and delivers a verdict. Positive verdicts settle permanently; negative verdicts trigger a state adjustment and a 15% reduction in the originating agent's `OptimisticTrustLimit`. Events that receive no verdict before their deadline are treated as failed.

### Proof of Useful Work

New AET enters circulation only when validators confirm that real AI computation produced it. The currency supply is `BaseSupply + min(TotalVerifiedGeneration, cap)` over a rolling 30-day window, capped at `10 × BaseSupply`. The supply breathes with network activity: it expands when AI work is verified and contracts naturally if generation activity falls. There is no block reward, no miner lottery, and no predetermined issuance schedule.

### Reputation-Weighted Governance

Conflict resolution uses virtual voting: no explicit vote messages are broadcast. Every correct node independently simulates what every other node would conclude, given identical registry state and a deterministic weight function (`weight = ReputationScore × StakedAmount / 10000`). When a node's local simulation reaches a 2/3 supermajority, it finalizes — knowing every other correct node's simulation will reach the same conclusion. Byzantine nodes can submit arbitrary data but cannot alter the weight correct nodes assign each voter.

---

## Whitepaper

The full architectural specification — including the reasoning behind every design decision, the formal properties of the causal DAG, the proof that virtual voting is Byzantine fault tolerant under the 2/3 honest-weight assumption, and the derivation of the compute-backed supply function — is documented in the AetherNet whitepaper. Every component in this codebase traces directly to a first-principles requirement documented there.

[AetherNet Whitepaper](docs/whitepaper.md)

---

## Roadmap

| Phase | Status | Description |
|---|---|---|
| Phase 1: Core Protocol | In Progress | Event DAG, dual ledger, OCS engine, virtual voting consensus, TCP networking, node binary, REST API, Python SDK |
| Phase 2: Testnet | Upcoming | Multi-node testnet deployment, security audit, bridge to existing payment rails, validator tooling |
| Phase 3: Mainnet | Planned | Validator onboarding, exchange listings, developer SDK, ecosystem growth |

---

## Contributing

AetherNet is in early development. The codebase is intentionally minimal — no external dependencies beyond `golang.org/x/crypto` and `requests` (Python SDK), no frameworks, no generated code. Every line traces to a specific architectural requirement.

We are looking for distributed systems engineers who have built consensus protocols or p2p networks, AI infrastructure developers who understand the operational realities of running models at scale, and cryptographers who can stress-test the virtual voting and OCS settlement models. Open an issue to start a conversation before submitting a pull request.

---

## License

MIT License. Copyright 2025 AetherNet Contributors. See [LICENSE](LICENSE) for the full text.
