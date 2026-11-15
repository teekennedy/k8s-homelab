from collections import Counter

from conftest import tone, upload, wav_bytes, words

from wake_word_trainer.db import split_for
from wake_word_trainer.matching import suspicious, wake_candidates


def only_clip(app):
    rows = app.state.store.query("SELECT * FROM clips")
    assert len(rows) == 1
    return rows[0]


# ------------------------------------------------------------------ matching


def test_whisper_spellings_after_okay_are_the_wake_word():
    for name in ("GLaDOS", "Gladys.", "Blados", "Claros", "glad", "blotters"):
        assert wake_candidates(words("Okay,", 1.0, 1.3, name, 1.3, 1.9)), name


def test_glados_without_okay_is_not_a_candidate_but_is_suspicious():
    w = words("I'm", 0.0, 0.2, "so", 0.2, 0.4, "glad", 0.4, 0.8)
    assert wake_candidates(w) == []
    assert suspicious(w) == ["glad @ 0.4s"]


def test_okay_followed_by_other_word_is_not_a_candidate():
    assert wake_candidates(words("okay", 0, 0.3, "google", 0.3, 0.8)) == []


def test_one_whisper_word_holding_both_tokens():
    [c] = wake_candidates(words("okay glados", 2.0, 3.0))
    assert (c.t0, c.t1) == (2.0, 3.0)


# -------------------------------------------------------------------- splits


def test_split_is_fixed_by_id_and_roughly_60_20_20():
    assert split_for("abc") == split_for("abc")
    counts = Counter(split_for(f"{i:032x}") for i in range(5000))
    assert 0.55 < counts["training"] / 5000 < 0.65
    assert 0.15 < counts["validation"] / 5000 < 0.25
    assert 0.15 < counts["testing"] / 5000 < 0.25


# ------------------------------------------------------------------- capture


def test_capture_needs_the_token(client):
    assert upload(client, "detected", token="wrong").status_code == 401
    r = client.post(
        "/capture", content=wav_bytes(tone(1)), headers={"X-Label": "detected"}
    )
    assert r.status_code == 401


def test_capture_stores_the_clip(client, app, settings):
    assert upload(client, "detected").status_code == 204
    clip = only_clip(app)
    assert clip["label"] == clip["label_sent"] == "detected"
    assert clip["device"] == "voice-pe"
    assert clip["review"] == "pending"
    assert abs(clip["duration_s"] - 12.0) < 1e-6
    assert (settings.clips_dir / clip["path"]).exists()


def test_root_path_still_accepts_captures(client):
    r = client.post(
        "/",
        content=wav_bytes(tone(2)),
        headers={
            "X-Label": "false_reject",
            "X-Device": "d",
            "X-Capture-Token": "capture-secret",
        },
    )
    assert r.status_code == 204


def test_capture_rejects_bad_audio_and_labels(client):
    assert upload(client, "nonsense").status_code == 400
    assert upload(client, "detected", seconds=61).status_code == 400
    r = client.post(
        "/capture",
        content=wav_bytes(tone(1), rate=44100),
        headers={"X-Label": "detected", "X-Capture-Token": "capture-secret"},
    )
    assert r.status_code == 400
    r = client.post(
        "/capture",
        content=b"not audio",
        headers={"X-Label": "detected", "X-Capture-Token": "capture-secret"},
    )
    assert r.status_code == 400


def relabel(client, label, device="voice-pe"):
    return client.post(
        "/capture",
        content=b"",
        headers={
            "X-Relabel-Last": label,
            "X-Device": device,
            "X-Capture-Token": "capture-secret",
        },
    )


def test_relabel_last_moves_the_newest_detection(client, app):
    upload(client, "detected")
    assert relabel(client, "false_accept").status_code == 204
    clip = only_clip(app)
    assert clip["label"] == "false_accept"
    assert clip["label_sent"] == "detected"
    assert clip["status"] == "pending"


