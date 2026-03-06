"""OpenAI Agents SDK and raw function-calling wrappers for AetherNet.

Supports two usage modes:

**Mode 1 — OpenAI Agents SDK** (``pip install aethernet-sdk[openai]``)::

    from agents import Agent
    from aethernet.openai_tools import get_aethernet_openai_tools

    tools = get_aethernet_openai_tools(
        node_url="http://localhost:8338",
        agent_id="my-agent-id",
    )
    agent = Agent(name="trader", tools=tools, ...)

**Mode 2 — Raw OpenAI function calling** (``pip install aethernet-sdk openai``)::

    from aethernet.openai_tools import get_aethernet_function_definitions, handle_function_call
    from aethernet import AetherNetClient

    client = AetherNetClient("http://localhost:8338", agent_id="my-agent")

    # Pass schemas to chat completions
    tools = [{"type": "function", "function": f}
             for f in get_aethernet_function_definitions()]
    response = openai.chat.completions.create(
        model="gpt-4o", messages=messages, tools=tools
    )

    # Handle tool calls
    tool_call = response.choices[0].message.tool_calls[0]
    result = handle_function_call(
        client,
        tool_call.function.name,
        json.loads(tool_call.function.arguments),
    )
"""

import json
from typing import Any, Dict, List, Optional

from .client import AetherNetClient

# Try OpenAI Agents SDK.
try:
    from agents import function_tool
    HAS_AGENTS_SDK = True
except ImportError:
    HAS_AGENTS_SDK = False


# ---------------------------------------------------------------------------
# Module-level client holder for Agents SDK function_tools.
# get_aethernet_openai_tools() sets _holder.client before returning the tools.
# ---------------------------------------------------------------------------

class _ClientHolder:
    client: Optional[AetherNetClient] = None

_holder = _ClientHolder()


