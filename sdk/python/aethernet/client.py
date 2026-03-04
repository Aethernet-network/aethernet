"""AetherNet Python SDK — HTTP client for the AetherNet node REST API.

Uses ``requests`` for HTTP; install it with ``pip install aethernet`` or
``pip install requests``.

Quick start::

    from aethernet import AetherNetClient

    client = AetherNetClient("http://localhost:8338", agent_id="my-agent")
    client.register()
    event_id = client.transfer(to_agent="bob", amount=1000, memo="payment")
    balance = client.balance()
    print(f"Balance: {balance['balance']} {balance['currency']}")
"""

from typing import Any, Dict, List, Optional

import requests


class AetherNetClient:
    """HTTP client for the AetherNet node REST API.

    Args:
        node_url: Scheme + host of the target node (e.g. "http://localhost:8338").
        agent_id: Agent ID used for balance, profile, and reputation lookups.
            May be set after construction by assigning ``client.agent_id``.
    """

    def __init__(self, node_url: str = "http://localhost:8338", agent_id: str = ""):
        self.node_url = node_url.rstrip("/")
        self.agent_id = agent_id
        self.session = requests.Session()
        self.session.headers["Content-Type"] = "application/json"

    # ------------------------------------------------------------------
    # Agent endpoints
    # ------------------------------------------------------------------

    def register(self, capabilities: Optional[List[Dict]] = None) -> Dict[str, Any]:
        """Register the node's own agent in the identity registry.

        Returns a dict with ``agent_id`` and ``fingerprint_hash``.
        Idempotent — safe to call multiple times.
        """
        return self._post("/v1/agents", {"capabilities": capabilities or []})

    def profile(self, agent_id: str = "") -> Dict[str, Any]:
        """Return the capability fingerprint for *agent_id*.

        Uses ``self.agent_id`` when *agent_id* is omitted.
        """
        aid = agent_id or self.agent_id
        if not aid:
            raise ValueError("agent_id required: pass to AetherNetClient() or profile()")
        return self._get(f"/v1/agents/{aid}")

    def balance(self, agent_id: str = "") -> Dict[str, Any]:
        """Return the spendable balance for *agent_id*.

        Uses ``self.agent_id`` when *agent_id* is omitted.

        Returns a dict with keys: ``agent_id``, ``balance`` (int micro-AET),
        ``currency``.
        """
        aid = agent_id or self.agent_id
        if not aid:
            raise ValueError("agent_id required: pass to AetherNetClient() or balance()")
        return self._get(f"/v1/agents/{aid}/balance")

    def agents(self, limit: int = 100, offset: int = 0) -> List[Dict]:
        """Return a list of all registered agent fingerprints."""
        resp = self._get(f"/v1/agents?limit={limit}&offset={offset}")
        if isinstance(resp, list):
            return resp
        return resp.get("agents", [])

    # ------------------------------------------------------------------
    # Event submission
    # ------------------------------------------------------------------

    def transfer(
        self,
        to_agent: str,
        amount: int,
        memo: str = "",
        currency: str = "AET",
        stake_amount: int = 5000,
        causal_refs: Optional[List[str]] = None,
    ) -> str:
        """Submit a Transfer event. Returns the event_id string."""
        body: Dict[str, Any] = {
            "to_agent": to_agent,
            "amount": amount,
            "currency": currency,
            "memo": memo,
            "stake_amount": stake_amount,
        }
        if causal_refs:
            body["causal_refs"] = causal_refs
        resp = self._post("/v1/transfer", body)
        return resp.get("event_id", "")

    def generate(
        self,
        claimed_value: int,
        evidence_hash: str,
        task_description: str = "",
        stake_amount: int = 5000,
        beneficiary_agent: str = "",
        beneficiary: str = "",  # alias for beneficiary_agent
        causal_refs: Optional[List[str]] = None,
    ) -> str:
        """Submit a Generation event. Returns the event_id string."""
        beneficiary_agent = beneficiary_agent or beneficiary
        body: Dict[str, Any] = {
            "claimed_value": claimed_value,
            "evidence_hash": evidence_hash,
            "task_description": task_description,
            "stake_amount": stake_amount,
        }
        if beneficiary_agent:
            body["beneficiary_agent"] = beneficiary_agent
        if causal_refs:
            body["causal_refs"] = causal_refs
        resp = self._post("/v1/generation", body)
        return resp.get("event_id", "")

    def verify(self, event_id: str, verdict: bool, verified_value: int = 0) -> Dict[str, Any]:
        """Submit a verification verdict for a pending OCS event.

        Returns a dict with ``event_id``, ``verdict``, and ``status``
        ("settled" or "adjusted").

        Raises:
            AetherNetError: If the event is not in the pending map (HTTP 400).
        """
        return self._post("/v1/verify", {
            "event_id": event_id,
            "verdict": verdict,
            "verified_value": verified_value,
        })

    # ------------------------------------------------------------------
    # Read endpoints
    # ------------------------------------------------------------------

    def get_event(self, event_id: str) -> Dict[str, Any]:
        """Return the DAG event for *event_id*."""
        return self._get(f"/v1/events/{event_id}")

    def status(self) -> Dict[str, Any]:
        """Return a point-in-time health snapshot of the node."""
        return self._get("/v1/status")

    def tips(self) -> List[str]:
        """Return the current DAG tip event IDs."""
        resp = self._get("/v1/dag/tips")
        return resp.get("tips", [])

    def pending(self) -> List[Dict[str, Any]]:
        """Return all events currently awaiting OCS verification."""
        return self._get("/v1/pending")

    # ------------------------------------------------------------------
    # Economics / staking endpoints
    # ------------------------------------------------------------------

    def stake(self, agent_id: str, amount: int) -> Dict[str, Any]:
        """Stake *amount* micro-AET tokens for *agent_id*.

        Returns a dict with ``agent_id``, ``staked_amount``, and ``trust_limit``.

        Raises:
            AetherNetError: If staking is not enabled on the node (HTTP 501).
        """
        return self._post("/v1/stake", {"agent_id": agent_id, "amount": amount})

    def unstake(self, agent_id: str, amount: int) -> Dict[str, Any]:
        """Unstake *amount* micro-AET tokens for *agent_id*.

        Returns a dict with ``agent_id``, ``staked_amount``, and ``trust_limit``.

        Raises:
            AetherNetError: If the agent has insufficient staked balance (HTTP 400)
                or staking is not enabled (HTTP 501).
        """
        return self._post("/v1/unstake", {"agent_id": agent_id, "amount": amount})

    def stake_info(self, agent_id: str = "") -> Dict[str, Any]:
        """Return the staking state for *agent_id*.

        Uses ``self.agent_id`` when *agent_id* is omitted.

        Returns a dict with ``agent_id``, ``staked_amount``, ``trust_multiplier``,
        and ``trust_limit``.
        """
        aid = agent_id or self.agent_id
        if not aid:
            raise ValueError("agent_id required: pass to AetherNetClient() or stake_info()")
        return self._get(f"/v1/agents/{aid}/stake")

    def economics(self) -> Dict[str, Any]:
        """Return a snapshot of the network's token economics.

        Returns a dict with ``total_supply``, ``onboarding_pool_total``,
        ``onboarding_max_agents``, ``onboarding_allocated``, ``total_collected``,
        ``total_burned``, ``treasury_accrued``, and ``fee_basis_points``.
        """
        return self._get("/v1/economics")

    # ------------------------------------------------------------------
    # Transport helpers
    # ------------------------------------------------------------------

    def _get(self, path: str) -> Any:
        resp = self.session.get(self.node_url + path)
        resp.raise_for_status()
        return resp.json()

    def _post(self, path: str, body: Dict) -> Any:
        resp = self.session.post(self.node_url + path, json=body)
        if resp.status_code >= 400:
            try:
                err = resp.json().get("error", resp.text)
            except Exception:
                err = resp.text
            raise AetherNetError(resp.status_code, err)
        return resp.json()


class AetherNetError(Exception):
    """Raised when the AetherNet API returns an error response."""

    def __init__(self, status_code: int, message: str):
        self.status_code = status_code
        self.message = message
        super().__init__(f"AetherNet API error {status_code}: {message}")
