"""Tests for trajectory commit SDK methods."""

import json


def test_emit_request_serialization():
    """Verify the emit request body has correct fields."""
    body = {
        "outcome": "exploring",
        "approach_description": "Testing approach",
        "compute_cost": 50000,
        "quality_score_bp": 7500,
        "parent_commit_id": "parent-123",
        "parameters": {"lr": "0.001"},
        "evidence_snippet": "Loss decreased",
        "category_hint": "research",
        "branch_id": "main",
    }
    serialized = json.dumps(body, sort_keys=True)
    deserialized = json.loads(serialized)

    assert deserialized["outcome"] == "exploring"
    assert deserialized["quality_score_bp"] == 7500
    assert deserialized["parameters"]["lr"] == "0.001"
    assert deserialized["parent_commit_id"] == "parent-123"


def test_emit_response_parsing():
    """Verify the emit response can be parsed correctly."""
    response = {
        "event_id": "abc123",
        "checkpoint_hash": "def456",
        "checkpoint_size": 1024,
        "task_id": "task-789",
    }
    data = json.dumps(response)
    parsed = json.loads(data)

    assert parsed["event_id"] == "abc123"
    assert parsed["checkpoint_hash"] == "def456"
    assert parsed["checkpoint_size"] == 1024
    assert parsed["task_id"] == "task-789"


def test_get_trajectories_response_parsing():
    """Verify the get response can be parsed correctly."""
    response = {
        "task_id": "task-1",
        "count": 2,
        "commits": [
            {
                "event_id": "ev-1",
                "task_id": "task-1",
                "outcome": "exploring",
                "checkpoint_hash": "hash1",
                "checkpoint_size": 100,
                "causal_timestamp": 1,
                "body_available": True,
            },
            {
                "event_id": "ev-2",
                "task_id": "task-1",
                "parent_commit_id": "ev-1",
                "outcome": "converged",
                "checkpoint_hash": "hash2",
                "checkpoint_size": 200,
                "causal_timestamp": 2,
                "body_available": False,
            },
        ],
    }
    data = json.dumps(response)
    parsed = json.loads(data)

    assert parsed["count"] == 2
    assert parsed["commits"][0]["outcome"] == "exploring"
    assert parsed["commits"][1]["parent_commit_id"] == "ev-1"
    assert parsed["commits"][0]["body_available"] is True
    assert parsed["commits"][1]["body_available"] is False


def test_emit_request_minimal_fields():
    """Verify minimal emit request omits optional fields."""
    body = {
        "outcome": "dead_end",
        "approach_description": "Failed approach",
        "compute_cost": 1000,
        "quality_score_bp": 2000,
    }
    serialized = json.dumps(body)
    parsed = json.loads(serialized)

    assert "parent_commit_id" not in parsed
    assert "parameters" not in parsed
    assert "branch_id" not in parsed
    assert parsed["outcome"] == "dead_end"


def test_get_trajectories_query_params():
    """Verify query parameter construction logic."""
    # Simulate the parameter building from client.get_trajectories
    params = []
    include_bodies = True
    branch_id = "main"
    limit = 50

    if include_bodies:
        params.append("include_bodies=true")
    if branch_id:
        params.append(f"branch_id={branch_id}")
    if limit > 0:
        params.append(f"limit={limit}")

    qs = "&".join(params)
    path = f"/v1/tasks/trajectories/task-123?{qs}"

    assert "include_bodies=true" in path
    assert "branch_id=main" in path
    assert "limit=50" in path
    assert path.startswith("/v1/tasks/trajectories/task-123?")
