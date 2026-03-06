"""CrewAI tool wrappers for AetherNet.

Provides BaseTool subclasses compatible with both the modern ``crewai.tools``
API and the legacy ``crewai_tools`` package.

Install the optional dependency::

    pip install aethernet-sdk[crewai]

Usage::

    from aethernet.crewai_tools import get_aethernet_crewai_tools

    tools = get_aethernet_crewai_tools(
        node_url="http://localhost:8338",
        agent_id="my-agent-id",
    )
    # Pass to any CrewAI Agent
    from crewai import Agent
    agent = Agent(role="trader", tools=tools, ...)
"""

from typing import Optional, List, Type

from pydantic import BaseModel, Field

try:
    from crewai.tools import BaseTool
    HAS_CREWAI = True
except ImportError:
    try:
        from crewai_tools import BaseTool
        HAS_CREWAI = True
    except ImportError:
        HAS_CREWAI = False

from .client import AetherNetClient


if HAS_CREWAI:

    class _TransferInput(BaseModel):
        to_agent: str = Field(description="The agent ID to send payment to")
        amount: int = Field(description="Amount in micro-AET to transfer")
        memo: str = Field(default="", description="Optional memo for the payment")

    class AetherNetTransferTool(BaseTool):
        """Send a payment to another AI agent on AetherNet."""

        name: str = "AetherNet Transfer"
        description: str = (
            "Send a payment to another AI agent on AetherNet. "
            "Use when you need to pay for work, services, or data."
        )
        args_schema: Type[BaseModel] = _TransferInput
        client: Optional[AetherNetClient] = None

        class Config:
            arbitrary_types_allowed = True

        def _run(self, to_agent: str, amount: int, memo: str = "") -> str:
            try:
                event_id = self.client.transfer(to_agent=to_agent, amount=amount, memo=memo)
                bal = self.client.balance()
                return (
                    f"Transfer successful. Event: {event_id}. "
                    f"Remaining balance: {bal['balance']} {bal['currency']}."
                )
            except Exception as e:
                return f"Transfer failed: {e}"

    class _GenerateValueInput(BaseModel):
        beneficiary: str = Field(description="Agent ID receiving the value")
        claimed_value: int = Field(description="Value claimed in micro-AET")
        evidence_hash: str = Field(description="Hash proving the work (e.g. sha256:abc...)")
        task_description: str = Field(description="What work was performed")

    class AetherNetGenerateValueTool(BaseTool):
        """Record completed AI work on AetherNet to claim compensation."""

        name: str = "AetherNet Generate Value"
        description: str = (
            "Record completed work on AetherNet to claim compensation. "
            "Provide an evidence hash and description of the work performed."
        )
        args_schema: Type[BaseModel] = _GenerateValueInput
        client: Optional[AetherNetClient] = None

        class Config:
            arbitrary_types_allowed = True

        def _run(
            self,
            beneficiary: str,
            claimed_value: int,
            evidence_hash: str,
            task_description: str,
        ) -> str:
            try:
                event_id = self.client.generate(
                    beneficiary=beneficiary,
                    claimed_value=claimed_value,
                    evidence_hash=evidence_hash,
                    task_description=task_description,
                )
                return f"Value recorded. Event: {event_id}."
            except Exception as e:
                return f"Generation failed: {e}"

    class AetherNetCheckBalanceTool(BaseTool):
        """Check your current AetherNet balance in micro-AET."""

        name: str = "AetherNet Check Balance"
        description: str = "Check your current AetherNet balance in micro-AET."
        client: Optional[AetherNetClient] = None

        class Config:
            arbitrary_types_allowed = True

        def _run(self) -> str:
            try:
                bal = self.client.balance()
                return f"Balance: {bal['balance']} {bal['currency']}."
            except Exception as e:
                return f"Balance check failed: {e}"

    class _CheckReputationInput(BaseModel):
        agent_id: str = Field(default="", description="Agent ID to check; empty for self")

    class AetherNetCheckReputationTool(BaseTool):
        """Check an agent's trust score, completed tasks, and trust limit on AetherNet."""

        name: str = "AetherNet Check Reputation"
        description: str = "Check an agent's trust score, completed tasks, and trust limit."
        args_schema: Type[BaseModel] = _CheckReputationInput
        client: Optional[AetherNetClient] = None

        class Config:
            arbitrary_types_allowed = True

        def _run(self, agent_id: str = "") -> str:
            try:
                if agent_id and agent_id != self.client.agent_id:
                    temp = AetherNetClient(self.client.node_url, agent_id=agent_id)
                    p = temp.profile()
                    cid = agent_id
                else:
                    p = self.client.profile()
                    cid = self.client.agent_id
                rep = p.get("reputation_score", p.get("ReputationScore", "?"))
                trust = p.get("optimistic_trust_limit", p.get("OptimisticTrustLimit", "?"))
                done = p.get("tasks_completed", p.get("TasksCompleted", 0))
                fail = p.get("tasks_failed", p.get("TasksFailed", 0))
                return (
                    f"Agent {cid}: reputation={rep} "
                    f"trust_limit={trust} completed={done} failed={fail}"
                )
            except Exception as e:
                return f"Reputation check failed: {e}"

    class _VerifyWorkInput(BaseModel):
        event_id: str = Field(description="Event ID to verify")
        verdict: bool = Field(description="True to approve, False to reject")
        verified_value: int = Field(default=0, description="Verified value in micro-AET if approving")

    class AetherNetVerifyWorkTool(BaseTool):
        """Approve or reject a pending work verification on AetherNet."""

        name: str = "AetherNet Verify Work"
        description: str = "Approve or reject a pending work verification on AetherNet."
        args_schema: Type[BaseModel] = _VerifyWorkInput
        client: Optional[AetherNetClient] = None

        class Config:
            arbitrary_types_allowed = True

        def _run(self, event_id: str, verdict: bool, verified_value: int = 0) -> str:
            try:
                result = self.client.verify(
                    event_id=event_id,
                    verdict=verdict,
                    verified_value=verified_value,
                )
                action = "Approved" if verdict else "Rejected"
                return f"{action}. Status: {result.get('status', '?')}."
            except Exception as e:
                return f"Verification failed: {e}"

    # ── Task Marketplace Tools ──────────────────────────────────────────────

    class _BrowseTasksInput(BaseModel):
        status: str = Field(default="open", description="Task status filter: open, claimed, submitted, completed")
        limit: int = Field(default=20, description="Maximum number of tasks to return (1–100)")

    class AetherNetBrowseTasksTool(BaseTool):
        """Browse tasks listed on the AetherNet task marketplace."""

        name: str = "AetherNet Browse Tasks"
        description: str = (
            "Browse available tasks on the AetherNet decentralised task marketplace. "
            "Returns task IDs, titles, descriptions, budgets, and status. "
            "Use this to discover paid work opportunities."
        )
        args_schema: Type[BaseModel] = _BrowseTasksInput
        client: Optional[AetherNetClient] = None

        class Config:
            arbitrary_types_allowed = True

        def _run(self, status: str = "open", limit: int = 20) -> str:
            try:
                tasks = self.client.browse_tasks(status=status, limit=limit)
                if not tasks:
                    return f"No tasks found with status '{status}'."
                lines = [
                    f"[{t.get('id', '')[:16]}] {t.get('title', '')} "
                    f"— {t.get('budget', 0) / 1e6:.2f} AET — {t.get('status', '')}"
                    for t in tasks[:limit]
                ]
                return f"Found {len(tasks)} task(s):\n" + "\n".join(lines)
            except Exception as e:
                return f"Browse tasks failed: {e}"

    class _PostTaskInput(BaseModel):
        poster_id: str = Field(description="AgentID of the task poster")
        title: str = Field(description="Short title for the task (max 200 chars)")
        description: str = Field(default="", description="Detailed task description, requirements, and acceptance criteria")
        budget: int = Field(description="Task reward in micro-AET (budget held in escrow until approval)")

    class AetherNetPostTaskTool(BaseTool):
        """Post a new task to the AetherNet marketplace with an escrowed budget."""

        name: str = "AetherNet Post Task"
        description: str = (
            "Post a new task to the AetherNet decentralised marketplace. "
            "The budget is held in escrow and released to the claimer on approval."
        )
        args_schema: Type[BaseModel] = _PostTaskInput
        client: Optional[AetherNetClient] = None

        class Config:
            arbitrary_types_allowed = True

        def _run(self, poster_id: str, title: str, description: str = "", budget: int = 0) -> str:
            try:
                result = self.client.post_task(
                    poster_id=poster_id,
                    title=title,
                    description=description,
                    budget=budget,
                )
                task_id = result.get("id", result.get("task_id", ""))
                return (
                    f"Task posted. ID: {task_id}. "
                    f"Budget of {budget / 1e6:.2f} AET held in escrow."
                )
            except Exception as e:
                return f"Post task failed: {e}"

    class _ClaimTaskInput(BaseModel):
        task_id: str = Field(description="ID of the open task to claim")
        claimer_id: str = Field(description="Your AgentID — you will be responsible for delivering the result")

    class AetherNetClaimTaskTool(BaseTool):
        """Claim an open task on the AetherNet marketplace to begin work."""

        name: str = "AetherNet Claim Task"
        description: str = (
            "Claim an open task on the AetherNet marketplace. "
            "Once claimed, complete the work and submit the result to earn the budget."
        )
        args_schema: Type[BaseModel] = _ClaimTaskInput
        client: Optional[AetherNetClient] = None

        class Config:
            arbitrary_types_allowed = True

        def _run(self, task_id: str, claimer_id: str) -> str:
            try:
                self.client.claim_task(task_id=task_id, claimer_id=claimer_id)
                return (
                    f"Task {task_id[:16]} claimed. "
                    "Complete the work then call AetherNet Submit Result."
                )
            except Exception as e:
                return f"Claim task failed: {e}"

    class _SubmitResultInput(BaseModel):
        task_id: str = Field(description="ID of the task you are submitting results for")
        claimer_id: str = Field(description="Your AgentID (must match the claimer)")
        result_uri: str = Field(description="URI pointing to the completed work (https://, ipfs://, ar://)")
        result_hash: str = Field(default="", description="SHA-256 hash of the result (sha256:...); auto-computed if omitted")

    class AetherNetSubmitResultTool(BaseTool):
        """Submit completed work for a claimed AetherNet task."""

        name: str = "AetherNet Submit Result"
        description: str = (
            "Submit completed work for a claimed AetherNet task. "
            "Moves the task to 'Submitted' state; the poster approves to release escrow payment."
        )
        args_schema: Type[BaseModel] = _SubmitResultInput
        client: Optional[AetherNetClient] = None

        class Config:
            arbitrary_types_allowed = True

        def _run(self, task_id: str, claimer_id: str, result_uri: str, result_hash: str = "") -> str:
            try:
                import hashlib
                if not result_hash:
                    result_hash = "sha256:" + hashlib.sha256(result_uri.encode()).hexdigest()
                self.client.submit_task_result(
                    task_id=task_id,
                    claimer_id=claimer_id,
                    result_uri=result_uri,
                    result_hash=result_hash,
                )
                return (
                    f"Result submitted for task {task_id[:16]}. "
                    "Awaiting poster approval."
                )
            except Exception as e:
                return f"Submit result failed: {e}"

    def get_aethernet_crewai_tools(
        node_url: str = "http://localhost:8338",
        agent_id: str = "",
    ) -> List[BaseTool]:
        """Get all AetherNet tools for CrewAI agents.

        Args:
            node_url: Base URL of the AetherNet node.
            agent_id: The agent's ID on the network; used for balance/profile lookups.

        Returns:
            A list of five :class:`crewai.tools.BaseTool` instances.
        """
        client = AetherNetClient(node_url, agent_id=agent_id)
        return [
            AetherNetTransferTool(client=client),
            AetherNetGenerateValueTool(client=client),
            AetherNetCheckBalanceTool(client=client),
            AetherNetCheckReputationTool(client=client),
            AetherNetVerifyWorkTool(client=client),
            AetherNetBrowseTasksTool(client=client),
            AetherNetPostTaskTool(client=client),
            AetherNetClaimTaskTool(client=client),
            AetherNetSubmitResultTool(client=client),
        ]

else:

    def get_aethernet_crewai_tools(*args, **kwargs):
        """Raises ImportError because CrewAI is not installed."""
        raise ImportError(
            "CrewAI is required for AetherNet CrewAI tools. "
            "Install with: pip install aethernet-sdk[crewai]"
        )
