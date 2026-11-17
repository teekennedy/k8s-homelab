import json

import pytest
from conftest import upload, words

from wake_word_trainer import kube, trainer

INTERNAL = {"X-Internal-Token": "internal-secret"}


def usable(client, app, transcriber, label, n):
    for _ in range(n):
        upload(client, label)
    transcriber.queue.extend(
        [words("okay", 10.5, 10.8, "glados", 10.8, 11.5)] * n
        if label in ("detected", "false_reject")
        else [[]] * n
    )
    while app.state.processor.run_once():
        pass


def start(client, forced=False):
    r = client.post("/internal/runs", json={"forced": forced}, headers=INTERNAL)
    assert r.status_code == 200
    return r.json()


def test_internal_api_needs_its_token(client):
    assert client.post("/internal/runs", json={}).status_code == 401
    r = client.post(
        "/internal/runs", json={}, headers={"X-Internal-Token": "capture-secret"}
    )
    assert r.status_code == 401


def test_gate_waits_for_enough_new_positives(client, app, transcriber):
    usable(client, app, transcriber, "detected", 19)
    usable(client, app, transcriber, "true_reject", 30)
    out = start(client)
    assert out["run"] is None
    assert out["gate"]["reason"].startswith("19/20 new positives")
    usable(client, app, transcriber, "false_reject", 1)
    out = start(client)
    assert out["run"]["status"] == "running"
    assert out["gate"]["reason"] == "20 new positives"


def test_gate_starts_on_false_accepts(client, app, transcriber):
    usable(client, app, transcriber, "false_accept", 10)
    assert start(client)["gate"]["reason"] == "10 new false accepts"


def test_gate_one_run_at_a_time_and_one_per_night(client, app, transcriber):
    usable(client, app, transcriber, "detected", 20)
    run = start(client)["run"]
    assert "still running" in start(client, forced=True)["gate"]["reason"]
    client.post(
        f"/internal/runs/{run['id']}/result",
        json={"status": "failed", "error": "x"},
        headers=INTERNAL,
    )
    assert "last day" in start(client)["gate"]["reason"]
    assert start(client, forced=True)["run"] is not None


def test_abandoned_run_does_not_block_forever(client, app, settings):
    run = start(client, forced=True)["run"]
    app.state.store._conn.execute(
        "UPDATE runs SET started_at = started_at - ?",
        (settings.run_timeout_hours * 3600 + 1,),
    )
    assert start(client, forced=True)["run"] is not None
    assert app.state.store.run(run["id"])["status"] == "failed"


def test_dataset_and_result_mark_clips_used(client, app, transcriber):
    usable(client, app, transcriber, "detected", 20)
    upload(client, "detected")  # not processed yet: not in the dataset
    run = start(client)["run"]
    rows = client.get(f"/internal/runs/{run['id']}/dataset", headers=INTERNAL).json()[
        "clips"
    ]
    assert len(rows) == 20
    assert {r["kind"] for r in rows} == {"positive"}
    assert {r["split"] for r in rows} <= {"training", "validation", "testing"}
    result = {"promote": True, "decision": "better", "candidate": {"recorded_frr": 0.1}}
    r = client.post(
        f"/internal/runs/{run['id']}/result",
        json={
            "status": "succeeded",
            "result": result,
            "clip_ids": [r["clip_id"] for r in rows],
        },
        headers=INTERNAL,
    )
    assert r.status_code == 200
    assert app.state.store.new_usable_counts() == {}
    saved = app.state.store.run(run["id"])
    assert saved["promoted"] == 1 and saved["result"]["decision"] == "better"
    client.post(
        f"/internal/runs/{run['id']}/published",
        json={"published": True},
        headers=INTERNAL,
    )
    assert app.state.store.run(run["id"])["published"]["published"] is True
    for path in ("/runs", f"/runs/{run['id']}", "/"):
        assert client.get(path).status_code == 200
    assert (
        "wake_word_trainer_last_promotion_timestamp_seconds"
        in client.get("/metrics").text
    )


def test_failed_run_does_not_mark_clips_used(client, app, transcriber):
    usable(client, app, transcriber, "detected", 20)
    run = start(client)["run"]
    ids = [c["id"] for c in app.state.store.usable_clips()]
    client.post(
        f"/internal/runs/{run['id']}/result",
        json={"status": "failed", "error": "boom", "clip_ids": ids},
        headers=INTERNAL,
    )
    assert app.state.store.new_usable_counts() == {"detected": 20}


