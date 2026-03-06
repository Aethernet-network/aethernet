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
        ]

else:

    def get_aethernet_crewai_tools(*args, **kwargs):
        """Raises ImportError because CrewAI is not installed."""
        raise ImportError(
            "CrewAI is required for AetherNet CrewAI tools. "
            "Install with: pip install aethernet-sdk[crewai]"
        )
