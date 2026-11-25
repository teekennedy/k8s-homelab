import json

import pytest

from app_server.event_time import mark_utc, mark_utc_frame


def test_a_naive_timestamp_is_marked_utc():
    event = {"kind": "MessageEvent", "timestamp": "2026-11-22T10:11:50.649600"}
    assert mark_utc(event) == {
        "kind": "MessageEvent",
        "timestamp": "2026-11-22T10:11:50.649600Z",
    }
    # The stored copy is left as the agent server sent it.
    assert event["timestamp"] == "2026-11-22T10:11:50.649600"


@pytest.mark.parametrize(
    "ts", ["2026-11-22T10:11:50Z", "2026-11-22T03:11:50.649600-07:00"]
)
def test_a_timestamp_with_an_offset_is_left_alone(ts):
    event = {"timestamp": ts}
    assert mark_utc(event) is event


@pytest.mark.parametrize("event", [{}, {"timestamp": None}, {"timestamp": "soon"}])
def test_missing_or_unparseable_timestamps_are_left_alone(event):
    assert mark_utc(event) is event


def test_a_frame_carrying_an_event_is_rewritten():
    frame = json.dumps({"kind": "ACPToolCallEvent", "timestamp": "2026-11-22T10:11:50"})
    assert json.loads(mark_utc_frame(frame))["timestamp"] == "2026-11-22T10:11:50Z"


@pytest.mark.parametrize(
    "frame",
    [
        "not json",
        "[1, 2]",
        json.dumps({"kind": "ConversationStateUpdateEvent"}),
        json.dumps({"timestamp": "2026-11-22T10:11:50Z"}),
    ],
)
def test_other_frames_pass_through_unchanged(frame):
    assert mark_utc_frame(frame) is frame
