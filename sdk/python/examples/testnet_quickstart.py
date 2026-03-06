"""Zero-config AetherNet testnet quickstart.

Run with:
    pip install aethernet[crypto]
    python testnet_quickstart.py

On first run, a local Ed25519 keypair is generated and cached in
~/.aethernet/my-agent.json. Subsequent runs reconnect with the same identity.
"""

from aethernet import AetherNetClient

# Connect to the public testnet — no configuration required.
client = AetherNetClient("https://testnet.aethernet.network")

# One-call onboarding: generates a keypair, saves it locally, and registers.
print("Onboarding with AetherNet testnet...")
result = client.quick_start(agent_name="my-agent")
print(f"  Agent ID      : {result['agent_id']}")
print(f"  Fingerprint   : {result.get('fingerprint_hash', 'N/A')}")
allocation = result.get("onboarding_allocation", 0)
if allocation:
    print(f"  Onboarding AET: {allocation:,} micro-AET")

# Check balance.
balance = client.balance()
print(f"\nBalance: {balance['balance']:,} {balance['currency']}")

# Browse the network status.
status = client.status()
print(f"\nNode status:")
print(f"  Version    : {status.get('version', 'N/A')}")
print(f"  DAG events : {status.get('dag_size', 0)}")
print(f"  Peers      : {status.get('peers', 0)}")
print(f"  OCS pending: {status.get('ocs_pending', 0)}")

# Search the service marketplace.
services = client.search_services(limit=5)
if services:
    print(f"\nAvailable services ({len(services)} found):")
    for svc in services:
        price = svc.get("price_aet", 0)
        print(f"  [{svc.get('category', '?'):12s}] {svc['name']} — {price:,} AET/task")
else:
    print("\nNo services listed yet — be the first!")
    print("  client.register_service('My Service', category='research', price_aet=5000)")

print("\nReady. Use client.transfer(), client.generate(), client.verify() to interact.")
