"""Turn a Nix ``json-log-path`` event stream into node-exporter textfile metrics.

The result answers a question the stream only hints at: of everything a build
needed, how much did it build itself, how much came from cache.nixos.org, and
how much came from a peer over the LAN.

The event stream is Nix's ``internal-json`` logger, which is explicitly internal
and carries no stability guarantee (NixOS/nix#13935). That shapes this whole
file: nothing here may abort a caller over it. Every event that does not look
the way this parser expects is counted in ``parse_errors`` and skipped, so a
Nix upgrade that reshapes the stream degrades the metrics instead of failing
the build that produced them.

Activity and result type numbers are from src/libutil/include/nix/util/logging.hh.

The stream carries no timestamps at all -- ``stop`` events are ``{action, id}``
and nothing else -- so durations have to be supplied from outside. The ``timings``
subcommand does that: it follows the log while the build runs and records when
each activity's start and stop event *arrived*, which the ``report`` subcommand
then joins back on by activity id.

Following a regular file, rather than having Nix write to a named pipe, is a
deliberate safety choice. Nix opens ``json-log-path`` at startup and blocks in
``open()`` until a reader appears, so a FIFO whose reader is late, crashed or
never started would hang every Nix process on the host -- and once running, a
reader that stops draining blocks the writer as soon as the pipe buffer fills.
A regular file cannot do either. Nix flushes every event (``std::endl``), so
following one costs only the poll interval in accuracy.
"""

import argparse
import json
import os
import signal
import sys
import time
from collections import defaultdict

# ActivityType, from Nix's logging.hh.
ACT_COPY_PATH = 100
ACT_FILE_TRANSFER = 101
ACT_BUILD = 105
ACT_SUBSTITUTE = 108

# ResultType. resProgress fields are [done, expected, running, failed]; the
# activity emits a stream of them and the last one is the final tally.
RES_PROGRESS = 105

# Label used for a derivation built by this host rather than handed to a remote
# builder. Nix reports that as an empty machine field, which is not a useful
# label value.
LOCAL = "local"


class Stats:
    """Everything the exporter reports about a single self-update run."""

    def __init__(self):
        # machine -> number of derivations built there
        self.paths_built = defaultdict(int)
        # substituter URI -> number of store paths fetched from it
        self.paths_substituted = defaultdict(int)
        # substituter URI -> bytes written into the local store
        self.substituted_bytes = defaultdict(int)
        # substituter URI -> bytes pulled over the network
        self.downloaded_bytes = defaultdict(int)
        # machine -> seconds spent building there, from the timings sidecar
        self.build_seconds = defaultdict(float)
        # substituter URI -> seconds spent substituting from it
        self.substitute_seconds = defaultdict(float)
        # Whether a timings sidecar was supplied and had anything in it. The
        # two _seconds maps are meaningless without it, and a zero is not the
        # same answer as "nobody was watching".
        self.have_timings = False
        self.parse_errors = 0


def _progress_done(fields):
    """Return the 'done' counter of a resProgress event, or None if malformed."""
    if not isinstance(fields, list) or len(fields) < 1:
        return None
    done = fields[0]
    if not isinstance(done, int) or isinstance(done, bool):
        return None
    return done


def read_timings(lines):
    """Read a timings sidecar into ``activity id -> elapsed seconds``.

    The sidecar is written by the ``timings`` subcommand: one
    ``<epoch> <action> <id>`` record per activity start and stop, in arrival
    order. Activity ids embed the writing process, so they do not collide
    between the several Nix processes a single nixos-rebuild spawns, and an
    id is never live twice at once within one process -- which is what makes
    pairing by id alone correct.

    A start with no stop is an activity that was still running when the
    follower was told to finish. It is dropped rather than charged an
    arbitrary end time.
    """
    started = {}
    elapsed = {}
    for line in lines:
        parts = line.split()
        if len(parts) != 3:
            continue
        stamp, action, act_id = parts
        try:
            stamp = float(stamp)
        except ValueError:
            continue
        if action == "start":
            started[act_id] = stamp
        elif action == "stop" and act_id in started:
            elapsed[act_id] = stamp - started.pop(act_id)
    return elapsed


