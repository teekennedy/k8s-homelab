"""Unit tests for the Nix json-log-path metrics parser.

The event fixtures here are verbatim shapes captured from a real
``nix build --json-log-path`` run on nixos/nix 2.35.2 -- the same upstream Nix
that Determinate Nix 3.22.3 on the borg hosts is built from. They are the
contract this parser depends on; the Dagger check
``validate-nix-build-metrics`` re-derives them from a live Nix so this file
cannot silently go stale.
"""

import json
import os
from pathlib import Path

import nix_build_metrics as nbm

FIXTURES = Path(__file__).parent / "fixtures"


def stream(*events):
    """Render events the way Nix writes them: one bare JSON object per line."""
    return [json.dumps(e) for e in events]


def build_start(act_id, drv, machine=""):
    return {
        "action": "start",
        "fields": [drv, machine, 1, 1],
        "id": act_id,
        "level": 3,
        "parent": 0,
        "text": f"building '{drv}'",
        "type": nbm.ACT_BUILD,
    }


def substitute_start(act_id, path, substituter):
    return {
        "action": "start",
        "fields": [path, substituter],
        "id": act_id,
        "level": 0,
        "parent": 0,
        "text": "",
        "type": nbm.ACT_SUBSTITUTE,
    }


def copy_start(act_id, path, source, parent=0):
    return {
        "action": "start",
        "fields": [path, source, "local"],
        "id": act_id,
        "level": 3,
        "parent": parent,
        "text": f"copying path '{path}' from '{source}'",
        "type": nbm.ACT_COPY_PATH,
    }


def transfer_start(act_id, uri, parent):
    return {
        "action": "start",
        "fields": [uri],
        "id": act_id,
        "level": 4,
        "parent": parent,
        "text": f"downloading '{uri}'",
        "type": nbm.ACT_FILE_TRANSFER,
    }


def progress(act_id, done, expected):
    return {
        "action": "result",
        "fields": [done, expected, 0, 0],
        "id": act_id,
        "type": nbm.RES_PROGRESS,
    }


CACHE = "https://cache.nixos.org"
PEER = "ssh-ng://nixbuilder@10.69.80.11"


def test_counts_local_builds():
    stats = nbm.parse_events(
        stream(
            build_start(1, "/nix/store/aaa.drv"),
            build_start(2, "/nix/store/bbb.drv"),
        )
    )
    assert stats.paths_built == {"local": 2}
    assert stats.parse_errors == 0


def test_remote_builder_is_labelled_by_machine():
    """A non-empty machine field means the derivation was built elsewhere."""
    stats = nbm.parse_events(
        stream(
            build_start(1, "/nix/store/aaa.drv"),
            build_start(2, "/nix/store/bbb.drv", machine="ssh://builder"),
        )
    )
    assert stats.paths_built == {"local": 1, "ssh://builder": 1}


def test_separates_public_cache_from_peer():
    """The whole point of the exporter: which cache did each path come from."""
    stats = nbm.parse_events(
        stream(
            substitute_start(1, "/nix/store/aaa", CACHE),
            substitute_start(2, "/nix/store/bbb", CACHE),
            substitute_start(3, "/nix/store/ccc", PEER),
        )
    )
    assert stats.paths_substituted == {CACHE: 2, PEER: 1}


def test_bytes_use_the_final_progress_value_not_the_sum():
    """resProgress is cumulative, so summing every event would multiply-count."""
    stats = nbm.parse_events(
        stream(
            copy_start(10, "/nix/store/aaa", CACHE),
            progress(10, 100, 300),
            progress(10, 200, 300),
            progress(10, 300, 300),
        )
    )
    assert stats.substituted_bytes == {CACHE: 300}


def test_wire_bytes_are_attributed_through_the_copy_path_parent():
    """A fileTransfer names only its URI; the substituter comes from its parent."""
    stats = nbm.parse_events(
        stream(
            substitute_start(1, "/nix/store/aaa", PEER),
            copy_start(2, "/nix/store/aaa", PEER, parent=1),
            transfer_start(3, "https://example/nar/aaa.nar.zst", parent=2),
            progress(2, 153696, 153696),
            progress(3, 76125, 76125),
        )
    )
    assert stats.substituted_bytes == {PEER: 153696}
    assert stats.downloaded_bytes == {PEER: 76125}


def test_parentless_transfers_are_not_attributed_to_any_substituter():
    """narinfo lookups and flake fetches are real traffic but not substitution."""
    stats = nbm.parse_events(
        stream(
            transfer_start(3, "https://cache.nixos.org/xyz.narinfo", parent=0),
            progress(3, 500, 500),
        )
    )
    assert stats.downloaded_bytes == {}
    assert stats.parse_errors == 0


