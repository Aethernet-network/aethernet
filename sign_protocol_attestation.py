#!/usr/bin/env python3
"""
AetherNet Protocol Attestation — Offline Signing

Hashes the current state of the AetherNet codebase and signs a
timestamped manifest with an Ed25519 key. This creates a cryptographic
proof that this exact codebase existed at this exact time, independent
of any third-party service.

Usage:
    python3 sign_protocol_attestation.py [--key-path PATH] [--repo-path PATH]

Output:
    protocol-attestation-{timestamp}.json — signed manifest
"""

import argparse
import hashlib
import json
import os
import subprocess
import time
from datetime import datetime, timezone
from pathlib import Path

try:
    from nacl.signing import SigningKey
    from nacl.encoding import HexEncoder
except ImportError:
    print("ERROR: pynacl required. Install with: pip3 install pynacl")
    exit(1)


def hash_file(path: str) -> str:
    """SHA-256 hash of a single file."""
    h = hashlib.sha256()
    with open(path, "rb") as f:
        for chunk in iter(lambda: f.read(8192), b""):
            h.update(chunk)
    return h.hexdigest()


def hash_directory(repo_path: str) -> dict:
    """Hash all tracked Go and Python source files in the repo."""
    result = subprocess.run(
        ["git", "ls-files", "--cached"],
        cwd=repo_path, capture_output=True, text=True
    )
    if result.returncode != 0:
        raise RuntimeError(f"git ls-files failed: {result.stderr}")

    file_hashes = {}
    extensions = {".go", ".py", ".mod", ".sum", ".toml", ".yaml", ".yml", ".json", ".md"}

    for rel_path in sorted(result.stdout.strip().split("\n")):
        if not rel_path:
            continue
        ext = Path(rel_path).suffix
        if ext in extensions:
            full_path = os.path.join(repo_path, rel_path)
            if os.path.isfile(full_path):
                file_hashes[rel_path] = hash_file(full_path)

    return file_hashes


def get_git_info(repo_path: str) -> dict:
    """Get current commit hash and branch."""
    commit = subprocess.run(
        ["git", "rev-parse", "HEAD"],
        cwd=repo_path, capture_output=True, text=True
    ).stdout.strip()

    branch = subprocess.run(
        ["git", "rev-parse", "--abbrev-ref", "HEAD"],
        cwd=repo_path, capture_output=True, text=True
    ).stdout.strip()

    # Count lines of Go code
    go_lines = subprocess.run(
        ["bash", "-c", "find . -name '*.go' -not -path './vendor/*' | xargs wc -l | tail -1"],
        cwd=repo_path, capture_output=True, text=True
    ).stdout.strip()

    # Count packages
    packages = subprocess.run(
        ["bash", "-c", "find . -name '*.go' -not -path './vendor/*' -exec dirname {} \\; | sort -u | wc -l"],
        cwd=repo_path, capture_output=True, text=True
    ).stdout.strip()

    return {
        "commit": commit,
        "branch": branch,
        "go_lines_raw": go_lines,
        "package_count_raw": packages,
    }


def load_signing_key(key_path: str) -> SigningKey:
    """Load an Ed25519 signing key from an AetherNet keystore file."""
    with open(key_path) as f:
        data = json.load(f)

    if "seed" in data:
        return SigningKey(bytes.fromhex(data["seed"]))
    elif "private_key" in data:
        return SigningKey(bytes.fromhex(data["private_key"]))
    else:
        raise ValueError(f"Keystore at {key_path} has no 'seed' or 'private_key' field")