def test_relabel_last_404_without_a_recent_detection(client, app):
    assert relabel(client, "false_accept").status_code == 404
    upload(client, "detected", device="other")
    assert relabel(client, "false_accept").status_code == 404
    upload(client, "detected")
    app.state.store._conn.execute("UPDATE clips SET received_at = received_at - 601")
    assert relabel(client, "false_accept").status_code == 404


def test_upload_discards_true_rejects_from_the_previous_30_seconds(client, app):
    store = app.state.store
    upload(client, "true_reject")
    store._conn.execute("UPDATE clips SET received_at = received_at - 40")
    upload(client, "true_reject")
    upload(client, "true_reject", device="other")
    upload(client, "detected")
    rows = store.query("SELECT device, label FROM clips ORDER BY received_at")
    assert [(r["device"], r["label"]) for r in rows] == [
        ("voice-pe", "true_reject"),  # 40 s before: kept
        ("voice-pe", "discarded"),  # probably holds the wake word
        ("other", "true_reject"),  # another device
        ("voice-pe", "detected"),
    ]


# ------------------------------------------------------------- processing


def process(app, transcriber, *word_lists):
    transcriber.queue.extend(word_lists)
    while app.state.processor.run_once():
        pass


def test_detected_is_cut_to_lead_in_plus_word(client, app, transcriber, settings):
    upload(client, "detected")
    process(
        app,
        transcriber,
        words("turn", 3.0, 3.4, "Okay,", 10.4, 10.8, "Gladys.", 10.8, 11.6),
    )
    clip = only_clip(app)
    assert clip["review"] == "auto"
    [d] = clip["derived"]
    assert d["kind"] == "positive"
    assert (d["t0"], d["t1"]) == (8.9, 11.75)
    assert (settings.clips_dir / d["path"]).exists()


def test_detected_without_wake_word_at_end_is_flagged(client, app, transcriber):
    upload(client, "detected")
    process(
        app, transcriber, words("okay", 2.0, 2.3, "glados", 2.3, 2.9, "what", 5, 5.3)
    )
    clip = only_clip(app)
    assert clip["review"] == "flagged"
    assert "last 2.5 s" in clip["flag_reason"]
    assert app.state.store.usable_clips() == []


def test_false_reject_flags_zero_or_several_candidates(client, app, transcriber):
    upload(client, "false_reject")
    upload(client, "false_reject")
    upload(client, "false_reject")
    process(
        app,
        transcriber,
        words("okay", 4.0, 4.3, "glados", 4.3, 4.9),
        [],
        words(
            "okay", 1.0, 1.3, "glados", 1.3, 1.9, "okay", 6.0, 6.3, "Gladys", 6.3, 6.9
        ),
    )
    rows = app.state.store.query("SELECT * FROM clips ORDER BY received_at")
    assert [r["review"] for r in rows] == ["auto", "flagged", "flagged"]
    assert len(rows[0]["derived"]) == 1
    assert rows[0]["derived"][0]["t0"] == 2.5
    assert rows[1]["flag_reason"] == "no wake word found"
    assert rows[2]["flag_reason"] == "2 wake word candidates"


def force_split(app, split):
    app.state.store._conn.execute("UPDATE clips SET split = ?", (split,))


def test_false_accept_training_split_becomes_hard_negative_and_chunks(
    client, app, transcriber
):
    upload(client, "false_accept")
    force_split(app, "training")
    process(app, transcriber, words("okay", 10.0, 10.3, "glasses", 10.3, 10.9))
    clip = only_clip(app)
    kinds = [(d["kind"], d["t0"], d["t1"]) for d in clip["derived"]]
    assert kinds[0] == ("negative", 8.8, 12.0)
    assert kinds[1:] == [("negative", 0.0, 3.0), ("negative", 3.0, 6.0)]
    # "glasses" starts with "gl": exactly the case a reviewer should hear.
    assert clip["review"] == "flagged"