def test_malformed_events_are_counted_not_raised():
    """A Nix upgrade that reshapes the stream must not fail a deploy."""
    stats = nbm.parse_events(
        [
            "not json at all",
            json.dumps({"action": "start", "type": nbm.ACT_BUILD}),
            json.dumps(
                {"action": "start", "type": nbm.ACT_SUBSTITUTE, "fields": ["p"]}
            ),
            json.dumps(
                {
                    "action": "start",
                    "type": nbm.ACT_BUILD,
                    "fields": ["/d.drv", 42, 1, 1],
                }
            ),
            json.dumps(build_start(9, "/nix/store/ok.drv")),
        ]
    )
    assert stats.parse_errors == 4
    # The one good event still lands, so metrics degrade rather than vanish.
    assert stats.paths_built == {"local": 1}


def test_blank_lines_are_not_errors():
    stats = nbm.parse_events(["", "   ", json.dumps(build_start(1, "/d.drv"))])
    assert stats.parse_errors == 0


def test_render_emits_zero_local_builds_rather_than_dropping_the_series():
    """A run that built nothing is an answer; a missing series is not."""
    out = nbm.render(nbm.Stats(), duration_seconds=12.0, succeeded=True)
    assert 'nixos_selfupdate_build_paths_built{machine="local"} 0' in out


def test_render_reports_failure_and_duration():
    out = nbm.render(nbm.Stats(), duration_seconds=95.6, succeeded=False)
    assert "nixos_selfupdate_build_success 0" in out
    assert "nixos_selfupdate_build_duration_seconds 96" in out


def test_render_escapes_label_values():
    stats = nbm.Stats()
    stats.paths_substituted['ssh://weird"host'] = 1
    out = nbm.render(stats, duration_seconds=0, succeeded=True)
    assert 'substituter="ssh://weird\\"host"' in out


def test_render_is_parseable_exposition():
    """Every non-comment line must be `name{labels} value`, or node-exporter
    rejects the whole file and takes the other collectors' metrics with it."""
    stats = nbm.parse_events(
        stream(
            build_start(1, "/nix/store/aaa.drv"),
            substitute_start(2, "/nix/store/bbb", CACHE),
            copy_start(3, "/nix/store/bbb", CACHE, parent=2),
            progress(3, 4096, 4096),
        )
    )
    out = nbm.render(stats, duration_seconds=3.0, succeeded=True)
    for line in out.splitlines():
        if line.startswith("#"):
            assert line.split()[1] in ("HELP", "TYPE")
            continue
        name, value = line.rsplit(" ", 1)
        assert name
        float(value)


def test_end_to_end_writes_the_output_file(tmp_path):
    log = tmp_path / "nix.json"
    log.write_text(
        "\n".join(
            stream(
                build_start(1, "/nix/store/aaa.drv"),
                substitute_start(2, "/nix/store/bbb", CACHE),
            )
        )
    )
    out = tmp_path / "metrics.prom"

    rc = nbm.main(
        [
            "report",
            "--json-log",
            str(log),
            "--output",
            str(out),
            "--duration-seconds",
            "42",
            "--exit-status",
            "0",
        ]
    )

    assert rc == 0
    text = out.read_text()
    assert 'nixos_selfupdate_build_paths_built{machine="local"} 1' in text
    assert (
        f'nixos_selfupdate_build_paths_substituted{{substituter="{CACHE}"}} 1' in text
    )
    assert "nixos_selfupdate_build_duration_seconds 42" in text
    assert "nixos_selfupdate_build_parse_errors 0" in text
    assert not (tmp_path / "metrics.prom.tmp").exists()


def test_missing_log_still_writes_metrics(tmp_path):
    """The build can die before Nix ever opens the log; that is still a run."""
    out = tmp_path / "metrics.prom"
    rc = nbm.main(
        [
            "report",
            "--json-log",
            str(tmp_path / "absent.json"),
            "--output",
            str(out),
            "--exit-status",
            "1",
        ]
    )
    assert rc == 0
    assert "nixos_selfupdate_build_success 0" in out.read_text()


def stop(act_id):
    return {"action": "stop", "id": act_id}


def sidecar(*records):
    """Render a timings sidecar the way the follower writes it."""
    return [f"{t:.6f} {action} {act_id}" for t, action, act_id in records]


def test_timings_are_joined_onto_the_activity_that_carries_the_label():
    """Durations arrive keyed only by activity id; the labels come from the log."""
    timings = nbm.read_timings(
        sidecar(
            (100.0, "start", 1),
            (104.5, "stop", 1),
            (100.0, "start", 2),
            (100.25, "stop", 2),
        )
    )
    stats = nbm.parse_events(
        stream(
            build_start(1, "/nix/store/aaa.drv"),
            substitute_start(2, "/nix/store/bbb", CACHE),
        ),
        timings,
    )
    assert stats.build_seconds == {"local": 4.5}
    assert stats.substitute_seconds == {CACHE: 0.25}
    assert stats.have_timings