def build_attestation(repo_path: str) -> dict:
    """Build the unsigned attestation manifest."""
    now = datetime.now(timezone.utc)
    git_info = get_git_info(repo_path)
    file_hashes = hash_directory(repo_path)

    # Compute a single root hash over all file hashes (deterministic)
    root_hasher = hashlib.sha256()
    for path in sorted(file_hashes.keys()):
        root_hasher.update(f"{path}:{file_hashes[path]}\n".encode())
    root_hash = root_hasher.hexdigest()

    # Key architectural files — hash individually for targeted verification
    key_files = [
        "cmd/node/main.go",
        "internal/ocs/engine.go",
        "internal/consensus/voting.go",
        "internal/dag/dag.go",
        "internal/event/event.go",
        "internal/ledger/transfer.go",
        "internal/ledger/generation.go",
        "internal/settlement/applicator.go",
        "internal/auth/transaction.go",
        "internal/validatorlifecycle/reducer.go",
        "internal/localpub/publisher.go",
        "internal/trajectory/service.go",
        "internal/network/node.go",
        "internal/tasks/manager.go",
    ]

    key_file_hashes = {}
    for kf in key_files:
        if kf in file_hashes:
            key_file_hashes[kf] = file_hashes[kf]

    return {
        "attestation_version": 1,
        "protocol": "AetherNet",
        "protocol_version": "0.1.0-testnet",
        "timestamp_utc": now.isoformat(),
        "timestamp_unix": int(now.timestamp()),
        "git_commit": git_info["commit"],
        "git_branch": git_info["branch"],
        "codebase_stats": {
            "go_lines": git_info["go_lines_raw"],
            "packages": git_info["package_count_raw"],
            "tracked_source_files": len(file_hashes),
        },
        "root_hash": root_hash,
        "key_file_hashes": key_file_hashes,
        "claim": (
            "This attestation certifies that the AetherNet protocol codebase, "
            "as identified by the root hash and individual file hashes above, "
            "existed in this exact form at the stated timestamp. The attestation "
            "is signed with an Ed25519 key from the AetherNet testnet validator set."
        ),
    }


def sign_attestation(attestation: dict, signing_key: SigningKey) -> dict:
    """Sign the attestation and return the complete signed document."""
    # Canonical JSON for signing
    canonical = json.dumps(attestation, sort_keys=True, separators=(",", ":")).encode()
    canonical_hash = hashlib.sha256(canonical).hexdigest()

    signed = signing_key.sign(canonical)
    signature = signed.signature.hex()
    public_key = signing_key.verify_key.encode(encoder=HexEncoder).decode()

    return {
        "attestation": attestation,
        "signature": {
            "algorithm": "Ed25519",
            "public_key": public_key,
            "canonical_sha256": canonical_hash,
            "signature_hex": signature,
        },
    }


def main():
    parser = argparse.ArgumentParser(description="Sign AetherNet protocol attestation")
    parser.add_argument(
        "--key-path",
        default=os.path.expanduser("~/.aethernet/keys/aethernet-quickstart.json"),
        help="Path to Ed25519 keystore file",
    )
    parser.add_argument(
        "--repo-path",
        default=os.path.expanduser("~/aethernet"),
        help="Path to AetherNet repo",
    )
    args = parser.parse_args()

    print("AetherNet Protocol Attestation")
    print("=" * 40)
    print()

    # Load key
    print(f"Loading signing key from {args.key_path}...")
    signing_key = load_signing_key(args.key_path)
    public_key = signing_key.verify_key.encode(encoder=HexEncoder).decode()
    print(f"  Signer: {public_key[:16]}...")
    print()

    # Build attestation
    print(f"Hashing repo at {args.repo_path}...")
    attestation = build_attestation(args.repo_path)
    print(f"  Commit: {attestation['git_commit'][:12]}...")
    print(f"  Files: {attestation['codebase_stats']['tracked_source_files']}")
    print(f"  Root hash: {attestation['root_hash'][:16]}...")
    print(f"  Key files: {len(attestation['key_file_hashes'])}")
    print()

    # Sign
    print("Signing attestation...")
    signed_doc = sign_attestation(attestation, signing_key)
    print(f"  Signature: {signed_doc['signature']['signature_hex'][:16]}...")
    print()

    # Save
    timestamp = attestation["timestamp_unix"]
    output_file = f"protocol-attestation-{timestamp}.json"
    with open(output_file, "w") as f:
        json.dump(signed_doc, f, indent=2)
    print(f"Saved: {output_file}")
    print()

    # Also save a copy with a stable name
    with open("protocol-attestation-latest.json", "w") as f:
        json.dump(signed_doc, f, indent=2)
    print(f"Saved: protocol-attestation-latest.json")
    print()

    # Verification hint
    print("To verify this attestation later:")
    print(f"  1. Check git commit: git log --oneline {attestation['git_commit'][:12]}")
    print(f"  2. Recompute root hash from the repo at that commit")
    print(f"  3. Verify Ed25519 signature against public key {public_key[:16]}...")
    print()
    print("This attestation is valid independent of GitHub, PyPI, or any third party.")


if __name__ == "__main__":
    main()
