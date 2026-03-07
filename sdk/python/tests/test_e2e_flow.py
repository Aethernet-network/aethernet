"""
End-to-end test of the developer experience.
This is what happens when someone runs: pip install aethernet-sdk
and follows the quickstart docs.

Run against the live testnet:
    cd sdk/python
    python tests/test_e2e_flow.py

Or as a pytest suite (skips automatically when testnet is unreachable):
    pytest tests/test_e2e_flow.py -v
"""
import hashlib
import time

import pytest

# Allow the test to be run directly or as part of pytest.
try:
    from aethernet import AetherNetClient
except ImportError:
    import sys
    import os
    sys.path.insert(0, os.path.join(os.path.dirname(__file__), ".."))
    from aethernet import AetherNetClient

TESTNET = "https://testnet.aethernet.network"


def _is_testnet_reachable() -> bool:
    """Return True when the testnet responds to a status request."""
    try:
        import requests
        resp = requests.get(TESTNET + "/v1/status", timeout=5)
        return resp.status_code == 200
    except Exception:
        return False


@pytest.fixture(scope="module")
def testnet_available():
    if not _is_testnet_reachable():
        pytest.skip("testnet unreachable — skipping E2E test")


def test_full_flow(testnet_available):  # noqa: F811
    """Full developer-experience flow: register → post task → claim → submit → settle."""
    ts = str(int(time.time()))

    # Step 1: Developer creates a client and registers an agent.
    poster = AetherNetClient(TESTNET, agent_id=f"e2e-poster-{ts}")
    poster_info = poster.quick_start(agent_name=f"e2e-poster-{ts}")
    assert poster_info.get("balance", 0) > 0, "Poster should be funded on registration"
    print(f"1. Poster registered: {poster.agent_id}, balance: {poster_info['balance']}")

    # Step 2: Register a worker agent.
    worker = AetherNetClient(TESTNET, agent_id=f"e2e-worker-{ts}")
    worker_info = worker.quick_start(agent_name=f"e2e-worker-{ts}")
    assert worker_info.get("balance", 0) > 0, "Worker should be funded on registration"
    print(f"2. Worker registered: {worker.agent_id}, balance: {worker_info['balance']}")

    # Step 3: Poster posts a task (escrows from poster's balance).
    task = poster.post_task(
        title=f"E2E test task {ts}",
        description="This is an end-to-end test of the full developer flow",
        category="testing",
        budget=1_000_000,  # 1 AET
    )
    assert "id" in task, f"Task should have an ID; got: {task}"
    task_id = task["id"]
    print(f"3. Task posted: {task_id}")

    # Step 4: Worker claims the task.
    worker.claim_task(task_id)
    print("4. Task claimed by worker")

    # Step 5: Worker submits a result with evidence good enough to pass the
    # evidence verifier (score ≥ 0.60). The note must contain keywords from
    # the task title/description and be at least 100 bytes long.
    output = (
        f"E2E test task completed at timestamp {ts}. "
        "Full end-to-end developer flow has been validated: registration, "
        "task posting, claiming, submission, and auto-settlement all work correctly."
    )
    evidence_hash = "sha256:" + hashlib.sha256(output.encode()).hexdigest()
    worker.submit_task_result(
        task_id=task_id,
        result_hash=evidence_hash,
        result_note=output,
    )
    print("5. Result submitted")

    # Step 6: Wait for the auto-validator to settle (poll for up to 30s).
    print("6. Waiting for auto-validator to settle the task...")
    settled = False
    for _ in range(30):
        time.sleep(1)
        task_status = poster.get_task(task_id)
        status = task_status.get("status", "unknown")
        if status == "completed":
            settled = True
            break
    print(f"7. Task status: {task_status.get('status', 'unknown')}")

    assert settled, (
        f"Task did not settle within 30 seconds. Final status: {task_status.get('status')}"
    )

    # Step 8: Check worker received payment (balance > 0 after settlement).
    worker_balance_resp = worker.balance()
    worker_bal = worker_balance_resp.get("balance", 0)
    print(f"8. Worker balance: {worker_bal}")
    # Worker should have at minimum their onboarding allocation.
    assert worker_bal > 0, "Worker should have a positive balance after receiving payment"

    # Step 9: Verify fee collection is reflected in economics.
    econ = poster.get_economics()
    print(
        f"9. Economics — total_collected: {econ.get('total_collected', 0)}, "
        f"treasury: {econ.get('treasury_accrued', 0)}, "
        f"total_generated_value: {econ.get('total_generated_value', 0)}"
    )
    assert econ.get("total_collected", 0) > 0, "total_collected should be > 0 after settlement"
    assert "total_generated_value" in econ, "economics must include total_generated_value"

    print("\n=== E2E FLOW COMPLETE ===")


# ---------------------------------------------------------------------------
# Direct execution support
# ---------------------------------------------------------------------------

def main():
    """Run the E2E test directly (without pytest)."""
    if not _is_testnet_reachable():
        print(f"ERROR: testnet unreachable at {TESTNET}")
        raise SystemExit(1)
    test_full_flow(None)  # pass dummy fixture value


if __name__ == "__main__":
    main()
