# AetherNet Operations Guide

## AWS Cloud Map — Automatic Peer Discovery

AetherNet nodes on ECS Fargate get a new private IP every time a task restarts.
The `--discover` flag (or `AETHERNET_DISCOVER` env var) tells the node to
resolve a DNS name every 30 seconds and automatically connect to any new IPs it
finds. AWS Cloud Map maintains that DNS record, adding and removing task IPs as
containers start and stop.

### 1. Create a private DNS namespace

```bash
aws servicediscovery create-private-dns-namespace \
  --name aethernet.local \
  --vpc vpc-XXXXXXXX \
  --region us-east-1
```

Note the `NamespaceId` returned (e.g. `ns-XXXXXXXXXXXXXXXX`).

### 2. Create a Cloud Map service

```bash
aws servicediscovery create-service \
  --name nodes \
  --namespace-id ns-XXXXXXXXXXXXXXXX \
  --dns-config "NamespaceId=ns-XXXXXXXXXXXXXXXX,DnsRecords=[{Type=A,TTL=10}]" \
  --health-check-custom-config FailureThreshold=1 \
  --region us-east-1
```

Note the `ServiceId` returned (e.g. `srv-XXXXXXXXXXXXXXXX`).

### 3. Wire the ECS service to Cloud Map

In the ECS service definition (console or Terraform), add a **Service registries**
entry pointing at the Cloud Map service created above. ECS will register each
task's private IP automatically when it starts and deregister it when it stops.

Terraform example:

```hcl
resource "aws_ecs_service" "aethernet_node" {
  # …
  service_registries {
    registry_arn = aws_service_discovery_service.nodes.arn
  }
}
```

### 4. Configure the node

Set the environment variable on every ECS task definition:

```
AETHERNET_DISCOVER=nodes.aethernet.local
```

Or pass the flag directly:

```
aethernet start --discover nodes.aethernet.local
```

The node will resolve `nodes.aethernet.local` (which Cloud Map answers with A
records for all live tasks) every 30 seconds and dial any IP it hasn't seen
before. When a task restarts and gets a new IP, the other nodes pick it up
within 30 seconds — no manual intervention or hardcoded IPs required.

> **Tip:** Keep the DNS TTL at 10 seconds (as shown above) so stale addresses
> expire quickly during rolling deploys.

### 5. Verify connectivity

From any running node, check the peer count via the health endpoint:

```bash
curl http://NODE_IP:8338/health
```

You should see `"peers": 2` (or higher) once all three testnet nodes are
connected.

---

## Static peers (`--peer`)

The `--peer` flag and `AETHERNET_PEER` environment variable still work and are
**additive** — you can mix static bootstrap peers with DNS discovery:

```
AETHERNET_PEER=10.0.1.10:8337
AETHERNET_DISCOVER=nodes.aethernet.local
```

Static peers are connected once at startup. DNS discovery handles reconnection
after restarts.

---

## Database recovery

If a node fails to start due to a corrupt BadgerDB store, set
`AETHERNET_RESET=true` in the task definition and redeploy. The node will wipe
its store and start fresh. Clear the variable after recovery to prevent
accidental wipes on subsequent restarts.
