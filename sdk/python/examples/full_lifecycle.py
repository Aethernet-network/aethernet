"""Full lifecycle demo for AetherNet using the Python SDK.

Demonstrates the complete OCS settlement lifecycle:

  1.  Node status check
  2.  Register agent
  3.  Check initial balance
  4.  Submit a Generation event (AI work claim)
  5.  Confirm the event landed in the DAG
  6.  Inspect pending OCS items
  7.  Submit a Transfer event (pay another agent)
  8.  Verify the Generation event → Settled
  9.  Confirm the event is no longer pending
  10. Check final DAG tips
  11. Read agent reputation profile

Prerequisites
-------------
* An AetherNet node must be running and reachable at NODE_URL.
* The node's agent must be pre-funded so transfers can succeed.
  Fund it by calling ``tl.FundAgent(agentID, amount)`` from the Go node
  (there is no HTTP funding endpoint by design).

Run with::

    python full_lifecycle.py [NODE_URL]

Or from the sdk/python directory::

    python -m examples.full_lifecycle [NODE_URL]
"""

import hashlib
import sys
import time

# Allow running directly from the sdk/python directory without installing.
sys.path.insert(0, ".")

from aethernet import AetherNetClient, AetherNetError

NODE_URL = sys.argv[1] if len(sys.argv) > 1 else "http://localhost:8338"


def main() -> None:
    print("AetherNet Python SDK — Full Lifecycle Demo")
    print(f"Node: {NODE_URL}\n")

    client = AetherNetClient(NODE_URL)

    # ------------------------------------------------------------------
    # 1. Node status
    # ------------------------------------------------------------------
    status = client.status()
    print(
        f"[0] Node status: version={status['version']}  "
        f"peers={status['peers']}  dag_size={status['dag_size']}  "
        f"supply_ratio={status['supply_ratio']:.4f}x"
    )

    # ------------------------------------------------------------------
    # 2. Register agent
    # ------------------------------------------------------------------
    agent_id = client.register()
    print(f"\n[1] Registered agent: {agent_id}")

    # ------------------------------------------------------------------
    # 3. Balance check
    # ------------------------------------------------------------------
    bal = client.balance(agent_id)
    print(f"[2] Balance: {bal['balance']} {bal['currency']}")
    if bal["balance"] == 0:
        print(
            "     Note: agent has zero balance. "
            "Fund via Go node tl.FundAgent() before attempting transfers."
        )

    # ------------------------------------------------------------------
    # 4. Submit a Generation event (AI work claim)
    # ------------------------------------------------------------------
    work_output = f"Summarised a 10-page research paper at t={time.time()}"
    evidence_hash = "sha256:" + hashlib.sha256(work_output.encode()).hexdigest()

    gen_event_id = client.generate(
        claimed_value=5000,
        evidence_hash=evidence_hash,
        task_description="Summarised a 10-page research paper",
        stake_amount=1000,
    )
    print(f"\n[3] Generation event submitted: {gen_event_id}")

    # ------------------------------------------------------------------
    # 5. Confirm the event is in the DAG
    # ------------------------------------------------------------------
    ev = client.get_event(gen_event_id)
    print(
        f"[4] Event retrieved:  type={ev['Type']}  "
        f"settlement={ev['SettlementState']}  "
        f"timestamp={ev['CausalTimestamp']}"
    )

    # ------------------------------------------------------------------
    # 6. Inspect pending OCS items
    # ------------------------------------------------------------------
    pending = client.pending()
    print(f"\n[5] Pending OCS items: {len(pending)}")
    for item in pending:
        print(
            f"     - {item['EventID'][:16]}...  "
            f"type={item['EventType']}  amount={item['Amount']}"
        )

    # ------------------------------------------------------------------
    # 7. Transfer (requires funded agent; gracefully skipped if unfunded)
    # ------------------------------------------------------------------
    try:
        tx_event_id = client.transfer(
            to_agent=agent_id,   # self-transfer for demo purposes
            amount=250,
            currency="AET",
            memo="demo payment",
            stake_amount=1000,
        )
        print(f"\n[6] Transfer event submitted: {tx_event_id}")
    except AetherNetError as exc:
        print(f"\n[6] Transfer skipped (insufficient funds): {exc.message}")

    # ------------------------------------------------------------------
    # 8. Verify the Generation event — approve the work claim
    # ------------------------------------------------------------------
    try:
        verification = client.verify(
            event_id=gen_event_id,
            verdict=True,
            verified_value=5000,
        )
        print(
            f"\n[7] Verification result: "
            f"event_id={verification['event_id'][:16]}...  "
            f"verdict={verification['verdict']}  "
            f"status={verification['status']}"
        )
    except AetherNetError as exc:
        print(f"\n[7] Verification error: {exc.message}")

    # ------------------------------------------------------------------
    # 9. Confirm the event is no longer pending
    # ------------------------------------------------------------------
    pending_after = client.pending()
    print(f"\n[8] Pending OCS items after settlement: {len(pending_after)}")

    # ------------------------------------------------------------------
    # 10. Confirm event state is now Settled
    # ------------------------------------------------------------------
    ev_after = client.get_event(gen_event_id)
    print(f"[9] Event settlement state: {ev_after['SettlementState']}")

    # ------------------------------------------------------------------
    # 11. DAG tips
    # ------------------------------------------------------------------
    tips = client.tips()
    print(f"\n[10] DAG tips ({len(tips)}): {[t[:16] + '...' for t in tips]}")

    # ------------------------------------------------------------------
    # 12. Agent reputation profile
    # ------------------------------------------------------------------
    profile = client.profile(agent_id)
    print(f"\n[11] Agent profile:")
    print(f"     reputation_score      = {profile.get('reputation_score', 0)}")
    print(f"     optimistic_trust_limit= {profile.get('optimistic_trust_limit', 0)}")
    print(f"     total_value_generated = {profile.get('total_value_generated', 0)}")
    print(f"     tasks_completed       = {profile.get('tasks_completed', 0)}")

    print("\nFull lifecycle demo complete!")


if __name__ == "__main__":
    main()
