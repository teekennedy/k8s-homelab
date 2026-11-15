"""16 kHz, 16-bit mono WAV handling with the standard library only.

The collector never resamples or normalises: device clips come from the wake
word model's own microphone tap and gain, and that level is part of what the
model must learn.
"""

import io
import math
import wave
from array import array
from pathlib import Path

RATE = 16000


class AudioError(ValueError):
    pass


def parse_wav(body: bytes, max_seconds: float) -> array:
    """The samples of a 16 kHz 16-bit mono WAV, or AudioError."""
    if body[:4] != b"RIFF" or body[8:12] != b"WAVE":
        raise AudioError("not a WAV file")
    try:
        with wave.open(io.BytesIO(body)) as w:
            if (w.getframerate(), w.getsampwidth(), w.getnchannels()) != (RATE, 2, 1):
                raise AudioError(
                    f"expected 16 kHz 16-bit mono, got {w.getframerate()} Hz "
                    f"{8 * w.getsampwidth()}-bit x{w.getnchannels()}"
                )
            n = w.getnframes()
            if n == 0 or n > max_seconds * RATE:
                raise AudioError(f"{n / RATE:.1f} s is outside 0-{max_seconds:g} s")
            samples = array("h")
            samples.frombytes(w.readframes(n))
    except (wave.Error, EOFError) as exc:
        raise AudioError(str(exc)) from exc
    return samples


def read(path: Path) -> array:
    return parse_wav(path.read_bytes(), max_seconds=3600)


def write(path: Path, samples: array) -> None:
    """Write atomically, so a reader never sees half a clip."""
    path.parent.mkdir(parents=True, exist_ok=True)
    tmp = path.with_name(f".{path.name}.tmp")
    with wave.open(str(tmp), "wb") as w:
        w.setnchannels(1)
        w.setsampwidth(2)
        w.setframerate(RATE)
        w.writeframes(samples.tobytes())
    tmp.replace(path)


def duration(samples: array) -> float:
    return len(samples) / RATE


def cut(samples: array, t0: float, t1: float) -> array:
    a = max(0, int(round(t0 * RATE)))
    b = min(len(samples), int(round(t1 * RATE)))
    return samples[a:b]


def chunks(
    samples: array, seconds: float, start: float = 0.0, end: float | None = None
):
    """Consecutive whole chunks of `seconds` between start and end."""
    end = duration(samples) if end is None else end
    t = start
    while t + seconds <= end + 1e-9:
        yield t, cut(samples, t, t + seconds)
        t += seconds


def peak_frame_dbfs(samples: array, frame_s: float = 0.1) -> float:
    """The loudest 100 ms frame's RMS level in dBFS: how loud the clip ever gets."""
    n = max(1, int(frame_s * RATE))
    best = 0.0
    for i in range(0, max(1, len(samples) - n + 1), n):
        frame = samples[i : i + n]
        if not frame:
            continue
        ms = sum(x * x for x in frame) / len(frame)
        best = max(best, ms)
    if best == 0:
        return -120.0
    return 10 * math.log10(best / (32768.0**2))
