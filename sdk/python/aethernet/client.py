"""AetherNet Python SDK — stdlib-only HTTP client for the AetherNet node API.

Uses only the Python standard library (urllib, json); no third-party packages
required.  Import langchain_tools for optional LangChain integration.

Quick start::

    from aethernet import AetherNetClient

    client = AetherNetClient("http://localhost:8338")
    agent_id = client.register()
    event_id = client.generate(
        claimed_value=5000,
        evidence_hash="sha256:...",
        task_description="inference run",
        stake_amount=1000,
    )
"""

import json
import urllib.request
import urllib.error


class AetherNetError(Exception):
    """Raised when the AetherNet API returns an error response."""

    def __init__(self, message: str, status_code: int = 0):
        super().__init__(message)
        self.message = message
        self.status_code = status_code


class AetherNetClient:
    """HTTP client for the AetherNet node REST API.

    Args:
        base_url: Scheme + host of the target node (e.g. "http://localhost:8338").
            No trailing slash required.
    """

    def __init__(self, base_url: str = "http://localhost:8338", agent_id: str = ""):
        self.base_url = base_url.rstrip("/")
        self.node_url = self.base_url  # alias used by framework tool integrations
        self.agent_id = agent_id

    # ------------------------------------------------------------------
    # Internal transport
    # ------------------------------------------------------------------

    def _request(self, method: str, path: str, body=None):
        url = self.base_url + path
        data = json.dumps(body).encode() if body is not None else None
        headers = {"Content-Type": "application/json"} if data is not None else {}
        req = urllib.request.Request(url, data=data, headers=headers, method=method)
        try:
            with urllib.request.urlopen(req) as resp:
                return json.loads(resp.read())
        except urllib.error.HTTPError as e:
            raw = e.read()
            try:
                payload = json.loads(raw)
                msg = payload.get("error", raw.decode())
            except Exception:
                msg = raw.decode()
            raise AetherNetError(msg, e.code) from None

    # ------------------------------------------------------------------
    # API methods
    # ------------------------------------------------------------------

    def register(self, capabilities=None) -> str:
        """Register this node's agent in the identity registry.

        Returns:
            The agent_id string.  Idempotent — safe to call multiple times.
        """
        result = self._request("POST", "/v1/agents", {"capabilities": capabilities or []})
        return result["agent_id"]

    def balance(self, agent_id: str = "") -> dict:
        """Return the spendable balance for agent_id.

        If agent_id is omitted the client's own agent_id (set in constructor) is used.

        Returns:
            Dict with keys: agent_id, balance (int micro-AET), currency.
        """
        aid = agent_id or self.agent_id
        if not aid:
            raise ValueError("agent_id required: pass to AetherNetClient() or balance()")
        return self._request("GET", f"/v1/agents/{aid}/balance")

    def profile(self, agent_id: str = "") -> dict:
        """Return the capability fingerprint for agent_id.

        If agent_id is omitted the client's own agent_id (set in constructor) is used.

        Returns:
            Dict with keys: agent_id, capabilities, reputation_score,
            optimistic_trust_limit, total_value_generated, etc.
        """
        aid = agent_id or self.agent_id
        if not aid:
            raise ValueError("agent_id required: pass to AetherNetClient() or profile()")
        return self._request("GET", f"/v1/agents/{aid}")

    def agents(self) -> list:
        """Return list of all registered agent fingerprints."""
        return self._request("GET", "/v1/agents")

    def generate(
        self,
        claimed_value: int,
        evidence_hash: str,
        task_description: str = "",
        stake_amount: int = 1000,
        beneficiary_agent: str = "",
        beneficiary: str = "",  # alias for beneficiary_agent (used by framework tools)
        causal_refs=None,
    ) -> str:
        """Submit a Generation event to claim value for completed AI work.

        Args:
            claimed_value: Value of the work in micro-AET.
            evidence_hash: SHA-256 hash of the work output (e.g. "sha256:abc...").
            task_description: Human-readable description of the task.
            stake_amount: Stake in micro-AET backing this claim.
            beneficiary_agent: Receiving agent ID; defaults to node's own agent.
            causal_refs: List of event IDs this event causally follows.

        Returns:
            The event_id string for the submitted Generation event.
        """
        beneficiary_agent = beneficiary_agent or beneficiary
        body: dict = {
            "claimed_value": claimed_value,
            "evidence_hash": evidence_hash,
            "task_description": task_description,
            "stake_amount": stake_amount,
        }
        if beneficiary_agent:
            body["beneficiary_agent"] = beneficiary_agent
        if causal_refs:
            body["causal_refs"] = causal_refs
        result = self._request("POST", "/v1/generation", body)
        return result["event_id"]

    def transfer(
        self,
        to_agent: str,
        amount: int,
        currency: str = "AET",
        memo: str = "",
        stake_amount: int = 1000,
        causal_refs=None,
    ) -> str:
        """Submit a Transfer event to send micro-AET to another agent.

        Args:
            to_agent: AgentID of the recipient.
            amount: Amount in micro-AET to transfer.
            currency: Token symbol; defaults to "AET".
            memo: Optional free-text memo.
            stake_amount: Stake in micro-AET backing this transfer.
            causal_refs: List of event IDs this event causally follows.

        Returns:
            The event_id string for the submitted Transfer event.
        """
        body: dict = {
            "to_agent": to_agent,
            "amount": amount,
            "currency": currency,
            "memo": memo,
            "stake_amount": stake_amount,
        }
        if causal_refs:
            body["causal_refs"] = causal_refs
        result = self._request("POST", "/v1/transfer", body)
        return result["event_id"]

    def get_event(self, event_id: str) -> dict:
        """Return the DAG event for event_id."""
        return self._request("GET", f"/v1/events/{event_id}")

    def tips(self) -> list:
        """Return the current DAG tip event IDs."""
        result = self._request("GET", "/v1/dag/tips")
        return result["tips"]

    def status(self) -> dict:
        """Return a point-in-time health snapshot of the node.

        Returns:
            Dict with keys: agent_id, version, peers, dag_size,
            ocs_pending, supply_ratio.
        """
        return self._request("GET", "/v1/status")

    def verify(self, event_id: str, verdict: bool, verified_value: int = 0) -> dict:
        """Submit a verification verdict for a pending OCS event.

        Args:
            event_id: ID of the pending event to adjudicate.
            verdict: True if the work is valid; False if fraudulent/invalid.
            verified_value: Confirmed value in micro-AET (Generation events only).

        Returns:
            Dict with keys: event_id, verdict, status ("settled" or "adjusted").

        Raises:
            AetherNetError: If the event is not in the pending map.
        """
        body = {
            "event_id": event_id,
            "verdict": verdict,
            "verified_value": verified_value,
        }
        return self._request("POST", "/v1/verify", body)

    def pending(self) -> list:
        """Return list of all events currently awaiting OCS verification.

        Each item dict has keys: EventID, EventType, AgentID, Amount,
        OptimisticAt, Deadline (nanoseconds).
        """
        return self._request("GET", "/v1/pending")
