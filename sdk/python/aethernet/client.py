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


# ---------------------------------------------------------------------------
# Ed25519 ↔ X25519 key conversion helpers
# ---------------------------------------------------------------------------

def _ed25519_pub_to_x25519(pub_bytes: bytes) -> bytes:
    """Convert a 32-byte Ed25519 public key to a 32-byte X25519 public key.

    Both key types live on Curve25519 but use different coordinate systems.
    This applies the standard Edwards-y → Montgomery-u mapping:
        u = (1 + y) / (1 − y)  mod p    where  p = 2²⁵⁵ − 19
    """
    p = 2**255 - 19
    # Ed25519 public key is the little-endian encoding of the Edwards y
    # coordinate with the sign bit of x packed into the high bit of byte 31.
    y = int.from_bytes(pub_bytes, "little") & ~(1 << 255)
    u = (1 + y) * pow(1 - y, p - 2, p) % p
    return u.to_bytes(32, "little")


def _ed25519_seed_to_x25519(seed: bytes) -> bytes:
    """Derive a 32-byte X25519 private key from a 32-byte Ed25519 seed.

    The Ed25519 private scalar is SHA-512(seed)[:32] with the standard
    clamping applied (RFC 7748 §5).
    """
    import hashlib

    h = bytearray(hashlib.sha512(seed).digest()[:32])
    h[0] &= 248   # clear bits 0, 1, 2
    h[31] &= 127  # clear bit 7
    h[31] |= 64   # set bit 6
    return bytes(h)


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

    def generate_keypair(self, agent_name: str) -> str:
        """Get or create a persistent Ed25519 keypair for *agent_name*.

        The keypair is stored as a JSON file at
        ``~/.aethernet/keys/{agent_name}.key`` with permissions 0o600.
        On subsequent calls with the same *agent_name* the existing key is
        loaded so the agent retains its identity across restarts.

        Requires ``pip install aethernet-sdk[crypto]`` (``cryptography`` package).

        Args:
            agent_name: Logical agent name, used as the file stem.

        Returns:
            Standard-base64-encoded Ed25519 public key (no newlines), suitable
            for passing as ``public_key_b64`` to ``POST /v1/agents``.

        Raises:
            ImportError: If the ``cryptography`` package is not installed.
        """
        import base64
        import binascii
        import json
        import os

        from cryptography.hazmat.primitives.asymmetric.ed25519 import Ed25519PrivateKey
        from cryptography.hazmat.primitives.serialization import (
            Encoding,
            NoEncryption,
            PrivateFormat,
            PublicFormat,
        )

        keys_dir = os.path.join(os.path.expanduser("~"), ".aethernet", "keys")
        os.makedirs(keys_dir, mode=0o700, exist_ok=True)
        key_path = os.path.join(keys_dir, f"{agent_name}.key")

        if os.path.exists(key_path):
            with open(key_path) as f:
                saved = json.load(f)
            pub_bytes = binascii.unhexlify(saved["public_key"])
        else:
            private_key = Ed25519PrivateKey.generate()
            public_key = private_key.public_key()
            pub_bytes = public_key.public_bytes(Encoding.Raw, PublicFormat.Raw)
            priv_bytes = private_key.private_bytes(Encoding.Raw, PrivateFormat.Raw, NoEncryption())
            saved = {
                "agent_name": agent_name,
                "public_key": binascii.hexlify(pub_bytes).decode(),
                "private_key": binascii.hexlify(priv_bytes).decode(),
            }
            fd = os.open(key_path, os.O_WRONLY | os.O_CREAT | os.O_TRUNC, 0o600)
            with os.fdopen(fd, "w") as f:
                json.dump(saved, f, indent=2)

        return base64.b64encode(pub_bytes).decode()

    def register_with_keypair(
        self,
        agent_name: str,
        capabilities: Optional[List[Dict]] = None,
    ) -> Dict[str, Any]:
        """Register *agent_name* as an independent economic actor with its own keypair.

        On first call, generates a persistent Ed25519 keypair (stored at
        ``~/.aethernet/keys/{agent_name}.key``). On subsequent calls the same
        key is reloaded so the agent retains its identity across restarts.

        Because each unique ``public_key_b64`` is a distinct identity, each
        agent receives its own onboarding allocation from the ecosystem bucket
        rather than sharing the node's allocation.

        Sets ``self.agent_id`` to *agent_name* on success.

        Args:
            agent_name:   Human-readable agent ID (e.g. "research-worker-01").
            capabilities: Optional list of capability dicts.

        Returns:
            Registration dict: ``agent_id``, ``fingerprint_hash``,
            ``onboarding_allocation`` (µAET), ``deposit_address``.
        """
        pub_b64 = self.generate_keypair(agent_name)
        result = self._post("/v1/agents", {
            "agent_id": agent_name,
            "public_key_b64": pub_b64,
            "capabilities": capabilities or [],
        })
        self.agent_id = result.get("agent_id", agent_name)
        return result

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
        # Include from_agent so the server charges the correct account.
        # Without this, the server defaults to the node's own keypair identity.
        if self.agent_id:
            body["from_agent"] = self.agent_id
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
        ``total_burned``, ``treasury_accrued``, ``fee_basis_points``, and
        ``total_generated_value`` (cumulative verified AI output in micro-AET).
        """
        return self._get("/v1/economics")

    # Alias used by the E2E test and docs — same as economics().
    def get_economics(self) -> Dict[str, Any]:
        """Alias for :meth:`economics`."""
        return self.economics()

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
        delivery_method: str = "public",
    ) -> Dict[str, Any]:
        """Post a new task to the marketplace.

        The budget is escrowed from the poster's balance immediately.  When
        ``self.agent_id`` is set the request includes ``poster_id`` so the
        server charges the correct account; otherwise the node's own identity
        is used (single-binary deployments).

        Args:
            title:           Short task title.
            description:     Detailed task description.
            category:        Capability category (e.g. "ml", "nlp", "code").
            budget:          Task budget in micro-AET.
            delivery_method: ``"public"`` (default) — result is plaintext.
                             ``"encrypted"`` — result is ECDH+AES-256-GCM ciphertext
                             that only the poster can decrypt with
                             :meth:`decrypt_from_agent`.

        Returns:
            The created task dict including ``id`` and ``status``.
        """
        body: Dict[str, Any] = {
            "title": title,
            "description": description,
            "category": category,
            "budget": budget,
            "delivery_method": delivery_method,
        }
        if self.agent_id:
            body["poster_id"] = self.agent_id
        return self._post("/v1/tasks", body)

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
            claimer_id: The claiming agent's ID; defaults to ``self.agent_id``.

        Returns the updated task dict.
        """
        body: Dict[str, Any] = {}
        effective_id = claimer_id or self.agent_id
        if effective_id:
            body["claimer_id"] = effective_id
        return self._post(f"/v1/tasks/{task_id}/claim", body)

    def submit_task_result(
        self,
        task_id: str,
        result_hash: Optional[str] = None,
        result_note: Optional[str] = None,
        evidence: Optional["Evidence"] = None,
        claimer_id: str = "",
        result_content: str = "",
        result_encrypted: bool = False,
    ) -> Dict[str, Any]:
        """Submit a result for a claimed task.

        Pass structured *evidence* for quality-scored auto-approval. Legacy
        callers can continue passing *result_hash* alone — both work.

        Args:
            task_id:          The task being worked on.
            result_hash:      Content-addressed hash of the deliverable (legacy).
            result_note:      Human-readable summary of the work performed.
            evidence:         :class:`Evidence` instance; hash/summary extracted automatically.
            claimer_id:       The claiming agent's ID; defaults to the node's own identity.
            result_content:   Full output string (plaintext or ciphertext) delivered to
                              the poster.  For public tasks pass the raw output; for
                              encrypted tasks pass the base64 ciphertext returned by
                              :meth:`encrypt_for_agent`.
            result_encrypted: ``True`` when *result_content* is ciphertext.

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
        if result_content:
            body["result_content"] = result_content
            if result_encrypted:
                body["result_encrypted"] = True
        effective_id = claimer_id or self.agent_id
        if effective_id:
            body["claimer_id"] = effective_id
        return self._post(f"/v1/tasks/{task_id}/submit", body)

    def get_task_result(self, task_id: str) -> Dict[str, Any]:
        """Fetch the result content for a task.

        Returns a dict with ``task_id``, ``status``, ``delivery_method``,
        ``result_content``, and ``result_encrypted``.

        For public tasks ``result_content`` is the raw output string.
        For encrypted tasks ``result_content`` is base64 ciphertext — call
        :meth:`decrypt_from_agent` to recover the plaintext.
        """
        return self._get(f"/v1/tasks/result/{task_id}")

    def encrypt_for_agent(self, agent_id: str, plaintext: str) -> str:
        """Encrypt *plaintext* so that only *agent_id* can decrypt it.

        Performs ephemeral ECDH key agreement using the recipient's Ed25519
        public key (converted to X25519 Montgomery form) and encrypts with
        AES-256-GCM.  The result is a base64-encoded blob:
        ``ephemeral_pubkey(32 bytes) || iv(12 bytes) || ciphertext+tag``.

        Requires the ``cryptography`` package (``pip install aethernet-sdk``).

        Args:
            agent_id:  The target agent whose public key is fetched from the node.
            plaintext: UTF-8 string to encrypt.

        Returns:
            Base64-encoded ciphertext string.
        """
        import base64
        import os

        from cryptography.hazmat.primitives.asymmetric.x25519 import (
            X25519PrivateKey,
            X25519PublicKey,
        )
        from cryptography.hazmat.primitives.ciphers.aead import AESGCM
        from cryptography.hazmat.primitives.hashes import SHA256
        from cryptography.hazmat.primitives.kdf.hkdf import HKDF

        profile = self._get(f"/v1/agents/{agent_id}")
        # Go serialises []byte as standard base64 in JSON.
        pub_b64 = profile.get("public_key", "")
        if not pub_b64:
            raise ValueError(f"agent {agent_id!r} has no public_key in registry")
        pub_bytes = base64.b64decode(pub_b64)
        if len(pub_bytes) != 32:
            raise ValueError(f"expected 32-byte Ed25519 public key, got {len(pub_bytes)}")

        x25519_pub_bytes = _ed25519_pub_to_x25519(pub_bytes)
        recipient = X25519PublicKey.from_public_bytes(x25519_pub_bytes)

        ephemeral_priv = X25519PrivateKey.generate()
        ephemeral_pub_bytes = ephemeral_priv.public_key().public_bytes_raw()
        shared_secret = ephemeral_priv.exchange(recipient)

        key = HKDF(algorithm=SHA256(), length=32, salt=None, info=b"aethernet-encrypt").derive(shared_secret)
        iv = os.urandom(12)
        ciphertext = AESGCM(key).encrypt(iv, plaintext.encode(), None)
        return base64.b64encode(ephemeral_pub_bytes + iv + ciphertext).decode()

    def decrypt_from_agent(self, ciphertext_b64: str) -> str:
        """Decrypt a ciphertext produced by :meth:`encrypt_for_agent`.

        Loads the local agent's Ed25519 private key from
        ``~/.aethernet/keys/{agent_id}.key``, converts it to X25519, and
        decrypts the AES-256-GCM payload.

        Requires the ``cryptography`` package.

        Args:
            ciphertext_b64: Base64 blob returned by :meth:`encrypt_for_agent`.

        Returns:
            Decrypted UTF-8 plaintext string.

        Raises:
            ValueError: When no keypair is found or decryption fails.
        """
        import base64
        import binascii
        import json
        import os

        from cryptography.hazmat.primitives.asymmetric.x25519 import (
            X25519PrivateKey,
            X25519PublicKey,
        )
        from cryptography.hazmat.primitives.ciphers.aead import AESGCM
        from cryptography.hazmat.primitives.hashes import SHA256
        from cryptography.hazmat.primitives.kdf.hkdf import HKDF

        agent_name = self.agent_id
        if not agent_name:
            raise ValueError("agent_id not set on client")
        key_path = os.path.expanduser(f"~/.aethernet/keys/{agent_name}.key")
        if not os.path.exists(key_path):
            raise ValueError(f"no keypair found at {key_path}")
        with open(key_path) as f:
            saved = json.load(f)
        priv_bytes = binascii.unhexlify(saved["private_key"])

        x25519_priv_bytes = _ed25519_seed_to_x25519(priv_bytes)
        my_priv = X25519PrivateKey.from_private_bytes(x25519_priv_bytes)

        data = base64.b64decode(ciphertext_b64)
        if len(data) < 32 + 12 + 16:
            raise ValueError("ciphertext blob is too short")
        ephemeral_pub = X25519PublicKey.from_public_bytes(data[:32])
        iv = data[32:44]
        ciphertext = data[44:]

        shared_secret = my_priv.exchange(ephemeral_pub)
        key = HKDF(algorithm=SHA256(), length=32, salt=None, info=b"aethernet-encrypt").derive(shared_secret)
        try:
            plaintext = AESGCM(key).decrypt(iv, ciphertext, None)
        except Exception as e:
            raise ValueError(f"decryption failed: {e}") from e
        return plaintext.decode()

    def approve_task(self, task_id: str, approver_id: str = "") -> Dict[str, Any]:
        """Approve a submitted task, releasing the escrowed budget to the worker.

        Args:
            task_id:     The task to approve.
            approver_id: The approving agent's ID; defaults to ``self.agent_id``.

        Returns the updated task dict (status = "completed").
        """
        body: Dict[str, Any] = {}
        effective_id = approver_id or self.agent_id
        if effective_id:
            body["approver_id"] = effective_id
        return self._post(f"/v1/tasks/{task_id}/approve", body)

    def dispute_task(self, task_id: str, poster_id: str = "") -> Dict[str, Any]:
        """Dispute a submitted task, holding funds in escrow.

        Args:
            task_id:   The task to dispute.
            poster_id: The poster's agent ID; defaults to ``self.agent_id``.

        Returns the updated task dict (status = "disputed").
        """
        body: Dict[str, Any] = {}
        effective_id = poster_id or self.agent_id
        if effective_id:
            body["poster_id"] = effective_id
        return self._post(f"/v1/tasks/{task_id}/dispute", body)

    def cancel_task(self, task_id: str, poster_id: str = "") -> Dict[str, Any]:
        """Cancel an open task and refund the budget to the poster.

        Args:
            task_id:   The open task to cancel.
            poster_id: The poster's agent ID; defaults to ``self.agent_id``.

        Returns the updated task dict (status = "cancelled").
        """
        body: Dict[str, Any] = {}
        effective_id = poster_id or self.agent_id
        if effective_id:
            body["poster_id"] = effective_id
        return self._post(f"/v1/tasks/{task_id}/cancel", body)

    def my_tasks(self, agent_id: str = "") -> List[Dict[str, Any]]:
        """Return all tasks involving *agent_id*: posted, claimed, or routed.

        Calls ``/v1/tasks/agent/{agent_id}`` for tasks where the agent is
        poster or claimer, then merges any open tasks where
        ``routed_to == agent_id`` from ``/v1/tasks?status=open``.  The merged
        list lets callers see tasks assigned by the router before they are
        claimed (the router sets ``routed_to`` while leaving status as
        ``"open"``; only after ``ClaimTask`` does the agent appear as claimer).

        Uses ``self.agent_id`` when *agent_id* is omitted.
        """
        aid = agent_id or self.agent_id
        if not aid:
            raise ValueError("agent_id required: pass to AetherNetClient() or my_tasks()")
        # Tasks where agent is already poster or claimer.
        result: List[Dict[str, Any]] = self._get(f"/v1/tasks/agent/{aid}")
        seen = {t["id"] for t in result}
        # Also surface open tasks routed to this agent.  The router sets
        # routed_to without changing status, so /v1/tasks/agent won't include
        # them until after ClaimTask is called.
        try:
            open_tasks: List[Dict[str, Any]] = self._get("/v1/tasks?status=open")
            for task in open_tasks:
                if task.get("routed_to") == aid and task["id"] not in seen:
                    result.append(task)
                    seen.add(task["id"])
        except Exception:
            pass  # best-effort — fall back to agent-tasks only
        return result

    def task_stats(self) -> Dict[str, Any]:
        """Return aggregate marketplace statistics.

        Returns a dict with ``total_tasks``, ``open_tasks``, ``completed_tasks``,
        ``total_budget``, and per-status counts.
        """
        return self._get("/v1/tasks/stats")

    # ------------------------------------------------------------------
    # Reputation endpoints
    # ------------------------------------------------------------------

    def get_reputation(self, agent_id: str = "") -> Dict[str, Any]:
        """Return the full category-level reputation profile for *agent_id*.

        Returns a dict with ``overall_score``, ``total_completed``, ``total_failed``,
        ``total_earned``, ``top_category``, ``member_since``, and a ``categories``
        mapping each category name to its record (tasks, avg_score, avg_delivery_secs, …).

        Uses ``self.agent_id`` when *agent_id* is omitted.
        """
        aid = agent_id or self.agent_id
        if not aid:
            raise ValueError("agent_id required: pass to AetherNetClient() or get_reputation()")
        return self._get(f"/v1/agents/{aid}/reputation")

    # ------------------------------------------------------------------
    # Discovery endpoint
    # ------------------------------------------------------------------

    def discover(
        self,
        query: str = "",
        category: str = "",
        max_budget: int = 0,
        min_reputation: float = 0.0,
        limit: int = 0,
    ) -> List[Dict[str, Any]]:
        """Find agents matching a task description using capability-aware ranking.

        Returns agents ranked by a composite score of relevance, reputation,
        completion rate, and price efficiency.

        Args:
            query:          Natural-language task description for relevance scoring.
            category:       Optional category filter (e.g. "writing", "code", "ml").
            max_budget:     Upper price limit in micro-AET; 0 = no limit.
            min_reputation: Minimum overall reputation score (0–100); 0 = no filter.
            limit:          Maximum results to return; 0 = no limit.

        Returns a list of match dicts with ``agent_id``, ``service_name``,
        ``category``, ``price_aet``, ``relevance_score``, ``reputation_score``,
        ``completion_rate``, ``tasks_completed``, ``avg_delivery_secs``, and
        ``overall_rank``.
        """
        params: Dict[str, Any] = {}
        if query:
            params["q"] = query
        if category:
            params["category"] = category
        if max_budget > 0:
            params["max_budget"] = max_budget
        if min_reputation > 0:
            params["min_reputation"] = min_reputation
        if limit > 0:
            params["limit"] = limit
        resp = self.session.get(self.node_url + "/v1/discover", params=params)
        if resp.status_code >= 400:
            try:
                err = resp.json().get("error", resp.text)
            except Exception:
                err = resp.text
            raise AetherNetError(resp.status_code, err)
        result = resp.json()
        return result if isinstance(result, list) else []

    def create_subtask(
        self,
        parent_task_id: str,
        title: str,
        description: str = "",
        category: str = "",
        budget: int = 0,
        claimer_id: str = "",
    ) -> Dict[str, Any]:
        """Create a child task under a claimed parent task.

        Only the current claimer of the parent task may call this. The subtask
        budget is deducted from the parent's remaining budget.

        Args:
            parent_task_id: ID of the claimed parent task.
            title:          Short title for the subtask.
            description:    Detailed description of the subtask.
            category:       Capability category (e.g. "ml", "code").
            budget:         Subtask budget in micro-AET (must not exceed parent remainder).
            claimer_id:     The claiming agent's ID; defaults to the node's own identity.

        Returns the created subtask dict including ``id``, ``parent_task_id``,
        and ``is_subtask: true``.
        """
        body: Dict[str, Any] = {
            "title": title,
            "description": description,
            "category": category,
            "budget": budget,
        }
        effective_id = claimer_id or self.agent_id
        if effective_id:
            body["claimer_id"] = effective_id
        return self._post(f"/v1/tasks/{parent_task_id}/subtask", body)

    def get_subtasks(self, task_id: str) -> List[Dict[str, Any]]:
        """Return all child tasks of the given parent task.

        Args:
            task_id: The parent task ID.

        Returns a list of subtask dicts (may be empty if no subtasks exist).
        """
        return self._get(f"/v1/tasks/subtasks/{task_id}")

    # ------------------------------------------------------------------
    # Autonomous routing endpoints
    # ------------------------------------------------------------------

    def register_for_routing(
        self,
        categories: List[str],
        tags: Optional[List[str]] = None,
        description: str = "",
        price_per_task: int = 0,
        max_concurrent: int = 1,
        webhook_url: str = "",
        webhook_secret: str = "",
    ) -> Dict[str, Any]:
        """Register this agent for autonomous task routing.

        Once registered, the router will automatically push open tasks that
        match the agent's categories to it, calling the agent's webhook
        (if configured) when a match is made.

        Args:
            categories:     List of task categories this agent handles.
            tags:           Optional keyword tags for finer-grained matching.
            description:    Human-readable description of agent capabilities.
            price_per_task: Maximum price per task in micro-AET (0 = any budget).
            max_concurrent: Maximum tasks to handle simultaneously.
            webhook_url:    URL the router should POST task-assignment notices to.
            webhook_secret: HMAC-SHA256 secret used to sign webhook payloads.

        Returns the registration confirmation dict.
        """
        if not self.agent_id:
            raise ValueError("agent_id required — set via AetherNetClient(agent_id=...)")
        body: Dict[str, Any] = {
            "agent_id": self.agent_id,
            "categories": categories,
            "price_per_task": price_per_task,
            "max_concurrent": max_concurrent,
        }
        if tags:
            body["tags"] = tags
        if description:
            body["description"] = description
        if webhook_url:
            body["webhook_url"] = webhook_url
        if webhook_secret:
            body["webhook_secret"] = webhook_secret
        return self._post("/v1/router/register", body)

    def unregister_from_routing(self, agent_id: str = "") -> Dict[str, Any]:
        """Remove this agent from the autonomous routing pool.

        Args:
            agent_id: Agent to unregister; defaults to ``self.agent_id``.

        Returns the confirmation dict.
        """
        aid = agent_id or self.agent_id
        if not aid:
            raise ValueError("agent_id required")
        return self._delete(f"/v1/router/register/{aid}")

    def set_availability(self, available: bool = True, agent_id: str = "") -> Dict[str, Any]:
        """Toggle whether the router will assign tasks to this agent.

        Args:
            available: True to accept routed tasks, False to pause routing.
            agent_id:  Agent to update; defaults to ``self.agent_id``.

        Returns the updated availability dict.
        """
        aid = agent_id or self.agent_id
        if not aid:
            raise ValueError("agent_id required")
        return self._put(f"/v1/router/availability/{aid}", {"available": available})

    def get_routing_stats(self) -> Dict[str, Any]:
        """Return aggregate statistics from the autonomous routing engine.

        Returns a dict with ``registered_agents``, ``available_agents``,
        ``total_routed``, and ``pending_tasks``.
        """
        return self._get("/v1/router/stats")

    def get_category_rankings(self, category: str, limit: int = 10) -> List[Dict[str, Any]]:
        """Return agents ranked by performance in *category*.

        Sorted descending by ``tasks_completed × avg_score``.

        Args:
            category: Category to rank agents in (e.g. "writing", "code", "ml").
            limit:    Maximum number of results. Defaults to 10.

        Returns a list of reputation profile dicts.
        """
        params = {"category": category, "limit": limit}
        resp = self.session.get(self.node_url + "/v1/reputation/rankings", params=params)
        if resp.status_code >= 400:
            try:
                err = resp.json().get("error", resp.text)
            except Exception:
                err = resp.text
            raise AetherNetError(resp.status_code, err)
        return resp.json()

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

    def _put(self, path: str, body: Dict) -> Any:
        resp = self.session.put(self.node_url + path, json=body)
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
