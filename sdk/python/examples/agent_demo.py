"""Two-agent demo: Alice generates AET through AI work, then pays Bob.

Run against a live node:

    python examples/agent_demo.py --node http://localhost:8338

The demo shows the full happy path:
  1. Both agents register with the node
  2. Alice submits a Generation event claiming AI compute credit
  3. A validator verifies Alice's work (settles it)
  4. Alice checks her balance, then transfers half to Bob
  5. Both balances are printed; DAG tip count is shown
"""

import argparse
import hashlib
import time

from aethernet import AetherNetClient, AetherNetError


def sha256(text: str) -> str:
    return "sha256:" + hashlib.sha256(text.encode()).hexdigest()


def main() -> None:
    parser = argparse.ArgumentParser(description="AetherNet two-agent demo")
    parser.add_argument("--node", default="http://localhost:8338", help="Node URL")
    args = parser.parse_args()

    print(f"Connecting to AetherNet node at {args.node}\n")

    # ------------------------------------------------------------------
    # 1. Create two clients (Alice and Bob share the same node for demo)
    # ------------------------------------------------------------------
    alice = AetherNetClient(args.node, agent_id="alice-demo-agent")
    bob = AetherNetClient(args.node, agent_id="bob-demo-agent")

    # Register both agents
    alice_reg = alice.register(capabilities=[{"type": "inference", "model": "gpt-4o"}])
    bob_reg = bob.register(capabilities=[{"type": "validation"}])
    print(f"Alice registered → {alice_reg['agent_id'][:16]}...")
    print(f"Bob   registered → {bob_reg['agent_id'][:16]}...\n")

    # ------------------------------------------------------------------
    # 2. Alice submits a Generation event (AI compute claim)
    # ------------------------------------------------------------------
    evidence = sha256("alice-inference-run-2025-01-01T00:00:00Z")
    gen_id = alice.generate(
        claimed_value=10_000,          # 10 000 micro-AET
        evidence_hash=evidence,
        task_description="GPT-4o inference run: 1 000 tokens",
        stake_amount=1_000,
        beneficiary_agent=alice.agent_id,
    )
    print(f"Alice submitted Generation event: {gen_id[:16]}...")

    # ------------------------------------------------------------------
    # 3. Bob (acting as validator) verifies Alice's work
    # ------------------------------------------------------------------
    time.sleep(0.1)  # brief pause so OCS engine registers the pending event
    try:
        verdict = alice.verify(event_id=gen_id, verdict=True, verified_value=10_000)
        print(f"Validator settled event → status={verdict['status']}\n")
    except AetherNetError as exc:
        print(f"Verify skipped (node may not support it yet): {exc}\n")

    # ------------------------------------------------------------------
    # 4. Check Alice's balance
    # ------------------------------------------------------------------
    try:
        bal = alice.balance()
        alice_balance = bal["balance"]
        currency = bal["currency"]
        print(f"Alice balance: {alice_balance} {currency}")
    except AetherNetError as exc:
        print(f"Balance unavailable (agent may not be funded): {exc}")
        alice_balance = 0
        currency = "AET"

    # ------------------------------------------------------------------
    # 5. Alice pays Bob half her balance (if she has any)
    # ------------------------------------------------------------------
    if alice_balance > 0:
        pay_amount = alice_balance // 2
        try:
            tx_id = alice.transfer(
                to_agent=bob.agent_id,
                amount=pay_amount,
                memo="demo payment from Alice to Bob",
                stake_amount=500,
            )
            print(f"Alice → Bob transfer: {pay_amount} {currency}  (event {tx_id[:16]}...)")
        except AetherNetError as exc:
            print(f"Transfer failed: {exc}")
    else:
        print("Alice has no balance to transfer (Generation not yet settled or node not funded)")

    # ------------------------------------------------------------------
    # 6. Final state
    # ------------------------------------------------------------------
    print()
    try:
        bob_bal = bob.balance()
        print(f"Bob balance:   {bob_bal['balance']} {bob_bal['currency']}")
    except AetherNetError:
        pass

    tips = alice.tips()
    print(f"DAG tips:      {len(tips)} event(s) at the frontier")

    node_status = alice.status()
    print(f"Node status:   peers={node_status.get('peers', 0)}  "
          f"dag={node_status.get('dag_size', 0)}  "
          f"ocs_pending={node_status.get('ocs_pending', 0)}")


if __name__ == "__main__":
    main()
