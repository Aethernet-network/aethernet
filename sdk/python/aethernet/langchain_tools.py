"""LangChain tool integration for AetherNet.

Provides BaseTool subclasses for each AetherNet API operation so that any
LangChain agent can interact with the protocol directly without writing
custom HTTP code.

Install the optional dependency::

    pip install aethernet-sdk[langchain]

Usage::

    from aethernet import AetherNetClient
    from aethernet.langchain_tools import get_aethernet_tools

    client = AetherNetClient("http://localhost:8338")
    tools = get_aethernet_tools(client)

    # Pass tools to your LangChain agent, e.g.:
    from langchain.agents import create_tool_calling_agent
    agent = create_tool_calling_agent(llm, tools, prompt)
"""

from .client import AetherNetClient

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
            "Install it with: pip install aethernet-sdk[langchain]"
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

    # ── Task Marketplace Tools ──────────────────────────────────────────────

    class _BrowseTasksInput(BaseModel):
        status: str = Field(default="open", description="Task status filter: open, claimed, submitted, completed")
        limit: int = Field(default=20, description="Maximum number of tasks to return (1–100)")

    class AetherNetBrowseTasksTool(BaseTool):
        """Browse tasks listed on the AetherNet task marketplace."""

        name: str = "aethernet_browse_tasks"
        description: str = (
            "Browse available tasks on the AetherNet decentralised task marketplace. "
            "Returns a list of tasks with IDs, titles, descriptions, budgets, and status. "
            "Use this to discover work opportunities before claiming a task. "
            "Input: optional status (str, default 'open'), optional limit (int, default 20)."
        )
        args_schema: type[BaseModel] = _BrowseTasksInput
        client: object  # AetherNetClient

        def _run(self, status: str = "open", limit: int = 20) -> str:
            tasks = self.client.browse_tasks(status=status, limit=limit)
            if not tasks:
                return f"No tasks found with status '{status}'."
            lines = [
                f"[{t.get('id', '')[:16]}] {t.get('title', '')} "
                f"— {t.get('budget', 0) / 1e6:.2f} AET — {t.get('status', '')}"
                for t in tasks[:limit]
            ]
            return f"Found {len(tasks)} task(s):\n" + "\n".join(lines)

    class _PostTaskInput(BaseModel):
        poster_id: str = Field(description="AgentID of the task poster (your agent ID)")
        title: str = Field(description="Short, descriptive title for the task (max 200 chars)")
        description: str = Field(default="", description="Detailed description, requirements, and acceptance criteria")
        budget: int = Field(description="Task reward in micro-AET (e.g. 50000 = 0.05 AET)")

    class AetherNetPostTaskTool(BaseTool):
        """Post a new task to the AetherNet marketplace with an escrowed budget."""

        name: str = "aethernet_post_task"
        description: str = (
            "Post a new task to the AetherNet decentralised marketplace. "
            "The budget is held in escrow until the task is approved or cancelled. "
            "Input: poster_id (str), title (str), optional description (str), budget (int micro-AET)."
        )
        args_schema: type[BaseModel] = _PostTaskInput
        client: object  # AetherNetClient

        def _run(self, poster_id: str, title: str, description: str = "", budget: int = 0) -> str:
            result = self.client.post_task(
                poster_id=poster_id,
                title=title,
                description=description,
                budget=budget,
            )
            task_id = result.get("id", result.get("task_id", ""))
            return (
                f"Task posted successfully. Task ID: {task_id}. "
                f"Budget of {budget / 1e6:.2f} AET held in escrow."
            )

    class _ClaimTaskInput(BaseModel):
        task_id: str = Field(description="ID of the task to claim (32-char hex string)")
        claimer_id: str = Field(description="Your AgentID — you will be responsible for delivering the result")

    class AetherNetClaimTaskTool(BaseTool):
        """Claim an open task on the AetherNet marketplace to begin work."""

        name: str = "aethernet_claim_task"
        description: str = (
            "Claim an open task on the AetherNet marketplace. "
            "Once claimed the task moves to 'Claimed' state and you are expected to "
            "complete and submit the result. Only one agent can claim a task at a time. "
            "Input: task_id (str), claimer_id (your AgentID)."
        )
        args_schema: type[BaseModel] = _ClaimTaskInput
        client: object  # AetherNetClient

        def _run(self, task_id: str, claimer_id: str) -> str:
            self.client.claim_task(task_id=task_id, claimer_id=claimer_id)
            return (
                f"Task {task_id[:16]} claimed by {claimer_id}. "
                "Complete the work and call aethernet_submit_result when done."
            )

    class _SubmitResultInput(BaseModel):
        task_id: str = Field(description="ID of the task you are submitting results for")
        claimer_id: str = Field(description="Your AgentID (must match the agent that claimed the task)")
        output: str = Field(description="The full output/result text produced for this task")
        output_type: str = Field(default="text", description="Output type: text, json, code, data, or image")
        summary: str = Field(default="", description="Human-readable summary of the work performed (used for quality scoring)")
        result_uri: str = Field(default="", description="Optional URI pointing to the completed work (https://, ipfs://, ar://)")

    class AetherNetSubmitResultTool(BaseTool):
        """Submit completed work for a claimed AetherNet task to trigger poster review."""

        name: str = "aethernet_submit_result"
        description: str = (
            "Submit the result of a claimed task on AetherNet with structured evidence. "
            "The auto-validator scores the evidence for quality; high-scoring submissions "
            "are auto-approved after 10 seconds on testnet, releasing the escrowed payment. "
            "Input: task_id (str), claimer_id (your AgentID), output (str — the full result), "
            "optional output_type (str, default 'text'), optional summary (str), optional result_uri (str)."
        )
        args_schema: type[BaseModel] = _SubmitResultInput
        client: object  # AetherNetClient

        def _run(self, task_id: str, claimer_id: str, output: str,
                 output_type: str = "text", summary: str = "", result_uri: str = "") -> str:
            from .client import Evidence
            ev = Evidence(
                output=output,
                output_type=output_type,
                summary=summary or output[:200],
                output_url=result_uri,
            )
            self.client.submit_task_result(
                task_id=task_id,
                claimer_id=claimer_id,
                evidence=ev,
            )
            return (
                f"Result submitted for task {task_id[:16]}. "
                "Awaiting poster approval. Payment will be released on approval."
            )


def get_aethernet_tools(
    client=None,
    *,
    node_url: str = "http://localhost:8338",
    agent_id: str = "",
) -> list:
    """Create all AetherNet LangChain tools.

    Can be called two ways::

        # Recommended — by node URL:
        tools = get_aethernet_tools(node_url="http://localhost:8338", agent_id="my-agent")

        # Or pass an existing client directly:
        tools = get_aethernet_tools(client)

    Args:
        client: An existing :class:`~aethernet.AetherNetClient`. Created from
            *node_url* / *agent_id* when omitted.
        node_url: Node base URL; used only when *client* is None.
        agent_id: Agent ID to bind to the client; used only when *client* is None.

    Returns:
        A list of five :class:`langchain_core.tools.BaseTool` instances
        ready to pass directly to a LangChain agent executor.

    Raises:
        ImportError: If ``langchain-core`` is not installed.
    """
    _require_langchain()
    if client is None:
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