def parse_events(lines, timings=None):
    """Fold a json-log-path event stream into a Stats.

    ``lines`` is any iterable of JSON text lines. Nix appends to the log across
    every process it spawns in one run -- ``nixos-rebuild`` invokes it more than
    once -- so a single file legitimately holds several interleaved sessions.
    Activity ids are unique within a session but not across them, which is why
    the maps below are keyed by id and simply overwritten: the last writer wins,
    and each activity's own results arrive while it is the live holder of its id.
    """
    stats = Stats()
    timings = timings or {}
    stats.have_timings = bool(timings)

    # Activity id -> the substituter a copyPath is pulling from. Populated by
    # copyPath starts and read by both copyPath results (store bytes) and
    # fileTransfer results (wire bytes), the latter reaching it through parent.
    copy_source = {}
    # Activity id -> latest 'done' value, so the final tally survives the stream
    # of intermediate progress events without holding all of them.
    copy_done = {}
    transfer_done = {}
    # fileTransfer activity id -> its parent copyPath id.
    transfer_parent = {}

    for line in lines:
        line = line.strip()
        if not line:
            continue
        try:
            event = json.loads(line)
        except json.JSONDecodeError:
            stats.parse_errors += 1
            continue
        if not isinstance(event, dict):
            stats.parse_errors += 1
            continue

        action = event.get("action")
        if action == "start":
            _handle_start(event, stats, copy_source, transfer_parent, timings)
        elif action == "result" and event.get("type") == RES_PROGRESS:
            act_id = event.get("id")
            done = _progress_done(event.get("fields"))
            if done is None:
                stats.parse_errors += 1
            elif act_id in copy_source:
                copy_done[act_id] = done
            elif act_id in transfer_parent:
                transfer_done[act_id] = done

    for act_id, done in copy_done.items():
        stats.substituted_bytes[copy_source[act_id]] += done

    for act_id, done in transfer_done.items():
        source = copy_source.get(transfer_parent[act_id])
        # Transfers with no copyPath parent are narinfo lookups and flake input
        # fetches. Real traffic, but not attributable to substituting a path,
        # so they are deliberately not counted against any substituter.
        if source is not None:
            stats.downloaded_bytes[source] += done

    return stats


def _handle_start(event, stats, copy_source, transfer_parent, timings):
    """Record one activity-start event.

    Split out so the shape assertions for each activity type stay legible. A
    field list that does not match what logging.hh documents is the signal that
    the internal-json format moved, so it counts as a parse error rather than
    being quietly coerced.
    """
    act_type = event.get("type")
    fields = event.get("fields")

    if act_type == ACT_BUILD:
        # fields: [drvPath, machine, curRound, nrRounds]
        if not isinstance(fields, list) or len(fields) < 2:
            stats.parse_errors += 1
            return
        machine = fields[1]
        if not isinstance(machine, str):
            stats.parse_errors += 1
            return
        stats.paths_built[machine or LOCAL] += 1
        seconds = timings.get(str(event.get("id")))
        if seconds is not None:
            stats.build_seconds[machine or LOCAL] += seconds

    elif act_type == ACT_SUBSTITUTE:
        # fields: [storePath, substituterURI]
        if not isinstance(fields, list) or len(fields) < 2:
            stats.parse_errors += 1
            return
        substituter = fields[1]
        if not isinstance(substituter, str):
            stats.parse_errors += 1
            return
        stats.paths_substituted[substituter] += 1
        seconds = timings.get(str(event.get("id")))
        if seconds is not None:
            stats.substitute_seconds[substituter] += seconds

    elif act_type == ACT_COPY_PATH:
        # fields: [storePath, from, to]
        if not isinstance(fields, list) or len(fields) < 2:
            stats.parse_errors += 1
            return
        source = fields[1]
        if not isinstance(source, str):
            stats.parse_errors += 1
            return
        copy_source[event.get("id")] = source

    elif act_type == ACT_FILE_TRANSFER:
        # fields: [uri]. The parent is the copyPath this transfer feeds, when
        # there is one; a top-level transfer has parent 0.
        parent = event.get("parent")
        if parent:
            transfer_parent[event.get("id")] = parent


def _escape(value):
    """Escape a Prometheus label value (backslash, quote, newline)."""
    return str(value).replace("\\", "\\\\").replace('"', '\\"').replace("\n", "\\n")


