"""Find "Okay GLaDOS" in a Whisper transcript.

Whisper cannot spell the name: "Gladys", "Blados", "Claros", "glad" and
"blotters" after "okay" have all been the wake word. So a match is "okay" plus
one GLaDOS-like word, never the literal string. The rules follow
okay_glados/tools/segment_recordings.py in the micro-wake-word repo, with the
"bl" spellings added.
"""

import re
from dataclasses import dataclass

OKAY = ("ok", "okay", "okey", "okie")
# The word after "okay" that makes it the wake word.
GLADOS_AFTER_OKAY = re.compile(r"^(gl|cl|kl|bl|gr|lados)")
# A word that is suspicious on its own, without a preceding "okay".
GLADOS_ALONE = re.compile(r"^(glad|glados|gladys|gladis|claros|lados|blados)")


@dataclass(frozen=True)
class Word:
    text: str
    t0: float
    t1: float


@dataclass(frozen=True)
class Candidate:
    """One "okay + GLaDOS-like word" occurrence, in seconds from the clip start."""

    t0: float
    t1: float
    text: str


def tokens(text: str) -> list[str]:
    return re.sub(r"[^a-z ]", " ", text.lower()).split()


def _flatten(words: list[Word]) -> list[Word]:
    """One entry per token: Whisper sometimes returns "Okay," or "okay glados" as one word."""
    out = []
    for w in words:
        toks = tokens(w.text)
        if not toks:
            continue
        step = (w.t1 - w.t0) / len(toks)
        for i, t in enumerate(toks):
            out.append(Word(t, w.t0 + i * step, w.t0 + (i + 1) * step))
    return out


def wake_candidates(words: list[Word]) -> list[Candidate]:
    """Every "okay" immediately followed by a GLaDOS-like word."""
    flat = _flatten(words)
    found = []
    for a, b in zip(flat, flat[1:]):
        if a.text in OKAY and GLADOS_AFTER_OKAY.match(b.text):
            found.append(Candidate(a.t0, b.t1, f"{a.text} {b.text}"))
    return found


def suspicious(words: list[Word]) -> list[str]:
    """Anything that might be the wake word, for flagging clips labelled negative."""
    flat = _flatten(words)
    hits = [f"{c.text} @ {c.t0:.1f}s" for c in wake_candidates(words)]
    for w in flat:
        if GLADOS_ALONE.match(w.text) and not any(
            w.t0 >= c.t0 - 1e-6 and w.t1 <= c.t1 + 1e-6 for c in wake_candidates(words)
        ):
            hits.append(f"{w.text} @ {w.t0:.1f}s")
    return hits
