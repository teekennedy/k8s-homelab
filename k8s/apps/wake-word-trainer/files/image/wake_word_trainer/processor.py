"""Transcribe each new clip, cut it into training material, and flag doubtful ones.

| label          | expected                    | becomes                                   |
|----------------|-----------------------------|-------------------------------------------|
| detected       | wake word at the very end   | lead-in (<= 1.5 s) + word                 |
| false_reject   | wake word somewhere         | lead-in + word, located by word timings   |
| false_accept   | no wake word                | last 3.2 s + 3 s chunks of the rest       |
| true_reject    | no wake word                | 3 s chunks                                |

Negatives in the validation and test splits are kept whole instead, as room
audio streamed for false accepts per hour. Positives keep up to 1.5 s of the
real audio before the word: a tightly cut clip teaches the model "noise-floor
onset, then speech" and it then fires on abrupt starts.

A flagged clip is still cut (so confirming it needs no reprocessing) but is not
used until someone confirms it.
"""

import logging
import threading
import time
import traceback
from pathlib import Path

from . import audio
from .db import Store
from .matching import Word, suspicious, wake_candidates

log = logging.getLogger(__name__)

LEAD_IN_S = 1.5
TAIL_S = 0.15
DETECTED_WINDOW_S = 2.5
HARD_NEGATIVE_S = 3.2
CHUNK_S = 3.0
SILENT_DBFS = -55.0


def plan(label: str, split: str, samples, words: list[Word]):
    """What to cut from a clip: ([(kind, t0, t1)], [flag reasons]).

    kind is positive / negative / ambient; ambient means the whole clip.
    """
    dur = audio.duration(samples)
    cands = wake_candidates(words)
    flags: list[str] = []
    cuts: list[tuple[str, float, float]] = []

    def positive(c):
        return ("positive", max(0.0, c.t0 - LEAD_IN_S), min(dur, c.t1 + TAIL_S))

    if label == "detected":
        at_end = [c for c in cands if c.t1 >= dur - DETECTED_WINDOW_S]
        if at_end:
            cuts.append(positive(at_end[-1]))
        else:
            flags.append(
                f"no 'okay + GLaDOS-like' word in the last {DETECTED_WINDOW_S:g} s"
            )
            cuts.append(
                ("positive", max(0.0, dur - DETECTED_WINDOW_S - LEAD_IN_S), dur)
            )
    elif label == "false_reject":
        if len(cands) != 1:
            flags.append(
                "no wake word found"
                if not cands
                else f"{len(cands)} wake word candidates"
            )
        cuts.extend(positive(c) for c in cands)
    elif label in ("false_accept", "true_reject"):
        hits = suspicious(words)
        if hits:
            flags.append("wake-word-like phrase: " + ", ".join(hits))
        if label == "true_reject" and audio.peak_frame_dbfs(samples) < SILENT_DBFS:
            flags.append(
                f"near-silent (loudest 100 ms frame below {SILENT_DBFS:g} dBFS)"
            )
        if split != "training":
            cuts.append(("ambient", 0.0, dur))
        elif label == "false_accept":
            # The audio right before the device fired is the hard negative.
            hard = max(0.0, dur - HARD_NEGATIVE_S)
            cuts.append(("negative", hard, dur))
            cuts.extend(
                ("negative", t, t + CHUNK_S)
                for t, _ in audio.chunks(samples, CHUNK_S, end=hard)
            )
        else:
            cuts.extend(
                ("negative", t, t + CHUNK_S) for t, _ in audio.chunks(samples, CHUNK_S)
            )
    else:
        flags.append(f"unknown label {label!r}")
    return cuts, flags


def process_clip(store: Store, clips_dir: Path, transcriber, clip: dict) -> None:
    src = clips_dir / clip["path"]
    samples = audio.read(src)
    words = transcriber(samples)
    transcript = " ".join(w.text for w in words).strip()
    cuts, flags = plan(clip["label"], clip["split"], samples, words)

    out_dir = clips_dir / "derived" / clip["id"]
    for old in out_dir.glob("*.wav") if out_dir.exists() else ():
        old.unlink()
    derived = []
    for n, (kind, t0, t1) in enumerate(cuts):
        if kind == "ambient":
            derived.append({"path": clip["path"], "kind": kind, "t0": t0, "t1": t1})
            continue
        rel = f"derived/{clip['id']}/{n:02d}-{kind}.wav"
        audio.write(clips_dir / rel, audio.cut(samples, t0, t1))
        derived.append(
            {"path": rel, "kind": kind, "t0": round(t0, 3), "t1": round(t1, 3)}
        )
    stored = store.save_processing(
        clip["id"],
        clip["label"],
        transcript,
        [{"w": w.text, "t0": w.t0, "t1": w.t1} for w in words],
        flags,
        derived,
    )
    if stored:
        log.info(
            "processed %s (%s): %r %s",
            clip["id"],
            clip["label"],
            transcript,
            flags or "",
        )


class Processor(threading.Thread):
    """The work queue: pending rows in the clips table, one at a time."""

    def __init__(self, store: Store, clips_dir: Path, transcriber, idle_s: float = 2.0):
        super().__init__(name="processor", daemon=True)
        self.store, self.clips_dir, self.transcriber = store, clips_dir, transcriber
        self.idle_s = idle_s
        self.stop = threading.Event()
        self.wake = threading.Event()
        self.last_error: str | None = None

    def run_once(self) -> bool:
        clip = self.store.next_pending()
        if clip is None:
            return False
        try:
            process_clip(self.store, self.clips_dir, self.transcriber, clip)
        except Exception as exc:  # one bad clip must not stop the queue
            log.exception("processing %s failed", clip["id"])
            self.last_error = f"{type(exc).__name__}: {exc}"
            self.store.save_error(clip["id"], traceback.format_exc())
        return True

    def run(self):
        while not self.stop.is_set():
            if not self.run_once():
                self.wake.wait(self.idle_s)
                self.wake.clear()
            else:
                time.sleep(0)
