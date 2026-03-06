"""
AetherNet Enterprise SDK

Manage fleets of agents on AetherNet from a single organisational account.

Usage::

    from aethernet.enterprise import Fleet

    fleet = Fleet(
        base_url="https://testnet.aethernet.network",
        org_id="openai-internal",
    )

    # Register a fleet of agents
    fleet.register_agents([
        {"agent_id": "summarizer-v3", "categories": ["summarization"]},
        {"agent_id": "coder-v2",      "categories": ["code-review"]},
        {"agent_id": "analyst-v1",    "categories": ["data-analysis"]},
    ])

    # Monitor fleet performance
    stats = fleet.stats()
    print(f"Total balance: {stats['total_balance']} micro-AET")
    print(f"Active agents: {stats['active_agents']}")

    # Bulk stake
    fleet.stake_all(amount_per_agent=100_000)
"""

import logging
from typing import Dict, List

from .client import AetherNetClient, AetherNetError

logger = logging.getLogger("aethernet.enterprise")


class Fleet:
    """Manage a fleet of agents on AetherNet under a single organisation."""

    def __init__(self, base_url: str, org_id: str, org_key: str = ""):
        """
        Args:
            base_url: AetherNet node URL.
            org_id: Organisation identifier used as a prefix for all agent IDs.
            org_key: Reserved for future organisation-level authentication.
        """
        self.base_url = base_url
        self.org_id = org_id
        self.org_key = org_key
        self.agents: Dict[str, AetherNetClient] = {}

    def register_agents(self, agent_configs: List[Dict]) -> List[Dict]:
        """Register multiple agents in bulk.

        Each config dict must have ``agent_id`` and may include:
        ``name``, ``description``, ``categories`` (list of str), ``price`` (int micro-AET).

        Returns a list of result dicts, one per agent, with keys
        ``agent_id``, ``status`` (``"registered"`` or ``"error"``), and
        optionally ``info`` or ``error``.
        """
        results = []
        for config in agent_configs:
            agent_id = f"{self.org_id}/{config['agent_id']}"
            client = AetherNetClient(self.base_url, agent_id=agent_id)

            try:
                info = client.quick_start(agent_name=agent_id)
                self.agents[agent_id] = client

                for cat in config.get("categories", []):
                    try:
                        client.register_service(
                            name=config.get("name", config["agent_id"]),
                            description=config.get(
                                "description",
                                f"{config['agent_id']} by {self.org_id}",
                            ),
                            category=cat,
                            price_aet=config.get("price", 5_000_000),
                        )
                    except Exception:
                        pass  # service listing is best-effort

                results.append({"agent_id": agent_id, "status": "registered", "info": info})
                logger.info(f"Registered fleet agent: {agent_id}")

            except AetherNetError as e:
                results.append({"agent_id": agent_id, "status": "error", "error": str(e)})
                logger.error(f"Failed to register {agent_id}: {e}")

        return results

    def get_agent(self, agent_id: str) -> AetherNetClient:
        """Return an :class:`~aethernet.AetherNetClient` for a specific fleet agent.

        The full ``org_id/agent_id`` form is resolved automatically.
        """
        full_id = f"{self.org_id}/{agent_id}"
        if full_id not in self.agents:
            client = AetherNetClient(self.base_url, agent_id=full_id)
            self.agents[full_id] = client
        return self.agents[full_id]

    def stake_all(self, amount_per_agent: int) -> Dict[str, str]:
        """Stake *amount_per_agent* micro-AET for every registered fleet agent.

        Returns a dict mapping agent IDs to ``"staked"`` or an error string.
        """
        results: Dict[str, str] = {}
        for agent_id, client in self.agents.items():
            try:
                client.stake(agent_id=agent_id, amount=amount_per_agent)
                results[agent_id] = "staked"
            except Exception as e:
                results[agent_id] = f"error: {e}"
        return results

    def stats(self) -> Dict:
        """Return aggregate fleet statistics.

        Queries balance and staking info for every registered agent.
        Agents that fail (e.g. not yet on-chain) are counted as inactive
        and skipped without raising.
        """
        total_balance = 0
        total_staked = 0
        total_tasks = 0
        active = 0

        for agent_id, client in self.agents.items():
            try:
                balance_resp = client.balance()
                total_balance += balance_resp.get("balance", 0)
                active += 1

                stake_info = client.stake_info(agent_id=agent_id)
                total_staked += stake_info.get("staked_amount", 0)
                total_tasks += stake_info.get("tasks_completed", 0)
            except Exception:
                pass

        return {
            "org_id": self.org_id,
            "total_agents": len(self.agents),
            "active_agents": active,
            "total_balance": total_balance,
            "total_staked": total_staked,
            "total_tasks_completed": total_tasks,
        }

    def balances(self) -> Dict[str, int]:
        """Return the micro-AET balance for each registered fleet agent."""
        result: Dict[str, int] = {}
        for agent_id, client in self.agents.items():
            try:
                result[agent_id] = client.balance().get("balance", 0)
            except Exception:
                result[agent_id] = 0
        return result

    def reputations(self) -> Dict[str, Dict]:
        """Return the reputation profile for each registered fleet agent."""
        result: Dict[str, Dict] = {}
        for agent_id, client in self.agents.items():
            try:
                result[agent_id] = client.get_reputation(agent_id=agent_id)
            except Exception:
                result[agent_id] = {}
        return result
