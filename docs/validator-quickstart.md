# Running an AetherNet Validator Node

## What You're Running

A validator node participates in AetherNet's BFT consensus — verifying AI work, voting on results, and earning verification rewards. Validators stake collateral to back their verdicts. Accurate validators earn more assignments and higher rewards. Fraudulent approvals get slashed.

This guide gets you from zero to a running, synced validator on the AetherNet testnet.

**Time required:** ~15 minutes
**Cost:** One EC2 instance (~$0.10/hr for m7i.large)

---

## Prerequisites

- An AWS account (or any cloud provider with Docker support)
- SSH access to your server
- Basic familiarity with Docker and command line

---

## Step 1: Launch a Server

**Minimum specs:**
- 2 vCPU, 8 GB RAM (AWS m7i.large or equivalent)
- 50 GB SSD storage
- Ubuntu 24.04 LTS
- Ports open: 8337 (P2P), 8338 (API)

**AWS quick launch:**
```bash
# Launch an m7i.large in us-east-1 (same region as testnet for lowest latency)
# Security group: allow inbound TCP 8337, 8338 from anywhere
# Use Ubuntu 24.04 AMI
```

SSH into your server:
```bash
ssh -i your-key.pem ubuntu@your-server-ip
```

---

## Step 2: Install Docker

```bash
sudo apt-get update
sudo apt-get install -y docker.io
sudo systemctl enable docker
sudo systemctl start docker
```

Verify:
```bash
sudo docker --version
```

---

## Step 3: Generate Validator Keys

Every validator has a unique Ed25519 keypair. Your validator identity is derived from this key — it cannot be changed later.

```bash
# Create persistent key directory
sudo mkdir -p /data/aethernet/node_keys

# Generate validator keypair
sudo docker run --rm \
  -v /data/aethernet/node_keys:/keys \
  435998721364.dkr.ecr.us-east-1.amazonaws.com/aethernet:latest \
  aethernet keygen --output /keys/validator.key

# Display your validator public key (this is your validator ID)
sudo cat /data/aethernet/node_keys/validator.key.pub
```

**Save your public key.** You'll need to share it with the network operator to be added to the validator manifest.

**Back up your private key.** If you lose it, you lose your validator identity, stake, and reputation. Store a copy somewhere safe outside the server.

---

## Step 4: Get the Validator Manifest

The validator manifest lists all authorized validators and their public keys. For the testnet, request the current manifest from the AetherNet team.

```bash
# Download the manifest (replace URL with the one provided to you)
sudo mkdir -p /data/aethernet
sudo curl -o /data/aethernet/validator-manifest.json \
  https://raw.githubusercontent.com/Aethernet-network/aethernet/main/testnet/validator-manifest.json
```

Verify your public key appears in the manifest:
```bash
cat /data/aethernet/validator-manifest.json | python3 -m json.tool
```

---

## Step 5: Authenticate with ECR

The node binary is distributed as a Docker image via AWS ECR.

```bash
# Install AWS CLI if not present
sudo apt-get install -y awscli

# Authenticate with ECR (testnet images are public-read)
aws ecr get-login-password --region us-east-1 | \
  sudo docker login --username AWS --password-stdin \
  435998721364.dkr.ecr.us-east-1.amazonaws.com

# Pull the latest image
sudo docker pull 435998721364.dkr.ecr.us-east-1.amazonaws.com/aethernet:latest
```

---

## Step 6: Configure Peers

Your node needs to know how to find existing nodes on the network. The peer list contains the private IPs (if in the same VPC) or public IPs of other nodes.

```bash
# Set your peer list (get current peer IPs from the AetherNet team)
# Format: comma-separated IP:port pairs
# Example (testnet):
export PEERS="172.31.12.70:8337,172.31.93.186:8337,172.31.17.237:8337,172.31.4.3:8337,172.31.13.36:8337"
```

If you're running outside the testnet VPC, use public IPs on port 8337.

---

## Step 7: Start Your Validator

```bash
sudo docker run -d \
  --name aethernet \
  --restart unless-stopped \
  -p 8337:8337 \
  -p 8338:8338 \
  -v /data/aethernet:/data \
  -e AETHERNET_DATA=/data \
  -e AETHERNET_LISTEN=0.0.0.0:8337 \
  -e AETHERNET_API=0.0.0.0:8338 \
  -e AETHERNET_TESTNET=true \
  -e AETHERNET_CONSENSUS_MIN_PARTICIPANTS=2 \
  -e AETHERNET_VALIDATOR_MANIFEST=/data/validator-manifest.json \
  -e AETHERNET_PEER=$PEERS \
  435998721364.dkr.ecr.us-east-1.amazonaws.com/aethernet:latest
```

---

## Step 8: Verify Your Node

### Check it's running:
```bash
sudo docker logs aethernet 2>&1 | head -20
```

You should see startup logs including peer connections and sync activity.

### Check status:
```bash
curl -s http://localhost:8338/v1/status | python3 -m json.tool
```

Expected output:
```json
{
  "agent_id": "your-validator-public-key-hex",
  "version": "0.1.0-testnet",
  "peers": 4,
  "dag_size": 237,
  "ocs_pending": 0,
  "supply_ratio": 1
}
```

