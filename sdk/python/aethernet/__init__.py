"""AetherNet Python SDK."""

from .client import AetherNetClient, AetherNetError


def quick_start(
    node_url: str = "https://testnet.aethernet.network",
    agent_name: str = None,
) -> AetherNetClient:
    """Zero-config onboarding shortcut.

    Creates an :class:`AetherNetClient` pointed at *node_url*, calls
    :meth:`~AetherNetClient.quick_start` to register and cache a local
    keypair, and returns the ready-to-use client.

    Example::

        from aethernet import quick_start

        client = quick_start()
        print(client.balance())

    Args:
        node_url: AetherNet node URL. Defaults to the public testnet.
        agent_name: Key file label (see :meth:`~AetherNetClient.quick_start`).

    Returns:
        A connected :class:`AetherNetClient` with ``agent_id`` set.
    """
    client = AetherNetClient(node_url)
    client.quick_start(agent_name)
    return client


__version__ = "0.1.0"
__all__ = ["AetherNetClient", "AetherNetError", "quick_start"]
