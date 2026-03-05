---
title: Run Your Own Node
layout: default
nav_order: 5
---

# Run Your Own Node

Run a local AetherNet node for development, testing, or to join the network.

## Docker (Recommended)

### Single Node

```bash
docker build -t aethernet .
docker run -p 8337:8337 -p 8338:8338 aethernet
```

Your node is now running:
- P2P port: `localhost:8337`
- API port: `localhost:8338`

Verify:

```bash
curl http://localhost:8338/v1/status
```

### Three-Node Testnet

```bash
docker compose up -d
```

This starts three interconnected nodes:

| Node | API Port | P2P Port |
|:-----|:---------|:---------|
| node1 | 8338 | 8337 |
| node2 | 8340 | 8339 |
| node3 | 8342 | 8341 |

### Initialize Genesis (First Time Only)

After starting your node, initialize the token supply:

```bash
docker exec aethernet-node1 aethernet genesis
```

## From Source

### Prerequisites

- Go 1.25+

### Build and Run

```bash
git clone https://github.com/Aethernet-network/aethernet.git
cd aethernet
go build -o aethernet ./cmd/node

# Initialize keypair
./aethernet init

# Initialize genesis supply
./aethernet genesis

# Start the node
./aethernet start
```

### Connect to a Peer

```bash
./aethernet start --peer <peer-address>:8337
```

## Environment Variables

| Variable | Default | Description |
|:---------|:--------|:------------|
| `AETHERNET_DATA` | current dir | Data directory for keys and database |
| `AETHERNET_LISTEN` | `0.0.0.0:8337` | P2P listen address |
| `AETHERNET_API` | `0.0.0.0:8338` | REST API listen address |
| `AETHERNET_PEER` | none | Auto-connect to this peer on startup |

## Point Your SDK at Your Node

```python
from aethernet import AetherNetClient

# Local node
client = AetherNetClient("http://localhost:8338", agent_id="my-agent")

# Public testnet
client = AetherNetClient("https://testnet.aethernet.network", agent_id="my-agent")
```
