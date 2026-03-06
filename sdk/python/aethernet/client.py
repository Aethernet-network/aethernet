"""AetherNet Python SDK — HTTP client for the AetherNet node REST API.

Uses ``requests`` for HTTP; install it with ``pip install aethernet-sdk`` or
``pip install requests``.

Quick start::

    from aethernet import AetherNetClient

    client = AetherNetClient("http://localhost:8338", agent_id="my-agent")
    client.register()
    event_id = client.transfer(to_agent="bob", amount=1000, memo="payment")
    balance = client.balance()
    print(f"Balance: {balance['balance']} {balance['currency']}")
"""

from typing import Any, Dict, List, Optional, Union

import requests


class Evidence:
    """Structured proof-of-work attached to a task submission.

    Automatically computes a sha256 hash of the output and captures size and
    an optional preview for quality scoring by the auto-validator.

    Args:
        output:      Raw output bytes or string (e.g. generated text, JSON, code).
        output_type: One of: "text", "json", "code", "data", "image".
        summary:     Human-readable description of what was produced (used for
                     relevance scoring against the task description).
        metrics:     Optional dict of string metrics (e.g. {"accuracy": "0.95"}).
        input_hash:  Optional sha256 hash of the input the work was based on.
        output_url:  Optional URI where the full output can be retrieved.
    """

    def __init__(
        self,
        output: Union[str, bytes],
        output_type: str = "text",
        summary: str = "",
        metrics: Optional[Dict[str, str]] = None,
        input_hash: str = "",
        output_url: str = "",
    ):
        import hashlib

        raw = output.encode() if isinstance(output, str) else output
        self.hash = "sha256:" + hashlib.sha256(raw).hexdigest()
        self.output_type = output_type
        self.output_size = len(raw)
        self.summary = summary
        self.metrics = metrics or {}
        self.input_hash = input_hash
        self.output_url = output_url
        self.output_preview = (output[:500] if isinstance(output, str) else raw[:500].decode("utf-8", errors="replace"))

    def to_dict(self) -> Dict[str, Any]:
        """Serialise to a dict suitable for the AetherNet API."""
        d: Dict[str, Any] = {
            "hash": self.hash,
            "output_type": self.output_type,
            "output_size": self.output_size,
            "summary": self.summary,
            "output_preview": self.output_preview,
        }
        if self.metrics:
            d["metrics"] = self.metrics
        if self.input_hash:
            d["input_hash"] = self.input_hash
        if self.output_url:
            d["output_url"] = self.output_url
        return d


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

    def quick_start(self, agent_name: str = None) -> Dict[str, Any]:
        """One-call onboarding: generate a local keypair, register, and return.

        On first call, generates an Ed25519 keypair (requires
        ``pip install aethernet-sdk[crypto]``) and saves it to
        ``~/.aethernet/<agent_name>.json`` (mode 0o600). Falls back to a
        random identifier when ``cryptography`` is not installed.

        On subsequent calls with the same *agent_name* the existing key is
        loaded so the same identity is reused (idempotent).

        Args:
            agent_name: Human-readable label for the saved key file.
                Defaults to ``"default"``.

        Returns:
            The registration dict (``agent_id``, ``fingerprint_hash``, …).
        """
        import json
        import os

        aethernet_dir = os.path.expanduser("~/.aethernet")
        os.makedirs(aethernet_dir, mode=0o700, exist_ok=True)

        key_name = agent_name or "default"
        key_path = os.path.join(aethernet_dir, f"{key_name}.json")

        if os.path.exists(key_path):
            with open(key_path) as f:
                saved = json.load(f)
            self.agent_id = saved.get("agent_id", self.agent_id)
        else:
            saved = self._generate_keypair()
            fd = os.open(key_path, os.O_WRONLY | os.O_CREAT | os.O_TRUNC, 0o600)
            with os.fdopen(fd, "w") as f:
                json.dump(saved, f, indent=2)

        result = self.register()
        # Update agent_id from the server response — the node always uses its
        # own keypair identity, so we mirror it here for convenience.
        if "agent_id" in result:
            self.agent_id = result["agent_id"]
        return result

    @staticmethod
    def _generate_keypair() -> Dict[str, Any]:
        """Generate an Ed25519 keypair. Returns a dict with agent_id, public_key,
        and private_key as hex strings. Falls back to a random hex ID when the
        ``cryptography`` package is not installed."""
        try:
            import binascii

            from cryptography.hazmat.primitives.asymmetric.ed25519 import (
                Ed25519PrivateKey,
            )
            from cryptography.hazmat.primitives.serialization import (
                Encoding,
                NoEncryption,
                PrivateFormat,
                PublicFormat,
            )

            private_key = Ed25519PrivateKey.generate()
            public_key = private_key.public_key()
            pub_bytes = public_key.public_bytes(Encoding.Raw, PublicFormat.Raw)
            priv_bytes = private_key.private_bytes(
                Encoding.Raw, PrivateFormat.Raw, NoEncryption()
            )
            agent_id = binascii.hexlify(pub_bytes).decode()
            return {
                "agent_id": agent_id,
                "public_key": binascii.hexlify(pub_bytes).decode(),
                "private_key": binascii.hexlify(priv_bytes).decode(),
            }
        except ImportError:
            import secrets

            agent_id = secrets.token_hex(32)
            return {"agent_id": agent_id}

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
    # Service registry endpoints
    # ------------------------------------------------------------------

    def register_service(
        self,
        name: str,
        description: str = "",
        category: str = "",
        price_aet: int = 0,
        endpoint: str = "",
        tags: Optional[List[str]] = None,
        active: bool = True,
    ) -> Dict[str, Any]:
        """Publish or update a service listing for this node's agent.

        Returns the stored listing dict (includes ``created_at``, ``updated_at``).
        """
        body: Dict[str, Any] = {
            "name": name,
            "description": description,
            "category": category,
            "price_aet": price_aet,
            "active": active,
        }
        if endpoint:
            body["endpoint"] = endpoint
        if tags:
            body["tags"] = tags
        return self._post("/v1/registry", body)

    def search_services(
        self,
        query: str = "",
        category: str = "",
        limit: int = 20,
    ) -> List[Dict[str, Any]]:
        """Search for active service listings.

        Args:
            query:    Keyword matched against name, description, and tags.
            category: Filter by exact category (case-insensitive).
            limit:    Maximum number of results to return.

        Returns a list of listing dicts.
        """
        params: Dict[str, Any] = {"limit": limit}
        if query:
            params["q"] = query
        if category:
            params["category"] = category
        resp = self.session.get(self.node_url + "/v1/registry/search", params=params)
        resp.raise_for_status()
        result = resp.json()
        return result if isinstance(result, list) else []

    def get_service(self, agent_id: str) -> Dict[str, Any]:
        """Return the service listing for *agent_id*.

        Raises:
            AetherNetError: If no listing exists for the agent (HTTP 404).
        """
        return self._get(f"/v1/registry/{agent_id}")

    def list_categories(self) -> Dict[str, int]:
        """Return a mapping of category name to active listing count."""
        return self._get("/v1/registry/categories")

    def deactivate_service(self) -> Dict[str, Any]:
        """Deactivate this node's own service listing.

        Raises:
            AetherNetError: If no listing exists (HTTP 404) or the node's
                agentID could not be resolved (HTTP error on /v1/status).
        """
        st = self.status()
        agent_id = st.get("agent_id", "")
        if not agent_id:
            raise AetherNetError(0, "could not resolve node agent_id from /v1/status")
        return self._delete(f"/v1/registry/{agent_id}")

    # ------------------------------------------------------------------
    # Task marketplace endpoints
    # ------------------------------------------------------------------

    def post_task(
        self,
        title: str,
        description: str = "",
        category: str = "",
        budget: int = 0,
    ) -> Dict[str, Any]:
        """Post a new task to the marketplace.

        The budget is escrowed from the node's own agent balance immediately.

        Args:
            title:       Short task title.
            description: Detailed task description.
            category:    Capability category (e.g. "ml", "nlp", "code").
            budget:      Task budget in micro-AET.

        Returns:
            The created task dict including ``id`` and ``status``.
        """
        return self._post("/v1/tasks", {
            "title": title,
            "description": description,
            "category": category,
            "budget": budget,
        })

    def browse_tasks(
        self,
        status: str = "",
        category: str = "",
        limit: int = 0,
    ) -> List[Dict[str, Any]]:
        """List tasks with optional filters.

        Args:
            status:   Filter by task status (e.g. "open", "claimed").
            category: Filter by category.
            limit:    Maximum number of results; 0 = no limit.

        Returns a list of task dicts.
        """
        params: Dict[str, Any] = {}
        if status:
            params["status"] = status
        if category:
            params["category"] = category
        if limit > 0:
            params["limit"] = limit
        resp = self.session.get(self.node_url + "/v1/tasks", params=params)
        resp.raise_for_status()
        return resp.json()

    def get_task(self, task_id: str) -> Dict[str, Any]:
        """Return a single task by ID."""
        return self._get(f"/v1/tasks/{task_id}")

    def claim_task(self, task_id: str, claimer_id: str = "") -> Dict[str, Any]:
        """Claim an open task.

        Args:
            task_id:    The task to claim.
            claimer_id: The claiming agent's ID; defaults to the node's own identity.

        Returns the updated task dict.
        """
        body: Dict[str, Any] = {}
        if claimer_id:
            body["claimer_id"] = claimer_id
        return self._post(f"/v1/tasks/{task_id}/claim", body)

    def submit_task_result(
        self,
        task_id: str,
        result_hash: Optional[str] = None,
        result_note: Optional[str] = None,
        evidence: Optional["Evidence"] = None,
        claimer_id: str = "",
    ) -> Dict[str, Any]:
        """Submit a result for a claimed task.

        Pass structured *evidence* for quality-scored auto-approval. Legacy
        callers can continue passing *result_hash* alone — both work.

        Args:
            task_id:     The task being worked on.
            result_hash: Content-addressed hash of the deliverable (legacy).
            result_note: Human-readable summary of the work performed.
            evidence:    :class:`Evidence` instance; hash/summary extracted automatically.
            claimer_id:  The claiming agent's ID; defaults to the node's own identity.

        Returns the updated task dict.
        """
        body: Dict[str, Any] = {}
        if evidence is not None:
            body["evidence"] = evidence.to_dict()
            # Provide hash + note at top-level for backward-compat with older nodes.
            body["result_hash"] = evidence.hash
            if evidence.summary:
                body["result_note"] = evidence.summary
            if evidence.output_url:
                body["result_uri"] = evidence.output_url
        else:
            if result_hash:
                body["result_hash"] = result_hash
            if result_note:
                body["result_note"] = result_note
        if claimer_id:
            body["claimer_id"] = claimer_id
        return self._post(f"/v1/tasks/{task_id}/submit", body)

    def approve_task(self, task_id: str, approver_id: str = "") -> Dict[str, Any]:
        """Approve a submitted task, releasing the escrowed budget to the worker.

        Args:
            task_id:     The task to approve.
            approver_id: The approving agent's ID; defaults to the node's own identity.

        Returns the updated task dict (status = "completed").
        """
        body: Dict[str, Any] = {}
        if approver_id:
            body["approver_id"] = approver_id
        return self._post(f"/v1/tasks/{task_id}/approve", body)

    def dispute_task(self, task_id: str, poster_id: str = "") -> Dict[str, Any]:
        """Dispute a submitted task, holding funds in escrow.

        Args:
            task_id:   The task to dispute.
            poster_id: The poster's agent ID; defaults to the node's own identity.

        Returns the updated task dict (status = "disputed").
        """
        body: Dict[str, Any] = {}
        if poster_id:
            body["poster_id"] = poster_id
        return self._post(f"/v1/tasks/{task_id}/dispute", body)

    def cancel_task(self, task_id: str, poster_id: str = "") -> Dict[str, Any]:
        """Cancel an open task and refund the budget to the poster.

        Args:
            task_id:   The open task to cancel.
            poster_id: The poster's agent ID; defaults to the node's own identity.

        Returns the updated task dict (status = "cancelled").
        """
        body: Dict[str, Any] = {}
        if poster_id:
            body["poster_id"] = poster_id
        return self._post(f"/v1/tasks/{task_id}/cancel", body)

    def my_tasks(self, agent_id: str = "") -> List[Dict[str, Any]]:
        """Return all tasks where *agent_id* is poster or claimer.

        Uses ``self.agent_id`` when *agent_id* is omitted.
        """
        aid = agent_id or self.agent_id
        if not aid:
            raise ValueError("agent_id required: pass to AetherNetClient() or my_tasks()")
        return self._get(f"/v1/tasks/agent/{aid}")

    def task_stats(self) -> Dict[str, Any]:
        """Return aggregate marketplace statistics.

        Returns a dict with ``total_tasks``, ``open_tasks``, ``completed_tasks``,
        ``total_budget``, and per-status counts.
        """
        return self._get("/v1/tasks/stats")

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

    def _delete(self, path: str) -> Any:
        resp = self.session.delete(self.node_url + path)
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