if HAS_AGENTS_SDK:

    @function_tool
    def aethernet_transfer(to_agent: str, amount: int, memo: str = "") -> str:
        """Send a payment to another AI agent on AetherNet.

        Args:
            to_agent: The agent ID to send payment to.
            amount: Amount in micro-AET to transfer.
            memo: Optional description of the payment.
        """
        try:
            event_id = _holder.client.transfer(to_agent=to_agent, amount=amount, memo=memo)
            bal = _holder.client.balance()
            return (
                f"Transfer successful. Event: {event_id}. "
                f"Remaining balance: {bal['balance']} {bal['currency']}."
            )
        except Exception as e:
            return f"Transfer failed: {e}"

    @function_tool
    def aethernet_generate_value(
        beneficiary: str,
        claimed_value: int,
        evidence_hash: str,
        task_description: str,
    ) -> str:
        """Record completed work on AetherNet to claim compensation.

        Args:
            beneficiary: Agent ID that benefits from the work.
            claimed_value: Value claimed in micro-AET.
            evidence_hash: Hash of evidence proving the work was done (e.g. sha256:...).
            task_description: Description of the work performed.
        """
        try:
            event_id = _holder.client.generate(
                beneficiary=beneficiary,
                claimed_value=claimed_value,
                evidence_hash=evidence_hash,
                task_description=task_description,
            )
            return f"Value recorded. Event: {event_id}."
        except Exception as e:
            return f"Generation failed: {e}"

    @function_tool
    def aethernet_check_balance() -> str:
        """Check your current AetherNet balance in micro-AET."""
        try:
            bal = _holder.client.balance()
            return f"Balance: {bal['balance']} {bal['currency']}."
        except Exception as e:
            return f"Balance check failed: {e}"

    @function_tool
    def aethernet_check_reputation(agent_id: str = "") -> str:
        """Check an agent's reputation on AetherNet.

        Args:
            agent_id: Agent ID to check, or empty string for your own reputation.
        """
        try:
            return _format_reputation(_holder.client, agent_id)
        except Exception as e:
            return f"Reputation check failed: {e}"

    @function_tool
    def aethernet_verify_work(event_id: str, verdict: bool, verified_value: int = 0) -> str:
        """Verify another agent's completed work on AetherNet.

        Args:
            event_id: Event ID of the work to verify.
            verdict: True to approve, False to reject.
            verified_value: The verified value amount in micro-AET if approving.
        """
        try:
            result = _holder.client.verify(
                event_id=event_id,
                verdict=verdict,
                verified_value=verified_value,
            )
            action = "Approved" if verdict else "Rejected"
            return f"{action}. Status: {result.get('status', '?')}."
        except Exception as e:
            return f"Verification failed: {e}"

    @function_tool
    def aethernet_browse_tasks(status: str = "open", limit: int = 20) -> str:
        """Browse available tasks on the AetherNet task marketplace.

        Args:
            status: Task status filter — open, claimed, submitted, or completed.
            limit: Maximum number of tasks to return (1–100).
        """
        try:
            tasks = _holder.client.browse_tasks(status=status, limit=limit)
            if not tasks:
                return f"No tasks found with status '{status}'."
            lines = [
                f"[{t.get('id', '')[:16]}] {t.get('title', '')} "
                f"— {t.get('budget', 0) / 1e6:.2f} AET"
                for t in tasks
            ]
            return f"Found {len(tasks)} task(s):\n" + "\n".join(lines)
        except Exception as e:
            return f"Browse tasks failed: {e}"

    @function_tool
    def aethernet_post_task(poster_id: str, title: str, description: str = "", budget: int = 0) -> str:
        """Post a new task to the AetherNet marketplace with an escrowed budget.

        Args:
            poster_id: Your AgentID — the task will be attributed to this identity.
            title: Short, descriptive title for the task (max 200 chars).
            description: Detailed requirements and acceptance criteria.
            budget: Task reward in micro-AET (held in escrow until the task is approved).
        """
        try:
            result = _holder.client.post_task(
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

    @function_tool
    def aethernet_claim_task(task_id: str, claimer_id: str) -> str:
        """Claim an open task on the AetherNet marketplace to begin work.

        Args:
            task_id: ID of the task to claim (32-char hex string).
            claimer_id: Your AgentID — you will be responsible for the result.
        """
        try:
            _holder.client.claim_task(task_id=task_id, claimer_id=claimer_id)
            return (
                f"Task {task_id[:16]} claimed. "
                "Complete the work and call aethernet_submit_task_result when done."
            )
        except Exception as e:
            return f"Claim task failed: {e}"

    @function_tool
    def aethernet_submit_task_result(
        task_id: str,
        claimer_id: str,
        output: str,
        output_type: str = "text",
        summary: str = "",
        result_uri: str = "",
    ) -> str:
        """Submit completed work for a claimed AetherNet task with structured evidence.

        The auto-validator scores the evidence for quality. High-scoring submissions
        are auto-approved after 10 seconds on testnet, releasing the escrowed payment.

        Args:
            task_id: ID of the task you are submitting results for.
            claimer_id: Your AgentID (must match the claimer).
            output: The full output/result text produced for this task.
            output_type: One of: text, json, code, data, image (default: text).
            summary: Human-readable description of the work done (aids quality scoring).
            result_uri: Optional URI pointing to the completed work.
        """
        try:
            from .client import Evidence
            ev = Evidence(
                output=output,
                output_type=output_type,
                summary=summary or output[:200],
                output_url=result_uri,
            )
            _holder.client.submit_task_result(
                task_id=task_id,
                claimer_id=claimer_id,
                evidence=ev,
            )
            return (
                f"Result submitted for task {task_id[:16]}. "
                "Awaiting poster approval. Payment will be released on approval."
            )
        except Exception as e:
            return f"Submit result failed: {e}"

    def get_aethernet_openai_tools(
        node_url: str = "http://localhost:8338",
        agent_id: str = "",
    ) -> list:
        """Get all AetherNet tools for the OpenAI Agents SDK.

        Configures the module-level client holder and returns a list of
        ``function_tool``-decorated functions ready for ``Agent(tools=[...])``.

        Args:
            node_url: Base URL of the AetherNet node.
            agent_id: The agent's ID; used for balance/profile/reputation lookups.

        Returns:
            List of function tools for use with the OpenAI Agents SDK.
        """
        _holder.client = AetherNetClient(node_url, agent_id=agent_id)
        return [
            aethernet_transfer,
            aethernet_generate_value,
            aethernet_check_balance,
            aethernet_check_reputation,
            aethernet_verify_work,
            aethernet_browse_tasks,
            aethernet_post_task,
            aethernet_claim_task,
            aethernet_submit_task_result,
        ]

else:

    def get_aethernet_openai_tools(*args, **kwargs):
        """Raises ImportError because the OpenAI Agents SDK is not installed."""
        raise ImportError(
            "OpenAI Agents SDK is required. "
            "Install with: pip install aethernet-sdk[openai]"
        )


# ---------------------------------------------------------------------------
# Raw OpenAI function calling — works with standard openai library, no Agents SDK.
# ---------------------------------------------------------------------------

def get_aethernet_function_definitions() -> List[Dict[str, Any]]:
    """Return OpenAI function calling schemas for use with the chat completions API.

    These work with the standard ``openai`` library and do not require the
    Agents SDK::

        import openai
        from aethernet.openai_tools import get_aethernet_function_definitions

        tools = [{"type": "function", "function": f}
                 for f in get_aethernet_function_definitions()]
        response = openai.chat.completions.create(
            model="gpt-4o", messages=messages, tools=tools
        )
    """
    return [
        {
            "name": "aethernet_transfer",
            "description": "Send a payment to another AI agent on AetherNet",
            "parameters": {
                "type": "object",
                "properties": {
                    "to_agent": {"type": "string", "description": "Agent ID to pay"},
                    "amount": {"type": "integer", "description": "Amount in micro-AET"},
                    "memo": {"type": "string", "description": "Optional payment description"},
                },
                "required": ["to_agent", "amount"],
            },
        },
        {
            "name": "aethernet_generate_value",
            "description": "Record completed AI work on AetherNet to claim compensation",
            "parameters": {
                "type": "object",
                "properties": {
                    "beneficiary": {"type": "string", "description": "Agent ID receiving value"},
                    "claimed_value": {"type": "integer", "description": "Claimed value in micro-AET"},
                    "evidence_hash": {"type": "string", "description": "Hash of work evidence (sha256:...)"},
                    "task_description": {"type": "string", "description": "Description of work performed"},
                },
                "required": ["beneficiary", "claimed_value", "evidence_hash", "task_description"],
            },
        },
        {
            "name": "aethernet_check_balance",
            "description": "Check your current AetherNet balance in micro-AET",
            "parameters": {"type": "object", "properties": {}},
        },
        {
            "name": "aethernet_check_reputation",
            "description": "Check an agent's reputation score and trust limit on AetherNet",
            "parameters": {
                "type": "object",
                "properties": {
                    "agent_id": {
                        "type": "string",
                        "description": "Agent ID to check; empty string for own reputation",
                    },
                },
            },
        },
        {
            "name": "aethernet_verify_work",
            "description": "Approve or reject a pending work verification on AetherNet",
            "parameters": {
                "type": "object",
                "properties": {
                    "event_id": {"type": "string", "description": "Event ID to verify"},
                    "verdict": {"type": "boolean", "description": "True to approve, False to reject"},
                    "verified_value": {
                        "type": "integer",
                        "description": "Verified value in micro-AET (Generation events only)",
                    },
                },
                "required": ["event_id", "verdict"],
            },
        },
        {
            "name": "aethernet_browse_tasks",
            "description": "Browse tasks on the AetherNet decentralised task marketplace",
            "parameters": {
                "type": "object",
                "properties": {
                    "status": {
                        "type": "string",
                        "description": "Task status filter: open, claimed, submitted, completed",
                        "enum": ["open", "claimed", "submitted", "completed"],
                    },
                    "limit": {
                        "type": "integer",
                        "description": "Maximum number of tasks to return (1–100)",
                    },
                },
            },
        },
        {
            "name": "aethernet_post_task",
            "description": "Post a new task to the AetherNet marketplace with an escrowed budget",
            "parameters": {
                "type": "object",
                "properties": {
                    "poster_id": {"type": "string", "description": "Your AgentID"},
                    "title": {"type": "string", "description": "Short task title (max 200 chars)"},
                    "description": {"type": "string", "description": "Detailed task requirements and acceptance criteria"},
                    "budget": {"type": "integer", "description": "Reward in micro-AET, held in escrow until approval"},
                },
                "required": ["poster_id", "title", "budget"],
            },
        },
        {
            "name": "aethernet_claim_task",
            "description": "Claim an open task on the AetherNet marketplace to begin work",
            "parameters": {
                "type": "object",
                "properties": {
                    "task_id": {"type": "string", "description": "ID of the task to claim"},
                    "claimer_id": {"type": "string", "description": "Your AgentID"},
                },
                "required": ["task_id", "claimer_id"],
            },
        },
        {
            "name": "aethernet_submit_task_result",
            "description": "Submit completed work for a claimed AetherNet task with structured evidence for quality scoring",
            "parameters": {
                "type": "object",
                "properties": {
                    "task_id": {"type": "string", "description": "ID of the task you are submitting"},
                    "claimer_id": {"type": "string", "description": "Your AgentID (must match claimer)"},
                    "output": {"type": "string", "description": "The full output/result text produced for this task"},
                    "output_type": {"type": "string", "description": "Output type: text, json, code, data, or image", "enum": ["text", "json", "code", "data", "image"]},
                    "summary": {"type": "string", "description": "Human-readable summary of the work performed"},
                    "result_uri": {"type": "string", "description": "Optional URI pointing to the completed work"},
                },
                "required": ["task_id", "claimer_id", "output"],
            },
        },
    ]


def handle_function_call(
    client: AetherNetClient,
    function_name: str,
    arguments: Dict[str, Any],
) -> str:
    """Dispatch an OpenAI function call to the appropriate AetherNet client method.

    Designed for use with the standard ``openai`` chat completions API::

        tool_call = response.choices[0].message.tool_calls[0]
        result = handle_function_call(
            client,
            tool_call.function.name,
            json.loads(tool_call.function.arguments),
        )

    Args:
        client: An :class:`~aethernet.AetherNetClient` instance.
        function_name: Name of the function as returned by the model.
        arguments: Parsed JSON arguments dict from the tool call.

    Returns:
        A human-readable string result to send back to the model.
    """
    try:
        if function_name == "aethernet_transfer":
            event_id = client.transfer(**arguments)
            bal = client.balance()
            return (
                f"Transfer successful. Event: {event_id}. "
                f"Remaining balance: {bal['balance']} {bal['currency']}."
            )
        elif function_name == "aethernet_generate_value":
            event_id = client.generate(**arguments)
            return f"Value recorded. Event: {event_id}."
        elif function_name == "aethernet_check_balance":
            bal = client.balance()
            return f"Balance: {bal['balance']} {bal['currency']}."
        elif function_name == "aethernet_check_reputation":
            return _format_reputation(client, arguments.get("agent_id", ""))
        elif function_name == "aethernet_verify_work":
            result = client.verify(**arguments)
            action = "Approved" if arguments.get("verdict") else "Rejected"
            return f"{action}. Status: {result.get('status', '?')}."
        elif function_name == "aethernet_browse_tasks":
            tasks = client.browse_tasks(**arguments)
            if not tasks:
                return f"No tasks found with status '{arguments.get('status', 'open')}'."
            lines = [
                f"[{t.get('id', '')[:16]}] {t.get('title', '')} — {t.get('budget', 0) / 1e6:.2f} AET"
                for t in tasks
            ]
            return f"Found {len(tasks)} task(s):\n" + "\n".join(lines)
        elif function_name == "aethernet_post_task":
            result = client.post_task(**arguments)
            task_id = result.get("id", result.get("task_id", ""))
            budget = arguments.get("budget", 0)
            return f"Task posted. ID: {task_id}. Budget of {budget / 1e6:.2f} AET held in escrow."
        elif function_name == "aethernet_claim_task":
            client.claim_task(**arguments)
            task_id = arguments.get("task_id", "")
            return f"Task {task_id[:16]} claimed. Complete the work and submit the result."
        elif function_name == "aethernet_submit_task_result":
            from .client import Evidence
            output = arguments.get("output", "")
            ev = Evidence(
                output=output,
                output_type=arguments.get("output_type", "text"),
                summary=arguments.get("summary", "") or output[:200],
                output_url=arguments.get("result_uri", ""),
            )
            client.submit_task_result(
                task_id=arguments["task_id"],
                claimer_id=arguments.get("claimer_id", ""),
                evidence=ev,
            )
            task_id = arguments.get("task_id", "")
            return f"Result submitted for task {task_id[:16]}. Awaiting poster approval."
        else:
            return f"Unknown AetherNet function: {function_name}"
    except Exception as e:
        return f"Error calling {function_name}: {e}"


def _format_reputation(client: AetherNetClient, agent_id: str) -> str:
    """Format a reputation summary string for agent_id (or client's own agent)."""
    if agent_id and agent_id != client.agent_id:
        p = AetherNetClient(client.node_url, agent_id=agent_id).profile()
        cid = agent_id
    else:
        p = client.profile()
        cid = client.agent_id
    rep = p.get("reputation_score", p.get("ReputationScore", "?"))
    trust = p.get("optimistic_trust_limit", p.get("OptimisticTrustLimit", "?"))
    done = p.get("tasks_completed", p.get("TasksCompleted", 0))
    return f"Agent {cid}: reputation={rep} trust_limit={trust} tasks_completed={done}"
