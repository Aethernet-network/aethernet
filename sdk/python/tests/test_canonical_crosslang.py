"""Cross-language canonicalization test vectors.

These vectors are ground truth produced by Go's auth.CanonicalizeJSON.
Python's canonicalize_json must produce identical bytes for every case.
"""

import hashlib

import pytest

from aethernet.signing import canonical_bytes, canonical_hash, canonicalize_json


# Each tuple: (name, input_dict, expected_canonical_bytes, expected_sha256_hex)
VECTORS = [
    (
        "simple_key_sorting",
        {"b": 2, "a": 1},
        b'{"a":1,"b":2}',
        "43258cff783fe7036d8a43033f830adfc60ec037382473548ac742b888292777",
    ),
    (
        "nested_recursive_sorting",
        {"outer": {"z": 1, "a": 2}, "inner": [3, 1, 2]},
        b'{"inner":[3,1,2],"outer":{"a":2,"z":1}}',
        "ab9eb50b92d069549a6e6b1e93380f070804e6619e0a5ccc62cd8717dfef71ad",
    ),
    (
        "integer_zero",
        {"val": 0},
        b'{"val":0}',
        "3d327872b987fdbbdece95d0d7bec019bb18cf392356e7e155a3606a1415070a",
    ),
    (
        "integer_negative",
        {"val": -1},
        b'{"val":-1}',
        "bd78e10a2d0a9da9baed04b8cc9779b555f2d40c42115ca3d4e0bd1cad70a373",
    ),
    (
        "integer_large",
        {"val": 1000000000000},
        b'{"val":1000000000000}',
        "e4064fa9f5d6cc73cdf030ef71ab5ca9ac578375ddbec4f62b71e0f18ef7190b",
    ),
    (
        "empty_object",
        {},
        b"{}",
        "44136fa355b3678a1146ad16f7e8649e94fb4fc21fe77e8310c060f61caaff8a",
    ),
    (
        "empty_string_value",
        {"a": ""},
        b'{"a":""}',
        "258555fe010df3da34b3920945d0fbc59cebbcff1878bfc2e9206f0f495d81b9",
    ),
    (
        "empty_array_value",
        {"a": []},
        b'{"a":[]}',
        "50e8660084976a10f0b3b9b3a6352d5881cbd219b5587a26224971a60ff2cc55",
    ),
    (
        "unicode_trademark",
        {"name": "AetherNet\u2122"},
        "AetherNet\u2122".encode("utf-8").join([b'{"name":"', b'"}']),
        "55c82259eef59b017eb24c59977a1cb0c587643a417b3a834dbea6e3758bb90d",
    ),
    (
        "boolean_and_null",
        {"flag": True, "nothing": None},
        b'{"flag":true,"nothing":null}',
        "aecd989457f6d1603fe9edf7a9908fc933c9948ecd649e903e5091a13066eab3",
    ),
    (
        "tx_v1_payload",
        {
            "version": "AETHERNET-TX-V1",
            "chain_id": "aethernet-testnet-1",
            "actor": "abc123def456",
            "method": "POST",
            "path": "/v1/agents/register",
            "body_sha256": "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
            "created_at": 1700000000,
            "expires_at": 1700000120,
            "nonce": "deadbeef01234567",
        },
        b'{"actor":"abc123def456","body_sha256":"e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855","chain_id":"aethernet-testnet-1","created_at":1700000000,"expires_at":1700000120,"method":"POST","nonce":"deadbeef01234567","path":"/v1/agents/register","version":"AETHERNET-TX-V1"}',
        "25c54f03e0a7e80a53bf0fd291b4d3e720e6d9f7049091992638ebffbe9100cf",
    ),
]


class TestCanonicalBytesVectors:
    """Verify canonicalize_json produces bytes identical to Go's CanonicalizeJSON."""

    @pytest.mark.parametrize("name,data,expected_bytes,_", VECTORS, ids=[v[0] for v in VECTORS])
    def test_canonical_bytes_match(self, name, data, expected_bytes, _):
        result = canonicalize_json(data)
        assert result == expected_bytes, (
            f"[{name}] canonical bytes mismatch:\n"
            f"  got:    {result!r}\n"
            f"  expect: {expected_bytes!r}"
        )

    @pytest.mark.parametrize("name,data,_,expected_hash", VECTORS, ids=[v[0] for v in VECTORS])
    def test_canonical_hash_match(self, name, data, _, expected_hash):
        result = hashlib.sha256(canonicalize_json(data)).hexdigest()
        assert result == expected_hash, (
            f"[{name}] hash mismatch:\n"
            f"  got:    {result}\n"
            f"  expect: {expected_hash}"
        )


class TestPublicAPI:
    """Verify the public canonical_bytes and canonical_hash utilities."""

    def test_canonical_bytes_returns_raw(self):
        data = {"b": 2, "a": 1}
        assert canonical_bytes(data) == b'{"a":1,"b":2}'

    def test_canonical_hash_returns_hex(self):
        data = {"b": 2, "a": 1}
        expected = "43258cff783fe7036d8a43033f830adfc60ec037382473548ac742b888292777"
        assert canonical_hash(data) == expected

    def test_canonical_hash_matches_manual(self):
        data = {"flag": True, "nothing": None}
        manual = hashlib.sha256(canonical_bytes(data)).hexdigest()
        assert canonical_hash(data) == manual

    def test_importable_from_top_level(self):
        from aethernet import canonical_bytes as cb, canonical_hash as ch
        assert callable(cb)
        assert callable(ch)
        assert ch({"x": 1}) == hashlib.sha256(cb({"x": 1})).hexdigest()
