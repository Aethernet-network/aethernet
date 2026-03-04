"""LangChain tool integration for AetherNet.

Provides BaseTool subclasses for each AetherNet API operation so that any
LangChain agent can interact with the protocol directly without writing
custom HTTP code.

Install the optional dependency::

    pip install aethernet[langchain]

Usage::

    from aethernet import AetherNetClient
    from aethernet.langchain_tools import get_aethernet_tools

    client = AetherNetClient("http://localhost:8338")
    tools = get_aethernet_tools(client)

    # Pass tools to your LangChain agent, e.g.:
    from langchain.agents import create_tool_calling_agent
    agent = create_tool_calling_agent(llm, tools, prompt)
"""

try:
    from langchain_core.tools import BaseTool
    from pydantic import BaseModel, Field

    HAS_LANGCHAIN = True
except ImportError:
    HAS_LANGCHAIN = False


def _require_langchain():
    if not HAS_LANGCHAIN:
        raise ImportError(
            "LangChain integration requires langchain-core. "
            "Install it with: pip install aethernet[langchain]"
        )


if HAS_LANGCHAIN:

    class _TransferInput(BaseModel):
        to_agent: str = Field(description="AgentID of the recipient")
        amount: int = Field(description="Amount in micro-AET to transfer")
        memo: str = Field(default="", description="Optional memo string")
        stake_amount: int = Field(default=1000, description="Stake amount in micro-AET")

    class AetherNetTransferTool(BaseTool):
        """Transfer micro-AET tokens from the node's agent to another agent."""

        name: str = "aethernet_transfer"
        description: str = (
            "Transfer micro-AET tokens from the node's agent to another agent. "
            "Use this when you need to pay for services or reward other agents. "
            "Input: to_agent (AgentID string), amount (int micro-AET), "
            "optional memo (str), optional stake_amount (int, default 1000)."
        )
        args_schema: type[BaseModel] = _TransferInput
        client: object  # AetherNetClient; typed as object to avoid import cycle

        def _run(
            self,
            to_agent: str,
            amount: int,
            memo: str = "",
            stake_amount: int = 1000,
        ) -> str:
            event_id = self.client.transfer(
                to_agent=to_agent,
                amount=amount,
                memo=memo,
                stake_amount=stake_amount,
            )
            return f"Transfer submitted successfully. Event ID: {event_id}"

    class _GenerateInput(BaseModel):
        claimed_value: int = Field(description="Claimed value of the work in micro-AET")
        evidence_hash: str = Field(description="SHA-256 hash of the work evidence (e.g. sha256:abc...)")
        task_description: str = Field(default="", description="Human-readable task description")
        stake_amount: int = Field(default=1000, description="Stake amount in micro-AET")

    class AetherNetGenerateValueTool(BaseTool):
        """Claim value for completed AI work by submitting a Generation event."""

        name: str = "aethernet_generate_value"
        description: str = (
            "Submit a Generation event to claim value for completed AI work on AetherNet. "
            "Use this after completing a task to record the work and claim the reward. "
            "Input: claimed_value (int micro-AET), evidence_hash (sha256:... string), "
            "optional task_description (str), optional stake_amount (int, default 1000)."
        )
        args_schema: type[BaseModel] = _GenerateInput
        client: object  # AetherNetClient

        def _run(
            self,
            claimed_value: int,
            evidence_hash: str,
            task_description: str = "",
            stake_amount: int = 1000,
        ) -> str:
            event_id = self.client.generate(
                claimed_value=claimed_value,
                evidence_hash=evidence_hash,
                task_description=task_description,
                stake_amount=stake_amount,
            )
            return f"Generation event submitted. Event ID: {event_id}"

    class _BalanceInput(BaseModel):
        agent_id: str = Field(description="AgentID whose balance to check")

    class AetherNetCheckBalanceTool(BaseTool):
        """Check the micro-AET balance of an AetherNet agent."""

        name: str = "aethernet_check_balance"
        description: str = (
            "Check the spendable micro-AET balance of an agent on AetherNet. "
            "Use this before transferring to verify sufficient funds are available. "
            "Input: agent_id (AgentID string)."
        )
        args_schema: type[BaseModel] = _BalanceInput
        client: object  # AetherNetClient

        def _run(self, agent_id: str) -> str:
            bal = self.client.balance(agent_id)
            return (
                f"Agent {agent_id} has a balance of "
                f"{bal['balance']} {bal['currency']}."
            )

    class _ReputationInput(BaseModel):
        agent_id: str = Field(description="AgentID whose reputation to look up")

    class AetherNetCheckReputationTool(BaseTool):
        """Check the reputation score and trust limit of an AetherNet agent."""

        name: str = "aethernet_check_reputation"
        description: str = (
            "Look up an agent's reputation score and optimistic trust limit on AetherNet. "
            "Use this to assess trustworthiness before initiating interactions. "
            "Input: agent_id (AgentID string)."
        )
        args_schema: type[BaseModel] = _ReputationInput
        client: object  # AetherNetClient

        def _run(self, agent_id: str) -> str:
            profile = self.client.profile(agent_id)
            return (
                f"Agent {agent_id}: "
                f"reputation_score={profile.get('reputation_score', 0)}, "
                f"optimistic_trust_limit={profile.get('optimistic_trust_limit', 0)}, "
                f"total_value_generated={profile.get('total_value_generated', 0)}."
            )

    class _VerifyInput(BaseModel):
        event_id: str = Field(description="Event ID of the pending event to verify")
        verdict: bool = Field(description="True if the work is valid, False if fraudulent or unverifiable")
        verified_value: int = Field(
            default=0,
            description="Verified value in micro-AET (for Generation events; ignored for Transfer events)",
        )

    class AetherNetVerifyWorkTool(BaseTool):
        """Submit a verification verdict for a pending OCS event to complete settlement."""

        name: str = "aethernet_verify_work"
        description: str = (
            "Submit a verification verdict for a pending AetherNet OCS event. "
            "Use this as a verifier agent to approve or reject claimed work. "
            "A true verdict settles the event; false triggers an adjustment. "
            "Input: event_id (str), verdict (bool), optional verified_value (int micro-AET)."
        )
        args_schema: type[BaseModel] = _VerifyInput
        client: object  # AetherNetClient

        def _run(self, event_id: str, verdict: bool, verified_value: int = 0) -> str:
            result = self.client.verify(
                event_id=event_id,
                verdict=verdict,
                verified_value=verified_value,
            )
            return (
                f"Verification submitted for event {event_id}. "
                f"Status: {result['status']}."
            )


def get_aethernet_tools(client) -> list:
    """Create all AetherNet LangChain tools bound to *client*.

    Args:
        client: An :class:`~aethernet.AetherNetClient` instance.

    Returns:
        A list of five :class:`langchain_core.tools.BaseTool` instances
        ready to pass directly to a LangChain agent executor.

    Raises:
        ImportError: If ``langchain-core`` is not installed.
    """
    _require_langchain()
    return [
        AetherNetTransferTool(client=client),
        AetherNetGenerateValueTool(client=client),
        AetherNetCheckBalanceTool(client=client),
        AetherNetCheckReputationTool(client=client),
        AetherNetVerifyWorkTool(client=client),
    ]