def render(stats, duration_seconds, succeeded, now=None):
    """Render Stats as an OpenMetrics-style textfile exposition.

    Everything is a gauge describing the most recent run rather than a counter,
    because this is written once per self-update and node-exporter re-reads the
    same file until the next one. ``last_run_timestamp_seconds`` is what makes
    a stale file recognisable as stale.
    """
    if now is None:
        now = time.time()

    out = []

    def emit(name, help_text, samples):
        out.append(f"# HELP {name} {help_text}")
        out.append(f"# TYPE {name} gauge")
        for labels, value in samples:
            if labels:
                rendered = ",".join(
                    f'{k}="{_escape(v)}"' for k, v in sorted(labels.items())
                )
                out.append(f"{name}{{{rendered}}} {value}")
            else:
                out.append(f"{name} {value}")

    emit(
        "nixos_selfupdate_build_last_run_timestamp_seconds",
        "Unix time this host last finished a self-update build.",
        [(None, f"{now:.0f}")],
    )
    emit(
        "nixos_selfupdate_build_duration_seconds",
        "Wall-clock seconds the last nixos-rebuild took.",
        [(None, f"{duration_seconds:.0f}")],
    )
    emit(
        "nixos_selfupdate_build_success",
        "Whether the last self-update build succeeded.",
        [(None, 1 if succeeded else 0)],
    )
    emit(
        "nixos_selfupdate_build_parse_errors",
        "Events in the Nix JSON log this exporter could not understand. "
        "Non-zero means Nix's internal-json format moved and the other "
        "metrics in this file are understated.",
        [(None, stats.parse_errors)],
    )

    # machine="local" is emitted even at zero: a run that built nothing is a
    # meaningful, common answer, and a series that vanishes cannot say it.
    built = dict(stats.paths_built)
    built.setdefault(LOCAL, 0)
    emit(
        "nixos_selfupdate_build_paths_built",
        "Derivations realised during the last self-update, by builder. "
        'machine="local" is this host; anything else is a remote builder.',
        [({"machine": m}, n) for m, n in sorted(built.items())],
    )
    emit(
        "nixos_selfupdate_build_paths_substituted",
        "Store paths fetched from a binary cache during the last "
        "self-update, by substituter.",
        [({"substituter": s}, n) for s, n in sorted(stats.paths_substituted.items())],
    )
    emit(
        "nixos_selfupdate_build_substituted_bytes",
        "Bytes written into the local store by substitution during the "
        "last self-update, by substituter.",
        [({"substituter": s}, n) for s, n in sorted(stats.substituted_bytes.items())],
    )
    emit(
        "nixos_selfupdate_build_downloaded_bytes",
        "Bytes pulled over the network to substitute paths during the last "
        "self-update, by substituter. Lower than substituted_bytes because "
        "NARs are transferred compressed.",
        [({"substituter": s}, n) for s, n in sorted(stats.downloaded_bytes.items())],
    )

    # Nix's event stream has no timestamps, so these exist only when the
    # `timings` follower was running alongside the build. Say so explicitly
    # rather than reporting zero seconds, which would read as "instant".
    emit(
        "nixos_selfupdate_build_timings_available",
        "Whether arrival timings were captured for the last self-update. "
        "The _seconds metrics are absent when this is 0.",
        [(None, 1 if stats.have_timings else 0)],
    )
    if stats.have_timings:
        emit(
            "nixos_selfupdate_build_seconds",
            "Seconds spent realising derivations during the last self-update, "
            "by builder. Wall-clock and overlapping: Nix builds in parallel, "
            "so this can exceed the run's duration.",
            [
                ({"machine": m}, f"{v:.3f}")
                for m, v in sorted(stats.build_seconds.items())
            ],
        )
        emit(
            "nixos_selfupdate_build_substitute_seconds",
            "Seconds spent substituting paths during the last self-update, by "
            "substituter. Against substituted_bytes this gives the throughput "
            "a substituter actually delivered.",
            [
                ({"substituter": s}, f"{v:.3f}")
                for s, v in sorted(stats.substitute_seconds.items())
            ],
        )

    return "\n".join(out) + "\n"


def write_atomically(path, content):
    """Write content to path via a temp file in the same directory.

    node-exporter reads this directory on its own schedule and will happily
    read a half-written file, so the rename has to be the only thing it can
    observe. 0644 because node-exporter reads it as an unprivileged user in
    the DaemonSet's mount namespace.
    """
    tmp = f"{path}.tmp"
    with open(tmp, "w") as handle:
        handle.write(content)
    os.chmod(tmp, 0o644)
    os.rename(tmp, path)


