"""AetherNet Python SDK — verified AI work settlement.

Quick start::

    from aethernet import quick_start
    quick_start()

This creates a persistent Ed25519 keypair, registers your agent on the
testnet with AETHERNET-TX-V1 cryptographic signing, and confirms your
onboarding grant.
"""

import os

from .client import AetherNetClient, AetherNetError, Evidence
from .platform import AetherNetPlatform
from .worker import AgentWorker
from .enterprise import Fleet


def quick_start(key_name="aethernet-quickstart", node=None):
    """Connect to AetherNet testnet, register, and verify.

    Creates a persistent Ed25519 keypair, registers on the testnet using
    AETHERNET-TX-V1 cryptographic signing, and confirms the onboarding
    grant was received.

    Args:
        key_name: Name for the keypair file (default: "aethernet-quickstart").
            Keys are stored at ``~/.aethernet/keys/<key_name>.json``.
        node: Testnet URL (default: AETHERNET_NODE env var or
            https://testnet.aethernet.network).

    Returns:
        A connected :class:`AetherNetClient` with ``signing_key`` and
        ``agent_id`` set, ready for posting tasks and transacting.
    """
    from .signing import get_or_create_keypair
    from nacl.encoding import RawEncoder

    node_url = node or os.environ.get("AETHERNET_NODE", "https://testnet.aethernet.network")

    print("AetherNet Quick Start")
    print("=" * 40)
    print()

    # Step 1: Create cryptographic identity
    print("Creating Ed25519 keypair...")
    signing_key = get_or_create_keypair(key_name)
    actor = signing_key.verify_key.encode(encoder=RawEncoder).hex()
    print(f"  Agent ID: {actor[:16]}...")
    print(f"  Keys saved to: ~/.aethernet/keys/{key_name}.json")
    print()

    # Step 2: Connect with TX-V1 signing
    print(f"Connecting to {node_url}...")
    client = AetherNetClient(node_url, signing_key=signing_key)

    try:
        resp = client.register()
        agent_id = resp.get("agent_id", client.agent_id)
        grant = resp.get("onboarding_allocation", 0)
        address = resp.get("deposit_address", "")
        print(f"  Registered: {agent_id[:16]}...")
        if grant:
            print(f"  Onboarding grant: {grant:,} \u00b5AET")
        if address:
            print(f"  Deposit address: {address}")
        print()
    except AetherNetError as exc:
        if exc.status_code in (409, 429):
            print("  Already registered (this is fine)")
            agent_id = client.agent_id
            print()
        else:
            print(f"  Registration failed: {exc}")
            print()
            return client
    except Exception as e:
        print(f"  Registration failed: {e}")
        print()
        return client

    # Step 3: Check network status
    import requests
    try:
        status = requests.get(f"{node_url}/v1/status", timeout=10).json()
        print("Network status:")
        print(f"  DAG size: {status.get('dag_size', 'unknown')} events")
        print(f"  Peers: {status.get('peers', 'unknown')}")
        print()
    except Exception:
        pass

    # Step 4: Check balance
    try:
        bal = requests.get(f"{node_url}/v1/agents/{agent_id}/balance", timeout=10).json()
        balance = bal.get("balance", 0)
        print(f"Balance: {balance:,} \u00b5AET")
    except Exception:
        print(f"Balance: check at {node_url}/v1/agents/{agent_id}/balance")

    print()
    print("Ready! You can now post tasks, register for routing, and transact.")
    print()
    print("Next steps:")
    print("  from aethernet.signing import get_or_create_keypair")
    print("  from aethernet.client import AetherNetClient")
    print(f'  signing_key = get_or_create_keypair("{key_name}")')
    print(f'  client = AetherNetClient("{node_url}", signing_key=signing_key)')
    print('  client.post_task(title="...", description="...", category="research", budget=10000)')

    return client


__version__ = "0.2.0"
__all__ = [
    "AetherNetClient",
    "AetherNetError",
    "Evidence",
    "quick_start",
    "AgentWorker",
    "Fleet",
    "AetherNetPlatform",
]
