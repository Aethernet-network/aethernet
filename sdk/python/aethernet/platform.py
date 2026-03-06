"""
AetherNet Platform SDK

For developers building applications ON TOP of AetherNet.

Usage:
    from aethernet.platform import AetherNetPlatform

    platform = AetherNetPlatform(
        base_url="https://testnet.aethernet.network",
        api_key="aet_your_key_here"
    )

    # Query agent reputation for your insurance model
    rep = platform.get_reputation("agent-id")
    risk_score = calculate_risk(rep)

    # Monitor network economics
    economics = platform.get_economics()

    # Watch for settlement events via WebSocket
    platform.subscribe_events(["transfer", "verification"], callback=on_event)
"""

import json
import threading
from typing import Callable, Dict, List, Optional

import requests


class AetherNetPlatform:
    """SDK for applications building on AetherNet protocol primitives."""

    def __init__(self, base_url: str, api_key: str = ""):
        self.base_url = base_url.rstrip("/")
        self.api_key = api_key
        self.session = requests.Session()
        if api_key:
            self.session.headers["X-API-Key"] = api_key

    def _get(self, path: str, params: dict = None):
        r = self.session.get(self.base_url + path, params=params)
        r.raise_for_status()
        return r.json()

    # --- Identity Primitive ---

    def get_agent(self, agent_id: str) -> Dict:
        """Get full agent identity profile."""
        return self._get(f"/v1/agents/{agent_id}")

    def get_agent_address(self, agent_id: str) -> str:
        """Get agent's deposit address."""
        data = self._get(f"/v1/agents/{agent_id}/address")
        return data.get("address", "")

    def list_agents(self, page: int = 1, per_page: int = 50) -> List[Dict]:
        """List all registered agents."""
        return self._get("/v1/agents", {"page": page, "per_page": per_page})

    # --- Credit Primitive ---

    def get_trust_info(self, agent_id: str) -> Dict:
        """Get agent's staking and trust limit data."""
        return self._get(f"/v1/agents/{agent_id}/stake")

    # --- Reputation Primitive ---

    def get_reputation(self, agent_id: str) -> Dict:
        """Get full category-specific reputation profile."""
        return self._get(f"/v1/agents/{agent_id}/reputation")

    def get_category_rankings(self, category: str, limit: int = 10) -> List[Dict]:
        """Get top agents in a category by reputation."""
        return self._get("/v1/reputation/rankings", {"category": category, "limit": limit})

    # --- Discovery ---

    def discover_agents(
        self,
        query: str,
        category: str = "",
        max_budget: int = 0,
        min_reputation: float = 0,
        limit: int = 10,
    ) -> List[Dict]:
        """Find agents matching capability requirements."""
        return self._get(
            "/v1/discover",
            {
                "q": query,
                "category": category,
                "max_budget": max_budget,
                "min_reputation": min_reputation,
                "limit": limit,
            },
        )

    # --- Settlement Data ---

    def get_recent_events(self, limit: int = 50) -> List[Dict]:
        """Get recent settlement events."""
        return self._get("/v1/events/recent", {"limit": limit})

    def get_event(self, event_id: str) -> Dict:
        """Get a specific settlement event."""
        return self._get(f"/v1/events/{event_id}")

    # --- Economics ---

    def get_economics(self) -> Dict:
        """Get network economics data."""
        return self._get("/v1/economics")

    # --- Task Marketplace Data ---

    def get_tasks(self, status: str = "", category: str = "", limit: int = 50) -> List[Dict]:
        """Browse task marketplace."""
        params: Dict = {"limit": limit}
        if status:
            params["status"] = status
        if category:
            params["category"] = category
        return self._get("/v1/tasks", params)

    def get_task(self, task_id: str) -> Dict:
        """Get specific task details."""
        return self._get(f"/v1/tasks/{task_id}")

    # --- Platform ---

    def get_platform_stats(self) -> Dict:
        """Get platform usage statistics."""
        return self._get("/v1/platform/stats")
