"""Entry point of an automation run.

Not imported by the server: it is shipped to the run's sandbox as `run.py` and
executed there by the automation service (`automation/execution.py`), with the
run described in the environment (`automation/dispatcher.py`). Standard
library only, so a sandbox needs nothing installed and no package index.

The app server starts the conversation, so a run uses the same agent and
credentials as a conversation started from the UI and shows up beside them.
"""

import json
import os
import sys
import time
import urllib.request

HERE = os.path.dirname(os.path.abspath(__file__))
POLL_SECONDS = 5
# How long a new conversation may sit idle before it counts as never started.
START_GRACE_SECONDS = 120
FAILED_STATES = ("error", "stuck", "paused", "waiting_for_confirmation", "deleting")


def call(method: str, url: str, key: str, body: dict | None = None) -> dict:
    request = urllib.request.Request(
        url,
        method=method,
        data=None if body is None else json.dumps(body).encode(),
        headers={"X-Session-API-Key": key, "Content-Type": "application/json"},
    )
    with urllib.request.urlopen(request, timeout=120) as response:
        return json.loads(response.read() or b"{}")


def wait(agent_url: str, conversation_id: str, key: str) -> str | None:
    """Block until the conversation stops; the reason it failed, or None."""
    started = time.monotonic()
    ran = False
    while True:
        info = call("GET", f"{agent_url}/api/conversations/{conversation_id}", key)
        status = info.get("execution_status")
        if status == "running":
            ran = True
        elif status == "finished" or (status == "idle" and ran):
            return None
        elif status in FAILED_STATES:
            return f"conversation ended {status}"
        elif time.monotonic() - started > START_GRACE_SECONDS:
            return f"conversation never started (status {status})"
        time.sleep(POLL_SECONDS)


def main() -> int:
    with open(os.path.join(HERE, "run.json")) as f:
        config = json.load(f)
    # The tarball is unpacked into the agent's working directory.
    for name in ("run.json", "run.py"):
        os.remove(os.path.join(HERE, name))

    key = os.environ.get("SESSION_API_KEY") or os.environ["OH_SESSION_API_KEYS_0"]
    base = f"{config['webhook_url']}/sandboxes/{os.environ['SANDBOX_ID']}/automation"
    agent_url = f"http://127.0.0.1:{config['agent_server_port']}"
    run_id = os.environ["AUTOMATION_RUN_ID"]
    payload = json.loads(os.environ.get("AUTOMATION_EVENT_PAYLOAD") or "{}")

    result: dict = {"status": "FAILED", "run_id": run_id}
    try:
        started = call(
            "POST",
            f"{base}/conversations",
            key,
            {
                "automation": payload.get("automation_name"),
                "event": payload.get("event"),
                "follow_up_turns": payload.get("follow_up_turns"),
                "conversation_id": os.environ.get("AUTOMATION_CONVERSATION_ID"),
            },
        )
        result["conversation_id"] = started["id"]
        error = wait(agent_url, started["id"], key)
        if error:
            result["error"] = error
        else:
            result["status"] = "COMPLETED"
    except Exception as e:
        result["error"] = f"{type(e).__name__}: {e}"

    print(json.dumps(result))
    try:
        call("POST", f"{base}/runs/{run_id}/complete", key, result)
    except Exception as e:
        # The automation service falls back to this command's exit code.
        print(f"completion callback failed: {e}", file=sys.stderr)
    return 0 if result["status"] == "COMPLETED" else 1


if __name__ == "__main__":
    sys.exit(main())