def follow_timings(json_log, sink, stop, poll_seconds=0.1):
    """Stamp activity start/stop events with the time they arrived.

    Runs alongside the build. ``stop`` is a zero-argument predicate that goes
    true when the caller wants us to finish; the remaining bytes are drained
    first, because the build is already over by then and the tail of the log
    holds exactly the stop events whose durations we came for.

    Records are flushed as they are written, so a follower that is killed
    outright still leaves usable timings for everything that had finished.

    Partial lines are held back and completed on a later pass. Nix flushes
    whole lines, so this is rare, but a half-read line would otherwise be
    charged to the wrong activity or dropped.
    """
    pending = ""
    draining = False

    while True:
        chunk = json_log.readline()
        if not chunk:
            if draining:
                break
            if stop():
                # One more pass to pick up whatever landed between the last
                # read and the stop request.
                draining = True
            else:
                time.sleep(poll_seconds)
            continue

        pending += chunk
        if not pending.endswith("\n"):
            continue
        line, pending = pending, ""

        arrived = time.time()

        # 98% of the stream is `result` progress events this pass has no use
        # for -- 162,446 of 165,979 lines in a measured ffmpeg substitution --
        # and json.loads costs 10x what looking for the two action names as
        # substrings does (256ms vs 25ms over that log). Deliberately loose:
        # a line that merely mentions "start" falls through to the parse below
        # and is discarded there, so the only way this drops a real event is
        # if Nix renames the actions themselves -- which would break the
        # report pass too.
        if '"start"' not in line and '"stop"' not in line:
            continue

        try:
            event = json.loads(line)
        except json.JSONDecodeError:
            continue
        action = event.get("action")
        if action in ("start", "stop"):
            sink.write(f"{arrived:.6f} {action} {event.get('id')}\n")
            sink.flush()


def cmd_timings(args):
    """Follow a Nix JSON log and write a timings sidecar until told to stop."""
    finished = []
    for sig in (signal.SIGTERM, signal.SIGINT):
        signal.signal(sig, lambda *_: finished.append(True))

    # The caller truncates and creates the log before starting us, so this
    # open is not a race. Line-buffered sink so a kill -9 still leaves data.
    with open(args.json_log) as json_log, open(args.output, "w", buffering=1) as sink:
        follow_timings(json_log, sink, lambda: bool(finished))
    return 0


def cmd_report(args):
    """Parse a finished Nix JSON log into textfile-collector metrics."""
    timings = {}
    if args.timings:
        try:
            with open(args.timings) as handle:
                timings = read_timings(handle)
        except OSError as err:
            # No sidecar means no durations, not a failed run.
            print(f"warning: cannot read {args.timings}: {err}", file=sys.stderr)

    try:
        with open(args.json_log) as handle:
            stats = parse_events(handle, timings)
    except OSError as err:
        # A missing log is not a failed deploy: the build may have died before
        # Nix opened it. Report an empty run rather than exiting non-zero.
        print(f"warning: cannot read {args.json_log}: {err}", file=sys.stderr)
        stats = Stats()

    content = render(stats, args.duration_seconds, args.exit_status == 0)
    write_atomically(args.output, content)
    if args.publish:
        # Written separately rather than copied, so both destinations get the
        # same atomic rename. --output lives somewhere persistent; --publish is
        # the textfile collector directory, which does not survive a reboot.
        write_atomically(args.publish, content)
    return 0


def main(argv=None):
    parser = argparse.ArgumentParser(
        description="Summarise a Nix json-log-path stream as Prometheus "
        "textfile metrics."
    )
    sub = parser.add_subparsers(dest="command", required=True)

    timings = sub.add_parser(
        "timings",
        help="follow a Nix JSON log while a build runs and record when each "
        "activity started and stopped; stop on SIGTERM",
    )
    timings.add_argument("--json-log", required=True)
    timings.add_argument("--output", required=True, help="sidecar file to write")
    timings.set_defaults(func=cmd_timings)

    report = sub.add_parser(
        "report", help="turn a finished Nix JSON log into a .prom file"
    )
    report.add_argument(
        "--json-log",
        required=True,
        help="Path to the file Nix wrote via the json-log-path setting.",
    )
    report.add_argument(
        "--output", required=True, help="Textfile collector .prom file to write."
    )
    report.add_argument(
        "--publish",
        help="Second path to write the same metrics to, atomically. Used to "
        "put a copy in the textfile collector directory while --output keeps "
        "one somewhere that survives a reboot.",
    )
    report.add_argument(
        "--timings",
        help="Sidecar written by the timings subcommand. Without it the "
        "duration metrics are omitted.",
    )
    report.add_argument(
        "--duration-seconds",
        type=float,
        default=0.0,
        help="Wall-clock duration of the nixos-rebuild that produced the log.",
    )
    report.add_argument(
        "--exit-status",
        type=int,
        default=0,
        help="Exit status of that nixos-rebuild.",
    )
    report.set_defaults(func=cmd_report)

    args = parser.parse_args(argv)
    return args.func(args)


if __name__ == "__main__":
    sys.exit(main())