def test_run_files_are_served_inside_the_run_only(client, settings):
    run = start(client, forced=True)["run"]
    d = settings.runs_dir / run["id"] / "report"
    d.mkdir(parents=True)
    (d / "REPORT.md").write_text("# report")
    (settings.runs_dir / "secret.txt").write_text("no")
    assert client.get(f"/runs/{run['id']}/files/report/REPORT.md").text == "# report"
    assert client.get(f"/runs/{run['id']}/files/../secret.txt").status_code == 404
    assert (
        client.get(
            f"/runs/{run['id']}/files/report/%2e%2e/%2e%2e/secret.txt"
        ).status_code
        == 404
    )
    assert "# report" in client.get(f"/runs/{run['id']}").text


def test_train_now_creates_a_job(client, monkeypatch):
    calls = []
    monkeypatch.setattr(
        kube, "create_training_job", lambda ns, cj: calls.append((ns, cj)) or "job"
    )
    r = client.post("/train-now", follow_redirects=False)
    assert r.status_code == 303
    assert calls == [("wake-word-trainer", "wake-word-trainer-nightly")]


# ----------------------------------------------------------- trainer logic


def test_decide():
    good = {"operating": {"cutoff": 0.9}, "faph": 0.05, "recorded_frr": 0.2}
    assert trainer.decide(good, None, 0.1)[0] is True
    assert trainer.decide(good, {"recorded_frr": 0.25}, 0.1)[0] is True
    assert trainer.decide(good, {"recorded_frr": 0.2}, 0.1)[0] is False
    assert trainer.decide({**good, "faph": 0.12}, None, 0.1)[0] is False
    assert trainer.decide({**good, "operating": None}, None, 0.1)[0] is False
    assert trainer.decide({**good, "recorded_frr": None}, None, 0.1)[0] is False


def test_stage_boundaries():
    args = trainer.DEFAULT_TRAIN_ARGS.split()
    assert trainer.stage_boundaries(args) == ["18000"]
    assert trainer.stage_boundaries(["--steps", "1", "2", "3", "--x"]) == ["1", "3"]


def test_publish_archives_then_replaces_manifest_last(tmp_path):
    model, target = tmp_path / "model", tmp_path / "public"
    model.mkdir()
    target.mkdir()
    (model / "okay_glados.tflite").write_bytes(b"new")
    (model / "okay_glados.json").write_text('{"v": "new"}')
    (target / "okay_glados.tflite").write_bytes(b"old")
    (target / "okay_glados.json").write_text('{"v": "old"}')
    archived = trainer.publish_files(model, target, "run-1")
    assert len(archived) == 2
    assert (target / "previous" / "run-1" / "okay_glados.tflite").read_bytes() == b"old"
    assert (target / "okay_glados.tflite").read_bytes() == b"new"
    assert json.loads((target / "okay_glados.json").read_text()) == {"v": "new"}
    assert not list(target.glob(".*.tmp"))


def test_publish_refuses_an_incomplete_model(tmp_path):
    (tmp_path / "m").mkdir()
    (tmp_path / "m" / "okay_glados.tflite").write_bytes(b"x")
    (tmp_path / "t").mkdir()
    with pytest.raises(FileNotFoundError):
        trainer.publish_files(tmp_path / "m", tmp_path / "t", "r")
    assert list((tmp_path / "t").iterdir()) == []


def test_publish_without_promotion_does_nothing(tmp_path, monkeypatch):
    handoff = tmp_path / "run.json"
    handoff.write_text(json.dumps({"run_id": "r", "promote": False}))
    monkeypatch.setenv("WWT_HANDOFF", str(handoff))
    monkeypatch.setenv("WWT_PUBLISHED_DIR", str(tmp_path / "pub"))
    assert trainer.publish(trainer.TrainerConfig(), dry_run=False, live_dir=None) == 0
    assert not (tmp_path / "pub").exists()


def test_job_from_cronjob_forces_every_container():
    cj = {
        "metadata": {"name": "nightly", "namespace": "ns", "uid": "u1"},
        "spec": {
            "jobTemplate": {
                "metadata": {"labels": {"a": "b"}},
                "spec": {
                    "template": {
                        "spec": {
                            "initContainers": [
                                {
                                    "name": "train",
                                    "env": [{"name": "WWT_FORCE", "value": "0"}],
                                }
                            ],
                            "containers": [{"name": "publish"}],
                        }
                    }
                },
            }
        },
    }
    job = kube.job_from_cronjob(cj, "nightly-manual-1", {"WWT_FORCE": "1"})
    pod = job["spec"]["template"]["spec"]
    assert pod["initContainers"][0]["env"] == [{"name": "WWT_FORCE", "value": "1"}]
    assert pod["containers"][0]["env"] == [{"name": "WWT_FORCE", "value": "1"}]
    assert job["metadata"]["ownerReferences"][0]["uid"] == "u1"
    assert job["metadata"]["labels"] == {"a": "b"}
    # The CronJob itself is untouched.
    assert cj["spec"]["jobTemplate"]["spec"]["template"]["spec"]["containers"][0] == {
        "name": "publish"
    }


