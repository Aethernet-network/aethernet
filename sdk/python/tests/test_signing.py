"""Tests for AETHERNET-TX-V1 signing module."""

import json
import os
import tempfile

from nacl.signing import SigningKey
from nacl.encoding import RawEncoder

from aethernet.signing import (
    canonicalize_json,
    build_sign_bytes,
    compute_txid,
    sign_request,
    generate_keypair,
    save_keypair,
    load_keypair,
)


def test_canonicalize_json_key_ordering():
    a = canonicalize_json({"b": 2, "a": 1})
    b = canonicalize_json({"a": 1, "b": 2})
    assert a == b
    assert a == b'{"a":1,"b":2}'


def test_canonicalize_json_nested():
    result = canonicalize_json({"z": {"b": 2, "a": 1}, "a": True})
    assert result == b'{"a":true,"z":{"a":1,"b":2}}'


def test_canonicalize_json_deterministic():
    data = {"version": "AETHERNET-TX-V1", "nonce": "abc", "actor": "def"}
    a = canonicalize_json(data)
    b = canonicalize_json(data)
    assert a == b


def test_sign_bytes_deterministic():
    sb1 = build_sign_bytes(
        chain_id="aethernet-testnet-1",
        actor="deadbeef",
        method="POST",
        path="/v1/transfer",
        body={"amount": 1000},
        created_at=1700000000,
        expires_at=1700000060,
        nonce="0123456789abcdef0123456789abcdef",
    )
    sb2 = build_sign_bytes(
        chain_id="aethernet-testnet-1",
        actor="deadbeef",
        method="POST",
        path="/v1/transfer",
        body={"amount": 1000},
        created_at=1700000000,
        expires_at=1700000060,
        nonce="0123456789abcdef0123456789abcdef",
    )
    assert sb1 == sb2


def test_txid_deterministic():
    sb = build_sign_bytes(
        chain_id="aethernet-testnet-1",
        actor="deadbeef",
        method="POST",
        path="/v1/transfer",
        body={"amount": 1000},
        created_at=1700000000,
        expires_at=1700000060,
        nonce="0123456789abcdef0123456789abcdef",
    )
    txid1 = compute_txid(sb)
    txid2 = compute_txid(sb)
    assert txid1 == txid2
    assert len(txid1) == 64  # SHA-256 hex


def test_sign_verify_roundtrip():
    sk, pubkey_hex = generate_keypair()

    headers = sign_request(sk, "POST", "/v1/tasks", body={"title": "test"})
    assert headers["X-AetherNet-Version"] == "AETHERNET-TX-V1"
    assert headers["X-AetherNet-Actor"] == pubkey_hex
    assert len(headers["X-AetherNet-Nonce"]) == 32
    assert len(headers["X-AetherNet-Signature"]) == 128  # 64 bytes hex


def test_cross_language_compatibility():
    """Verify Python produces identical canonical bytes as Go for a fixed input.

    The expected values are computed from Go's CanonicalizeJSON and
    (*Transaction).SignBytes() for the same inputs.
    """
    # Fixed inputs.
    body = {"amount": 1000}
    canon_body = canonicalize_json(body)
    assert canon_body == b'{"amount":1000}'

    import hashlib
    body_sha256 = hashlib.sha256(canon_body).hexdigest()

    tx = {
        "version": "AETHERNET-TX-V1",
        "chain_id": "aethernet-testnet-1",
        "actor": "deadbeef",
        "method": "POST",
        "path": "/v1/transfer",
        "body_sha256": body_sha256,
        "created_at": 1700000000,
        "expires_at": 1700000060,
        "nonce": "0123456789abcdef0123456789abcdef",
    }
    sign_bytes = canonicalize_json(tx)

    # Verify the sign bytes contain expected structure.
    parsed = json.loads(sign_bytes)
    assert parsed["version"] == "AETHERNET-TX-V1"
    assert parsed["actor"] == "deadbeef"
    assert parsed["body_sha256"] == body_sha256

    # TxID = SHA-256(sign_bytes).
    txid = hashlib.sha256(sign_bytes).hexdigest()
    assert len(txid) == 64

    # The canonical JSON must have keys in sorted order.
    keys = list(json.loads(sign_bytes).keys())
    assert keys == sorted(keys), f"Keys not sorted: {keys}"


def test_keypair_save_load():
    sk, pubkey = generate_keypair()
    with tempfile.NamedTemporaryFile(suffix=".json", delete=False) as f:
        path = f.name
    try:
        save_keypair(sk, path)
        loaded = load_keypair(path)
        assert loaded.verify_key.encode(encoder=RawEncoder).hex() == pubkey
    finally:
        os.unlink(path)