**What to verify:**
- `peers` should be > 0 (ideally matches the number of other nodes)
- `dag_size` should match other nodes (check within 30 seconds of starting — sync converges quickly)
- `ocs_pending` should be 0 or very low

### Check sync is working:
```bash
sudo docker logs aethernet 2>&1 | grep 'sync: tick' | tail -5
```

You should see periodic sync ticks with your local DAG size matching the network.

### Check you appear as a validator:
```bash
curl -s http://localhost:8338/v1/validators | python3 -m json.tool
```

Your validator public key should appear in the list.

---

## Step 9: Set Recovery Keys (Recommended)

Recovery keys allow you to rotate your validator key if the primary key is compromised, without losing your identity, stake, or reputation.

```bash
# Generate a recovery keypair (store this OFFLINE — USB drive, hardware wallet, paper)
sudo docker run --rm \
  -v /data/aethernet/node_keys:/keys \
  435998721364.dkr.ecr.us-east-1.amazonaws.com/aethernet:latest \
  aethernet keygen --output /keys/recovery.key

# Register the recovery key with the protocol
curl -X POST https://testnet.aethernet.network/v1/validator/recovery \
  -H "Content-Type: application/json" \
  -d '{"recovery_public_key": "'$(cat /data/aethernet/node_keys/recovery.key.pub)'"}'
```

**Store recovery keys separately from validator keys.** If your server is compromised, the attacker gets your validator key but not your recovery key.

---

## Monitoring

### Health check (run periodically or via cron):
```bash
curl -s http://localhost:8338/v1/status | python3 -c "
import sys, json
d = json.load(sys.stdin)
print(f\"peers={d['peers']} dag={d['dag_size']} pending={d['ocs_pending']}\")
if d['peers'] == 0: print('WARNING: no peers connected')
"
```

### Check for errors:
```bash
sudo docker logs aethernet 2>&1 | grep -i 'ERROR\|WARN\|panic' | tail -10
```

### Watch sync activity:
```bash
sudo docker logs -f aethernet 2>&1 | grep 'sync:'
```

### Check DAG convergence against the network:
```bash
# Compare your DAG size to the public testnet endpoint
LOCAL=$(curl -s http://localhost:8338/v1/status | python3 -c "import sys,json; print(json.load(sys.stdin)['dag_size'])")
NETWORK=$(curl -s https://testnet.aethernet.network/v1/status | python3 -c "import sys,json; print(json.load(sys.stdin)['dag_size'])")
echo "Local: $LOCAL  Network: $NETWORK"
```

Both numbers should be equal or within 1-2 of each other.

---

## Updating Your Node

When a new version is released:

```bash
# Pull the new image
aws ecr get-login-password --region us-east-1 | \
  sudo docker login --username AWS --password-stdin \
  435998721364.dkr.ecr.us-east-1.amazonaws.com

sudo docker pull 435998721364.dkr.ecr.us-east-1.amazonaws.com/aethernet:latest

# Restart (preserves your data and keys)
sudo docker stop aethernet
sudo docker rm aethernet

# Re-run the same docker run command from Step 7
```

**Do NOT delete /data/aethernet/** — this contains your keys and DAG data. Only wipe if explicitly instructed for a breaking protocol upgrade.

---

## Troubleshooting

### Node shows 0 peers
- Check security group: ports 8337 and 8338 must be open for inbound TCP
- Check peer list: are the IPs correct and reachable?
- Check if other nodes are running: `curl -s http://PEER_IP:8338/v1/status`

### DAG size doesn't match network
- Wait 30 seconds — sync converges within 2-3 cycles
- Check sync logs: `sudo docker logs aethernet 2>&1 | grep 'sync:'`
- If stuck, restart: `sudo docker restart aethernet`

### Node crashes on startup
- Check logs: `sudo docker logs aethernet 2>&1 | head -50`
- Verify manifest exists: `ls -la /data/aethernet/validator-manifest.json`
- Verify keys exist: `ls -la /data/aethernet/node_keys/`

### "validator not in manifest" error
- Your public key must be in the validator manifest before starting
- Contact the AetherNet team to be added to the testnet manifest

---

## What Happens Next

Once your validator is running and synced:

1. **Your node automatically participates in consensus.** When tasks are submitted, your validator votes on verification results based on the autovalidator's structural checks.

2. **You earn verification rewards.** Every accurate verdict earns a share of the assurance fee that task posters pay.

3. **You can deploy custom verification backends.** Instead of relying on the default autovalidator, build category-specific verification logic (e.g., re-querying data sources for drug interaction analyses, re-executing code for code review tasks). Custom backends earn higher rewards for higher-quality verification.

4. **Your reputation compounds.** Accurate verdicts build your validator reputation score. Higher reputation means more verification assignments and higher rewards.

---

## Questions?

- **Protocol repo:** [github.com/Aethernet-network/aethernet](https://github.com/Aethernet-network/aethernet)
- **Testnet API:** [testnet.aethernet.network](https://testnet.aethernet.network)
- **Signing spec:** `docs/tx-v1-signing-spec.md` in the repo
- **Audit reports:** `docs/protocol-freeze-*-audit.md` in the repo
