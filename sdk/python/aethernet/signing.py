"""AETHERNET-TX-V1 transaction signing for Python clients.

This module produces signatures that are verified by the Go server's
VerifyTransaction function in internal/auth/txverify.go. The canonical
bytes MUST match the Go implementation exactly.
"""

import hashlib
import json
import os
import time

from nacl.signing import SigningKey, VerifyKey
from nacl.encoding import RawEncoder


def canonicalize_json(data: dict) -> bytes:
    """RFC 8785-compatible JSON canonicalization for AetherNet payloads.

    Uses json.dumps with sort_keys=True and compact separators.
    Produces identical output to Go's CanonicalizeJSON for the same input.
    """
    return json.dumps(data, sort_keys=True, separators=(",", ":"), ensure_ascii=False).encode("utf-8")


def canonical_bytes(data: dict) -> bytes:
    """Return the raw JCS-canonicalized JSON bytes for a dict.

    This is the same canonicalization used for AETHERNET-TX-V1 signing,
    evidence hashes, and TxID computation. Useful for signature verification
    where you need the raw bytes before hashing.

    Args:
        data: Any JSON-serializable dict

    Returns:
        UTF-8 encoded canonical JSON bytes
    """
    return canonicalize_json(data)


def canonical_hash(data: dict) -> str:
    """Compute SHA-256 hash of the JCS-canonicalized JSON representation.

    This is the same canonicalization used for AETHERNET-TX-V1 signing,
    evidence hashes, and TxID computation. Third-party verifiers should
    use this function to ensure hash compatibility with the protocol.

    Args:
        data: Any JSON-serializable dict

    Returns:
        Hex-encoded SHA-256 hash of the canonical bytes
    """
    return hashlib.sha256(canonicalize_json(data)).hexdigest()


def build_sign_bytes(
    chain_id: str,
    actor: str,
    method: str,
    path: str,
    body: dict = None,
    created_at: int = None,
    expires_at: int = None,
    nonce: str = None,
) -> bytes:
    """Build canonical sign bytes for AETHERNET-TX-V1.

    Returns JCS-canonicalized JSON of the transaction envelope.
    Must produce identical bytes to Go's (*Transaction).SignBytes().
    """
    if body is None:
        body_bytes = b""
    else:
        body_bytes = canonicalize_json(body)

    body_sha256 = hashlib.sha256(body_bytes).hexdigest()

    tx = {
        "version": "AETHERNET-TX-V1",
        "chain_id": chain_id,
        "actor": actor,
        "method": method,
        "path": path,
        "body_sha256": body_sha256,
        "created_at": created_at,
        "expires_at": expires_at,
        "nonce": nonce,
    }
    return canonicalize_json(tx)


def compute_txid(sign_bytes: bytes) -> str:
    """Compute TxID = SHA-256(sign_bytes) as hex string."""
    return hashlib.sha256(sign_bytes).hexdigest()


def sign_request(
    signing_key: SigningKey,
    method: str,
    path: str,
    body: dict = None,
    chain_id: str = "aethernet-testnet-1",
    lifetime_secs: int = 120,
) -> dict:
    """Sign a request with AETHERNET-TX-V1. Returns dict of HTTP headers."""
    actor = signing_key.verify_key.encode(encoder=RawEncoder).hex()
    now = int(time.time())
    nonce = os.urandom(16).hex()

    sb = build_sign_bytes(
        chain_id=chain_id,
        actor=actor,
        method=method.upper(),
        path=path,
        body=body,
        created_at=now,
        expires_at=now + lifetime_secs,
        nonce=nonce,
    )

    signed = signing_key.sign(sb, encoder=RawEncoder)
    signature = signed.signature.hex()

    return {
        "X-AetherNet-Version": "AETHERNET-TX-V1",
        "X-AetherNet-Chain-ID": chain_id,
        "X-AetherNet-Actor": actor,
        "X-AetherNet-Created": str(now),
        "X-AetherNet-Expires": str(now + lifetime_secs),
        "X-AetherNet-Nonce": nonce,
        "X-AetherNet-Signature": signature,
    }


# ── Key Management ────────────────────────────────────────────────────────────


def generate_keypair():
    """Generate an Ed25519 keypair. Returns (SigningKey, hex_public_key)."""
    sk = SigningKey.generate()
    pubkey_hex = sk.verify_key.encode(encoder=RawEncoder).hex()
    return sk, pubkey_hex


def save_keypair(signing_key: SigningKey, path: str):
    """Save a signing key to a JSON file."""
    seed = signing_key.encode(encoder=RawEncoder).hex()
    pubkey = signing_key.verify_key.encode(encoder=RawEncoder).hex()
    data = {"seed": seed, "public_key": pubkey}
    os.makedirs(os.path.dirname(path) or ".", exist_ok=True)
    fd = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_TRUNC, 0o600)
    with os.fdopen(fd, "w") as f:
        json.dump(data, f, indent=2)


def load_keypair(path: str) -> SigningKey:
    """Load a signing key from a JSON file."""
    with open(path) as f:
        data = json.load(f)
    seed = bytes.fromhex(data["seed"])
    return SigningKey(seed)


def get_or_create_keypair(name: str) -> SigningKey:
    """Load or create a keypair at ~/.aethernet/keys/{name}.json."""
    keys_dir = os.path.join(os.path.expanduser("~"), ".aethernet", "keys")
    os.makedirs(keys_dir, mode=0o700, exist_ok=True)
    path = os.path.join(keys_dir, f"{name}.json")
    if os.path.exists(path):
        return load_keypair(path)
    sk, _ = generate_keypair()
    save_keypair(sk, path)
    return sk