def test_unfinished_activities_are_dropped_rather_than_guessed():
    """A start with no stop was still running when the follower was stopped."""
    timings = nbm.read_timings(
        sidecar((100.0, "start", 1), (100.0, "start", 2), (103.0, "stop", 2))
    )
    assert timings == {"2": 3.0}


def test_malformed_sidecar_records_are_skipped():
    timings = nbm.read_timings(
        ["", "garbage", "not-a-number start 1", "100.0 start 5", "101.5 stop 5"]
    )
    assert timings == {"5": 1.5}


def test_duration_metrics_are_omitted_when_no_timings_were_captured():
    """Zero seconds and "nobody was watching" are different answers."""
    stats = nbm.parse_events(stream(build_start(1, "/nix/store/aaa.drv")))
    out = nbm.render(stats, duration_seconds=5, succeeded=True)
    assert "nixos_selfupdate_build_timings_available 0" in out
    assert "nixos_selfupdate_build_seconds" not in out
    assert "nixos_selfupdate_build_substitute_seconds" not in out


def test_duration_metrics_are_emitted_when_timings_exist():
    stats = nbm.parse_events(
        stream(build_start(1, "/nix/store/aaa.drv")),
        nbm.read_timings(sidecar((10.0, "start", 1), (12.5, "stop", 1))),
    )
    out = nbm.render(stats, duration_seconds=5, succeeded=True)
    assert "nixos_selfupdate_build_timings_available 1" in out
    assert 'nixos_selfupdate_build_seconds{machine="local"} 2.500' in out


class FakeLog:
    """A growing file: readline() returns "" at EOF and more data appears later."""

    def __init__(self, batches):
        self.batches = list(batches)
        self.lines = []

    def readline(self):
        if not self.lines and self.batches:
            self.lines = list(self.batches.pop(0))
        return self.lines.pop(0) if self.lines else ""


def test_follower_stamps_events_as_they_arrive(monkeypatch):
    import io

    clock = iter([10.0, 11.0, 12.0, 13.0])
    monkeypatch.setattr(nbm.time, "time", lambda: next(clock))
    monkeypatch.setattr(nbm.time, "sleep", lambda _: None)

    log = FakeLog(
        [
            [json.dumps(build_start(1, "/d.drv")) + "\n"],
            [json.dumps(stop(1)) + "\n"],
        ]
    )
    sink = io.StringIO()
    # Stop once both batches have been handed out, so the drain pass sees EOF.
    nbm.follow_timings(log, sink, lambda: not log.batches, poll_seconds=0)

    assert sink.getvalue().splitlines() == [
        "10.000000 start 1",
        "11.000000 stop 1",
    ]


def test_follower_holds_back_a_partial_line(monkeypatch):
    """A half-written line must be completed, not charged to the wrong activity."""
    import io

    monkeypatch.setattr(nbm.time, "time", lambda: 42.0)
    monkeypatch.setattr(nbm.time, "sleep", lambda _: None)

    whole = json.dumps(build_start(7, "/d.drv")) + "\n"
    log = FakeLog([[whole[:20]], [whole[20:]]])
    sink = io.StringIO()
    nbm.follow_timings(log, sink, lambda: not log.batches, poll_seconds=0)

    assert sink.getvalue() == "42.000000 start 7\n"


def test_follower_ignores_non_activity_events(monkeypatch):
    import io

    monkeypatch.setattr(nbm.time, "time", lambda: 1.0)
    monkeypatch.setattr(nbm.time, "sleep", lambda _: None)

    log = FakeLog([[json.dumps(progress(1, 5, 10)) + "\n", "not json\n"]])
    sink = io.StringIO()
    nbm.follow_timings(log, sink, lambda: not log.batches, poll_seconds=0)

    assert sink.getvalue() == ""


def test_follower_substring_prefilter_tolerates_whitespace(monkeypatch):
    """The fast path keys on the action names, not on Nix's exact JSON spacing."""
    import io

    monkeypatch.setattr(nbm.time, "time", lambda: 3.0)
    monkeypatch.setattr(nbm.time, "sleep", lambda _: None)

    spaced = '{"action": "start", "type": 105, "id": 4, "fields": ["/d.drv", ""]}\n'
    log = FakeLog([[spaced]])
    sink = io.StringIO()
    nbm.follow_timings(log, sink, lambda: not log.batches, poll_seconds=0)

    assert sink.getvalue() == "3.000000 start 4\n"