def test_train_waits_for_the_seed(tmp_path, monkeypatch):
    monkeypatch.setenv("OKAY_GLADOS_WORK_DIR", str(tmp_path / "work"))
    monkeypatch.setenv("WWT_HANDOFF", str(tmp_path / "run.json"))
    calls = []
    monkeypatch.setattr(trainer, "api", lambda *a, **k: calls.append(a))
    assert trainer.train(trainer.TrainerConfig()) == 0
    assert calls == []
    assert json.loads((tmp_path / "run.json").read_text())["run_id"] is None


def finish(client, run_id, promote):
    client.post(
        f"/internal/runs/{run_id}/result",
        json={"status": "succeeded", "result": {"promote": promote}},
        headers=INTERNAL,
    )


def pending(client):
    return client.get("/internal/publish", headers=INTERNAL).json()["run"]


def test_only_the_newest_promotion_is_waiting_to_publish(client, app):
    store = app.state.store
    older = start(client, forced=True)["run"]["id"]
    finish(client, older, promote=True)
    assert pending(client)["id"] == older
    newer = start(client, forced=True)["run"]["id"]
    finish(client, newer, promote=True)
    assert pending(client)["id"] == newer
    # A later run that keeps the published model leaves the promotion pending.
    kept = start(client, forced=True)["run"]["id"]
    finish(client, kept, promote=False)
    assert pending(client)["id"] == newer
    assert "wake_word_trainer_publish_pending 1" in client.get("/metrics").text

    store.set_published(newer, {"published": False, "error": "disk full", "at": 1})
    assert pending(client)["id"] == newer
    store.set_published(newer, {"published": True, "at": 2})
    assert pending(client) is None
    assert "wake_word_trainer_publish_pending 0" in client.get("/metrics").text


def test_publish_pending_publishes_from_the_run_folder(tmp_path, monkeypatch):
    monkeypatch.setenv("WWT_RUNS_DIR", str(tmp_path / "runs"))
    monkeypatch.setenv("WWT_PUBLISHED_DIR", str(tmp_path / "pub"))
    report = tmp_path / "runs" / "r1" / "report"
    report.mkdir(parents=True)
    for name in trainer.MODEL_FILES:
        (report / name).write_text(name)
    (tmp_path / "pub").mkdir()
    queue = [{"run": {"id": "r1", "published": None}}]
    calls = []

    def api(cfg, method, path, body=None):
        calls.append((method, path, body))
        return queue.pop(0) if method == "GET" else {"ok": True}

    monkeypatch.setattr(trainer, "api", api)
    cfg = trainer.TrainerConfig()
    assert trainer.publish_pending(cfg, False, None, 900, now=100) == "r1"
    assert (tmp_path / "pub" / "okay_glados.json").read_text() == "okay_glados.json"
    assert calls[-1][1] == "/internal/runs/r1/published"
    assert calls[-1][2]["published"] is True

    # A failed attempt waits out the retry delay.
    failed = {"published": False, "error": "x", "at": 50}
    queue.append({"run": {"id": "r1", "published": failed}})
    assert trainer.publish_pending(cfg, False, None, 900, now=100) is None
    queue.append({"run": {"id": "r1", "published": failed}})
    assert trainer.publish_pending(cfg, False, None, 900, now=1000) == "r1"

    queue.append({"run": None})
    assert trainer.publish_pending(cfg, False, None, 900, now=2000) is None


def test_train_without_a_handoff_writes_none(tmp_path, monkeypatch):
    monkeypatch.setenv("OKAY_GLADOS_WORK_DIR", str(tmp_path / "work"))
    monkeypatch.delenv("WWT_HANDOFF", raising=False)
    monkeypatch.setattr(trainer, "api", lambda *a, **k: None)
    assert trainer.train(trainer.TrainerConfig()) == 0
    assert list(tmp_path.iterdir()) == []


def test_watch_beats_even_when_a_pass_fails(tmp_path, monkeypatch):
    beat = tmp_path / "heartbeat"

    def broken(*a, **k):
        raise OSError("collector down")

    class Stop(Exception):
        pass

    def sleep(_):
        raise Stop

    monkeypatch.setattr(trainer, "publish_pending", broken)
    monkeypatch.setattr(trainer.time, "sleep", sleep)
    with pytest.raises(Stop):
        trainer.watch(trainer.TrainerConfig(), False, None, 60, 900, beat)
    assert beat.exists()


def test_check_heartbeat(tmp_path):
    from wake_word_trainer.__main__ import heartbeat_ok

    beat = tmp_path / "heartbeat"
    assert not heartbeat_ok(beat, 0, 1000)
    beat.touch()
    mtime = beat.stat().st_mtime
    assert heartbeat_ok(beat, 0, mtime + 10**6)
    assert heartbeat_ok(beat, 900, mtime + 899)
    assert not heartbeat_ok(beat, 900, mtime + 901)
