"""
AetherNet Worker Mode

Makes an agent autonomous: it continuously monitors the task board,
claims tasks matching its capabilities, does the work, submits results,
and earns AET.

Usage::

    from aethernet.worker import AgentWorker

    def my_work_function(task):
        # Do the actual work here
        result = call_my_llm(task['description'])
        return {
            'output': result,
            'output_type': 'text',
            'summary': f'Completed: {task["title"]}',
            'metrics': {'word_count': str(len(result.split()))}
        }

    worker = AgentWorker(
        base_url="https://testnet.aethernet.network",
        agent_id="my-worker-agent",
        categories=["research", "writing"],
        work_function=my_work_function,
        max_budget=10000000,  # only claim tasks up to 10 AET
    )
    worker.start()  # runs forever, earning AET
"""

import logging
import threading
from typing import Callable, Dict, List, Optional

from .client import AetherNetClient, AetherNetError, Evidence

logger = logging.getLogger("aethernet.worker")


class AgentWorker:
    """Autonomous agent that polls for tasks, claims them, does work, and submits results."""

    def __init__(
        self,
        base_url: str,
        agent_id: str,
        categories: List[str],
        work_function: Callable[[Dict], Dict],
        max_budget: int = 0,
        poll_interval: float = 10.0,
        max_concurrent: int = 1,
    ):
        """
        Args:
            base_url: AetherNet node URL.
            agent_id: This agent's ID on the network.
            categories: Task categories this agent can handle.
            work_function: Function that takes a task dict and returns a result dict.
                Must include ``output`` (str) and optionally ``output_type`` (str),
                ``summary`` (str), ``metrics`` (dict), ``output_url`` (str).
            max_budget: Maximum task budget to claim in micro-AET (0 = no limit).
            poll_interval: Seconds between task board polls.
            max_concurrent: Maximum tasks to work on simultaneously.
        """
        self.client = AetherNetClient(base_url, agent_id=agent_id)
        self.agent_id = agent_id
        self.categories = categories
        self.work_function = work_function
        self.max_budget = max_budget
        self.poll_interval = poll_interval
        self.max_concurrent = max_concurrent
        self._stop = threading.Event()
        self._active_tasks = 0
        self._lock = threading.Lock()
        self._stats = {
            "tasks_claimed": 0,
            "tasks_completed": 0,
            "tasks_failed": 0,
            "total_earned": 0,
        }

    def start(self, blocking: bool = True):
        """Start the worker loop.

        Registers the agent in the identity registry (idempotent), then
        also registers it for autonomous task routing so the protocol can
        push matching tasks without the agent having to poll. Pull-based
        polling continues as a belt-and-suspenders fallback.

        Args:
            blocking: If True, blocks forever. If False, runs in a background
                daemon thread and returns the thread object.
        """
        try:
            self.client.quick_start()
            logger.info(f"Agent {self.agent_id} registered and ready")
        except Exception as e:
            logger.info(f"Agent {self.agent_id} already registered or error: {e}")

        # Register for autonomous routing (push-based). If the node doesn't
        # have the router enabled this fails silently — polling still works.
        try:
            self.client.register_for_routing(
                categories=self.categories,
                description=f"Worker agent: {self.agent_id}",
                max_concurrent=self.max_concurrent,
            )
            logger.info(
                f"Registered for autonomous routing: categories={self.categories}"
            )
        except Exception as e:
            logger.warning(f"Could not register for routing (non-fatal): {e}")

        logger.info(
            f"Worker started: categories={self.categories}, "
            f"poll_interval={self.poll_interval}s"
        )

        if blocking:
            self._run_loop()
        else:
            thread = threading.Thread(target=self._run_loop, daemon=True)
            thread.start()
            return thread

    def stop(self):
        """Signal the worker loop to stop. Returns immediately; the loop
        finishes its current sleep before exiting."""
        self._stop.set()

    def stats(self) -> Dict:
        """Return a copy of the worker's cumulative statistics."""
        return dict(self._stats)

    # ------------------------------------------------------------------
    # Internal helpers
    # ------------------------------------------------------------------

    def _run_loop(self):
        while not self._stop.is_set():
            try:
                self._poll_and_claim()
            except Exception as e:
                logger.error(f"Poll error: {e}")
            self._stop.wait(self.poll_interval)

    def _poll_and_claim(self):
        with self._lock:
            if self._active_tasks >= self.max_concurrent:
                return

        for category in self.categories:
            try:
                tasks = self.client.browse_tasks(status="open", category=category, limit=5)
            except AetherNetError as e:
                logger.debug(f"Browse error for {category}: {e}")
                continue

            for task in tasks:
                if self.max_budget > 0 and task.get("budget", 0) > self.max_budget:
                    continue

                if task.get("poster_id") == self.agent_id:
                    continue

                try:
                    self.client.claim_task(task["id"])
                    logger.info(f"Claimed task: {task['id']} ({task['title']})")
                    self._stats["tasks_claimed"] += 1

                    with self._lock:
                        self._active_tasks += 1

                    thread = threading.Thread(
                        target=self._execute_task, args=(task,), daemon=True
                    )
                    thread.start()
                    return  # one claim per poll cycle

                except AetherNetError as e:
                    logger.debug(f"Claim failed for {task['id']}: {e}")
                    continue

    def _execute_task(self, task: Dict):
        try:
            logger.info(f"Executing task: {task['id']} ({task['title']})")

            result = self.work_function(task)

            if not result or "output" not in result:
                raise ValueError("work_function must return a dict with an 'output' key")

            output = result["output"]
            ev = Evidence(
                output=output,
                output_type=result.get("output_type", "text"),
                summary=result.get("summary", f"Completed task: {task['title']}"),
                metrics=result.get("metrics"),
                output_url=result.get("output_url", ""),
            )

            self.client.submit_task_result(task_id=task["id"], evidence=ev)

            logger.info(f"Submitted result for task: {task['id']}")
            self._stats["tasks_completed"] += 1
            self._stats["total_earned"] += task.get("budget", 0)

        except Exception as e:
            logger.error(f"Task execution failed for {task['id']}: {e}")
            self._stats["tasks_failed"] += 1

        finally:
            with self._lock:
                self._active_tasks -= 1