def test_follower_prefilter_false_positive_is_discarded(monkeypatch):
    """A line that merely mentions an action name must not become a record."""
    import io

    monkeypatch.setattr(nbm.time, "time", lambda: 3.0)
    monkeypatch.setattr(nbm.time, "sleep", lambda _: None)

    chatty = json.dumps({"action": "msg", "level": 1, "msg": 'will "start" soon'})
    log = FakeLog([[chatty + "\n"]])
    sink = io.StringIO()
    nbm.follow_timings(log, sink, lambda: not log.batches, poll_seconds=0)

    assert sink.getvalue() == ""


def test_publish_writes_the_same_metrics_to_both_paths(tmp_path):
    """--output is the persistent copy, --publish the textfile collector one."""
    log = tmp_path / "nix.json"
    log.write_text("\n".join(stream(build_start(1, "/nix/store/aaa.drv"))))
    state = tmp_path / "state.prom"
    textfile = tmp_path / "textfile.prom"

    rc = nbm.main(
        [
            "report",
            "--json-log",
            str(log),
            "--output",
            str(state),
            "--publish",
            str(textfile),
        ]
    )

    assert rc == 0
    assert state.read_text() == textfile.read_text()
    assert 'nixos_selfupdate_build_paths_built{machine="local"} 1' in state.read_text()
    assert not (tmp_path / "textfile.prom.tmp").exists()


def parse_prom_samples(prom):
    """Read a textfile-collector exposition into series -> value."""
    samples = {}
    for line in prom.strip().splitlines():
        if not line or line.startswith("#"):
            continue
        name, value = line.rsplit(" ", 1)
        samples[name] = float(value)
    return samples


def test_real_build_produces_every_series_the_exporter_exists_for(tmp_path):
    """Assert a real build produced the series the exporter exists to
    produce. Each assertion names the internal-json activity the parser reads
    it from, so a failure here points at what moved in Nix's event stream
    rather than just saying the numbers are wrong.

    The json log and timings sidecar default to a fixture captured from a
    real ``nix build --json-log-path`` run; ``ValidateNixBuildMetrics`` in
    ``.dagger/main.go`` points both at a build it just ran instead, via
    environment variable, so this same contract is checked against current
    Nix in CI.
    """
    json_log = os.environ.get("NIX_BUILD_METRICS_LOG", str(FIXTURES / "nix-log.json"))
    timings_path = os.environ.get(
        "NIX_BUILD_METRICS_TIMINGS", str(FIXTURES / "timings.txt")
    )
    output = tmp_path / "metrics.prom"

    rc = nbm.main(
        [
            "report",
            "--json-log",
            json_log,
            "--timings",
            timings_path,
            "--output",
            str(output),
            "--duration-seconds",
            "1",
            "--exit-status",
            "0",
        ]
    )
    assert rc == 0
    samples = parse_prom_samples(output.read_text())

    cache = 'nixos_selfupdate_build_{}{{substituter="https://cache.nixos.org"}}'
    checks = [
        (
            'nixos_selfupdate_build_paths_built{machine="local"}',
            1,
            "actBuild (105) start events, fields[1]",
        ),
        (
            cache.format("paths_substituted"),
            1,
            "actSubstitute (108) start events, fields[1]",
        ),
        (
            cache.format("substituted_bytes"),
            1,
            "resProgress (105) results on actCopyPath (100)",
        ),
        (
            cache.format("downloaded_bytes"),
            1,
            "resProgress (105) results on actFileTransfer (101), attributed via parent",
        ),
        (
            "nixos_selfupdate_build_timings_available",
            1,
            "the timings follower having run alongside the build at all",
        ),
        # The probe's shell loop runs for around a second. A floor well above
        # the follower's 100ms poll proves the start and stop events were
        # seen as they arrived rather than in one burst at the end -- which
        # is what a follower that only woke up after the build would
        # produce, and it would report a duration near zero.
        (
            'nixos_selfupdate_build_seconds{machine="local"}',
            0.2,
            "start/stop arrival times paired by activity id",
        ),
        (
            cache.format("substitute_seconds"),
            0,
            "start/stop arrival times on actSubstitute (108)",
        ),
    ]
    for series, minimum, reads in checks:
        assert (
            series in samples
        ), f"{series} is missing; the parser reads it from {reads}"
        if minimum == 0:
            continue
        got = samples[series]
        assert (
            got >= minimum
        ), f"{series} is {got}, want >= {minimum}; the parser reads it from {reads}"

    # Checked last so the more specific failures above win: this only says
    # that something in the stream did not look the way the parser expects.
    assert samples["nixos_selfupdate_build_parse_errors"] == 0, (
        "parser reported unrecognised events; Nix's internal-json format has "
        "likely changed"
    )