def test_false_accept_heldout_split_is_kept_whole(client, app, transcriber):
    upload(client, "false_accept")
    force_split(app, "testing")
    process(app, transcriber, words("the", 1.0, 1.2, "weather", 1.2, 1.6))
    clip = only_clip(app)
    assert clip["review"] == "auto"
    assert [d["kind"] for d in clip["derived"]] == ["ambient"]
    assert clip["derived"][0]["path"] == clip["path"]


def test_true_reject_near_silent_is_flagged(client, app, transcriber):
    upload(client, "true_reject", samples=tone(12, amplitude=10))
    force_split(app, "training")
    process(app, transcriber, [])
    clip = only_clip(app)
    assert clip["review"] == "flagged"
    assert "near-silent" in clip["flag_reason"]
    assert len(clip["derived"]) == 4


def test_processing_error_is_recorded_and_queue_continues(
    client, app, transcriber, settings
):
    upload(client, "detected")
    upload(client, "detected")
    first = app.state.store.query("SELECT * FROM clips ORDER BY received_at")[0]
    (settings.clips_dir / first["path"]).unlink()
    process(app, transcriber, words("okay", 10.5, 10.8, "glados", 10.8, 11.5))
    rows = app.state.store.query("SELECT status FROM clips ORDER BY received_at")
    assert [r["status"] for r in rows] == ["error", "processed"]


# ------------------------------------------------------------------ review


def test_relabel_by_reviewer_reprocesses_and_confirms(client, app, transcriber):
    upload(client, "false_accept")
    process(
        app,
        transcriber,
        words("okay", 10.5, 10.8, "glados", 10.8, 11.5, "stop", 11.6, 11.9),
    )
    clip = only_clip(app)
    assert clip["review"] == "flagged"
    r = client.post(
        f"/clips/{clip['id']}/review",
        data={"action": "relabel", "label": "detected", "back": "/review"},
        headers={"X-Forwarded-Preferred-Username": "alice"},
        follow_redirects=False,
    )
    assert r.status_code == 303
    process(
        app,
        transcriber,
        words("okay", 10.5, 10.8, "glados", 10.8, 11.5, "stop", 11.6, 11.9),
    )
    clip = only_clip(app)
    assert clip["label"] == "detected"
    assert clip["review"] == "confirmed"
    assert clip["reviewed_by"] == "alice"
    assert [d["kind"] for d in clip["derived"]] == ["positive"]


def test_confirm_and_discard(client, app, transcriber):
    upload(client, "detected")
    process(app, transcriber, [])
    clip = only_clip(app)
    client.post(f"/clips/{clip['id']}/review", data={"action": "confirm"})
    assert only_clip(app)["review"] == "confirmed"
    assert len(app.state.store.usable_clips()) == 1
    client.post(f"/clips/{clip['id']}/review", data={"action": "discard"})
    assert only_clip(app)["review"] == "discarded"
    assert app.state.store.usable_clips() == []


def test_review_redirect_stays_on_site(client, app, transcriber):
    upload(client, "detected")
    clip = only_clip(app)
    r = client.post(
        f"/clips/{clip['id']}/review",
        data={"action": "discard", "back": "//evil.example"},
        follow_redirects=False,
    )
    assert r.headers["location"] == "/review"


def test_pages_render(client, app, transcriber):
    upload(client, "detected")
    process(app, transcriber, words("okay", 10.5, 10.8, "glados", 10.8, 11.5))
    clip = only_clip(app)
    for path in ("/", "/review", "/clips", f"/clips/{clip['id']}", "/runs", "/healthz"):
        assert client.get(path).status_code == 200, path
    assert client.get(f"/clips/{clip['id']}/audio").content[:4] == b"RIFF"
    assert client.get(f"/clips/{clip['id']}/audio?n=0").content[:4] == b"RIFF"
    assert client.get(f"/clips/{clip['id']}/audio?n=5").status_code == 404


def test_metrics(client, app, transcriber):
    upload(client, "detected")
    text = client.get("/metrics").text
    assert 'wake_word_trainer_clips{label="detected",review="pending"} 1' in text
    assert "wake_word_trainer_queue_depth 1" in text
    assert "wake_word_trainer_last_capture_timestamp_seconds" in text
