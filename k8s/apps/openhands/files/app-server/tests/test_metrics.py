import json

from fastapi.testclient import TestClient

from app_server.main import build_metrics

from .test_conversations import CONV_ID, _start, agent  # noqa: F401

STATS = {
    "usage_to_metrics": {
        "acp-managed": {
            "model_name": "sonnet",
            "accumulated_cost": 0.5,
            "accumulated_token_usage": {
                "prompt_tokens": 4,
                "completion_tokens": 24,
                "cache_read_tokens": 100,
            },
        }
    }
}


def test_metrics_report_usage_per_conversation_and_sandboxes(
    api, user, state, hooks, agent
):
    sandbox_id = _start(api, user, title='Say "hi"')["sandbox_id"]
    key = state.sandboxes.row(sandbox_id)["session_api_key"]
    # Usage is part of the agent server's conversation record.
    r = hooks.post(
        f"/sandboxes/{sandbox_id}/conversations",
        headers={"X-Session-API-Key": key},
        json={"id": CONV_ID, "execution_status": "finished", "stats": STATS},
    )
    assert r.status_code == 200
    assert json.loads(state.conversations.row(CONV_ID)["meta"])["stats"] == STATS

    text = TestClient(build_metrics(state)).get("/metrics").text
    own = f'conversation="{CONV_ID}",title="Say \\"hi\\"",trigger="gui",model="sonnet"'
    assert f"openhands_conversation_cost_usd{{{own}}} 0.500000" in text
    assert f'openhands_conversation_tokens{{{own},kind="cache_read"}} 100' in text
    assert 'openhands_usage_cost_usd{model="sonnet"} 0.500000' in text
    assert 'openhands_usage_tokens{model="sonnet",kind="completion"} 24' in text
    assert 'openhands_conversations{status="finished"} 1' in text
    assert "openhands_conversations_total 1" in text
    assert 'openhands_sandboxes{spec="repo",status="RUNNING"} 1' in text


def test_the_api_port_does_not_serve_metrics(api, user):
    assert api.get("/metrics", headers=user).status_code == 404
