import json

from app_server.terminal_events import TerminalMirror


def _call(status, **extra):
    return {
        "kind": "ACPToolCallEvent",
        "tool_call_id": "toolu_1",
        "title": "dagger check",
        "tool_kind": "execute",
        "status": status,
        "raw_input": {"command": "dagger check"},
        "timestamp": "2026-10-09T01:00:00",
        **extra,
    }


def test_start_mirrors_an_action_and_completion_an_observation():
    m = TerminalMirror()
    (action,) = m.mirror(_call("in_progress"))
    assert action["action"] == {"kind": "TerminalAction", "command": "dagger check"}
    assert action["source"] == "agent" and action["tool_call_id"] == "toolu_1"

    assert m.mirror(_call("in_progress")) == []
    (obs,) = m.mirror(_call("completed", raw_output="ok"))
    assert obs["source"] == "environment" and obs["action_id"] == action["id"]
    assert obs["observation"]["content"] == [{"type": "text", "text": "ok"}]
    # The tab reads exit_code at the top level or under metadata.
    assert obs["observation"]["exit_code"] == 0
    assert obs["observation"]["metadata"]["exit_code"] == 0


def test_a_failed_call_reports_a_nonzero_exit_code():
    _, obs = TerminalMirror().mirror(_call("failed"))
    assert obs["observation"]["exit_code"] == 1
    assert obs["observation"]["metadata"]["exit_code"] == 1


def test_a_call_first_seen_complete_gets_both_events():
    kinds = [e["kind"] for e in TerminalMirror().mirror(_call("completed"))]
    assert kinds == ["ActionEvent", "ObservationEvent"]


def test_output_falls_back_to_content_blocks():
    _, obs = TerminalMirror().mirror(
        _call("completed", content=[{"content": {"text": "```console\nhi\n```"}}])
    )
    assert obs["observation"]["content"][0]["text"] == "```console\nhi\n```"


def test_other_events_pass_through_alone():
    m = TerminalMirror()
    assert m.mirror({"kind": "MessageEvent"}) == []
    assert m.mirror(_call("completed") | {"tool_kind": "edit"}) == []
    assert m.frames("not json") == ["not json"]
    other = json.dumps({"kind": "MessageEvent"})
    assert m.frames(other) == [other]


def test_a_shell_command_replaces_its_acp_event():
    m = TerminalMirror()
    (start,) = m.frames(json.dumps(_call("in_progress")))
    assert json.loads(start)["kind"] == "ActionEvent"
    (done,) = m.frames(json.dumps(_call("completed")))
    assert json.loads(done)["kind"] == "ObservationEvent"
