#!/usr/bin/env python3
"""
AetherNet Real Agent Demo

Two AI agents transact on AetherNet:
  - Researcher agent: has a task budget, commissions AI work
  - Writer agent: completes the work, records a value-generation event
  - Validator: verifies the work and settles the event

Prerequisites:
    1. Start a node:
          docker compose up -d
       or:
          ./aethernet start
    2. pip install aethernet
    3. python sdk/python/examples/real_agent_demo.py

No LLM API key needed — this demo exercises the full AetherNet payment flow
without requiring an external model. To integrate with a real LLM see the
commented section at the bottom of this file.
"""

import hashlib
import sys

from aethernet import AetherNetClient, AetherNetError

NODE = "http://localhost:8338"


def hash_work(content: str) -> str:
    """Return a deterministic evidence hash for a work product."""
    return "sha256:" + hashlib.sha256(content.encode()).hexdigest()[:32]


def section(title: str) -> None:
    print(f"\n{'━' * 56}")
    print(f"  {title}")
    print(f"{'━' * 56}")


def safe_balance(client: AetherNetClient) -> str:
    try:
        bal = client.balance()
        return f"{bal['balance']:,} {bal['currency']}"
    except AetherNetError:
        return "n/a"


def main() -> None:
    print()
    print("  ╔════════════════════════════════════════════════╗")
    print("  ║   AetherNet: AI Agent Payment Demo             ║")
    print("  ║   Two agents. Real transactions. Live.         ║")
    print("  ╚════════════════════════════════════════════════╝")

    # ------------------------------------------------------------------
    # Connect to node
    # ------------------------------------------------------------------
    try:
        status = AetherNetClient(NODE).status()
        print(f"\n  Connected to AetherNet node  version={status.get('version', '?')}")
    except Exception as exc:
        print(f"\n  Cannot reach {NODE}: {exc}")
        print("  Start a node:  docker compose up -d")
        sys.exit(1)

    researcher = AetherNetClient(NODE, agent_id="researcher-agent")
    writer = AetherNetClient(NODE, agent_id="writer-agent")
    validator = AetherNetClient(NODE, agent_id="validator-agent")

    # ------------------------------------------------------------------
    # 1. Registration
    # ------------------------------------------------------------------
    section("1. Agent Registration")

    for name, client, caps in [
        ("Researcher", researcher, [{"type": "coordination"}]),
        ("Writer",     writer,     [{"type": "content-generation", "model": "gpt-4o"}]),
        ("Validator",  validator,  [{"type": "verification"}]),
    ]:
        try:
            reg = client.register(capabilities=caps)
            print(f"  ✓ {name:12s}  id={reg['agent_id'][:20]}...")
        except AetherNetError as exc:
            # 409 or "already" means the agent is already registered — safe to continue.
            if exc.status_code == 409 or "already" in exc.message.lower():
                print(f"  ✓ {name:12s}  (already registered)")
            else:
                print(f"  ✗ {name:12s}  {exc.message}")

    # ------------------------------------------------------------------
    # 2. Task commission: Researcher → Writer (Transfer)
    # ------------------------------------------------------------------
    section("2. Researcher Posts Task (Transfer)")
    print('  Task:   "Summarize 10 papers on transformer architectures"')
    print("  Budget: 25,000 micro-AET")

    tx_id = None
    try:
        tx_id = researcher.transfer(
            to_agent="writer-agent",
            amount=25_000,
            memo="Summarize 10 papers on transformer architectures",
            stake_amount=1_000,
        )
        print(f"  ✓ Payment submitted  event={tx_id[:40]}...")
        print(f"    Settlement: OPTIMISTIC (instant acceptance, async verification)")
    except AetherNetError as exc:
        print(f"  ✗ Payment failed: {exc.message}")
        print(f"    (Accounts need funding — Generation events create new supply)")

    # ------------------------------------------------------------------
    # 3. Work delivery: Writer records a Generation event
    # ------------------------------------------------------------------
    section("3. Writer Delivers Work (Generation Event)")

    work_product = (
        "Comprehensive survey of transformer architectures (2017-2026):\n"
        "- Attention mechanisms: self-attention, cross-attention, sparse attention\n"
        "- Key architectures: BERT, GPT, T5, PaLM, Llama, Mistral, Claude\n"
        "- Efficiency innovations: FlashAttention, grouped-query attention\n"
        "- Emerging trends: mixture-of-experts, state-space models\n"
        "10 papers analysed. 4,200 words. 47 citations."
    )
    evidence = hash_work(work_product)
    print(f"  Work completed.  evidence={evidence[:40]}...")

    gen_id = None
    try:
        gen_id = writer.generate(
            claimed_value=25_000,
            evidence_hash=evidence,
            task_description="Survey: 10 transformer architecture papers, 4,200 words",
            stake_amount=1_000,
            beneficiary_agent=writer.agent_id,
        )
        print(f"  ✓ Generation recorded  event={gen_id[:40]}...")
        print(f"    Settlement: PENDING VERIFICATION")
    except AetherNetError as exc:
        print(f"  ✗ Generation failed: {exc.message}")

    # ------------------------------------------------------------------
    # 4. Validation: settle all pending events
    # ------------------------------------------------------------------
    section("4. Validator Settles Pending Events")

    try:
        pending = validator.pending()
        print(f"  {len(pending)} event(s) awaiting verification")

        settled = 0
        for item in pending:
            # The pending list may use different key names depending on server version.
            eid = item.get("event_id") or item.get("EventID") or item.get("id", "")
            if not eid:
                continue
            try:
                result = validator.verify(
                    event_id=eid,
                    verdict=True,
                    verified_value=25_000,
                )
                settled += 1
                status_str = result.get("status", "settled")
                print(f"  ✓ {eid[:36]}...  →  {status_str}")
            except AetherNetError as exc:
                print(f"  ✗ {eid[:36]}...  →  {exc.message}")

        print(f"\n  {settled} event(s) settled")
    except AetherNetError as exc:
        print(f"  ✗ Could not fetch pending events: {exc.message}")

    # ------------------------------------------------------------------
    # 5. Final balances
    # ------------------------------------------------------------------
    section("5. Final Balances")

    for name, client in [
        ("Researcher", researcher),
        ("Writer",     writer),
        ("Validator",  validator),
    ]:
        print(f"  {name:12s}  {safe_balance(client)}")

    # ------------------------------------------------------------------
    # Summary
    # ------------------------------------------------------------------
    section("Summary")
    try:
        node_status = AetherNetClient(NODE).status()
        tips = AetherNetClient(NODE).tips()
        remaining = len(validator.pending())
        print(f"  DAG events:  {node_status.get('dag_size', 0)}")
        print(f"  DAG tips:    {len(tips)}")
        print(f"  OCS pending: {remaining}")
        print(f"  Peers:       {node_status.get('peers', 0)}")
    except AetherNetError:
        pass

    print()
    print("  ╔════════════════════════════════════════════════╗")
    print("  ║   Demo complete. Agents transacted on          ║")
    print("  ║   AetherNet with instant optimistic settlement.║")
    print("  ╚════════════════════════════════════════════════╝")
    print()

    # ------------------------------------------------------------------
    # How to wire a real LLM
    # ------------------------------------------------------------------
    print("  ── Integrate a real LangChain agent ──────────────────")
    print("  from aethernet.langchain_tools import get_aethernet_tools")
    print("  from langchain.agents import create_tool_calling_agent")
    print()
    print("  tools = get_aethernet_tools(node_url=NODE, agent_id='my-agent')")
    print("  agent = create_tool_calling_agent(llm, tools, prompt)")
    print("  agent_executor.invoke({'input': 'Pay bob 500 AET for the report'})")
    print()


if __name__ == "__main__":
    main()
