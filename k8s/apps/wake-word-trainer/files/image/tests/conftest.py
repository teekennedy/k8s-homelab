import io
import math
import wave
from array import array

import pytest
from fastapi.testclient import TestClient

from wake_word_trainer.app import create_app
from wake_word_trainer.matching import Word
from wake_word_trainer.settings import Settings

RATE = 16000


def tone(seconds: float, amplitude: int = 3000) -> array:
    return array(
        "h", (int(amplitude * math.sin(i / 8)) for i in range(int(seconds * RATE)))
    )


def wav_bytes(samples: array, rate: int = RATE) -> bytes:
    buf = io.BytesIO()
    with wave.open(buf, "wb") as w:
        w.setnchannels(1)
        w.setsampwidth(2)
        w.setframerate(rate)
        w.writeframes(samples.tobytes())
    return buf.getvalue()


def words(*spec) -> list[Word]:
    """words("okay", 9.9, 10.2, "Gladys.", 10.2, 10.8)"""
    return [Word(spec[i], spec[i + 1], spec[i + 2]) for i in range(0, len(spec), 3)]


class FakeTranscriber:
    """Returns queued word lists in order, then nothing."""

    def __init__(self):
        self.queue: list[list[Word]] = []

    def __call__(self, samples):
        return self.queue.pop(0) if self.queue else []


@pytest.fixture
def settings(tmp_path):
    return Settings(
        state_dir=tmp_path / "state",
        clips_dir=tmp_path / "clips",
        runs_dir=tmp_path / "runs",
        capture_token="capture-secret",
        internal_token="internal-secret",
        whisper_model=tmp_path / "model.bin",
        namespace="wake-word-trainer",
        cronjob="wake-word-trainer-nightly",
    )


@pytest.fixture
def transcriber():
    return FakeTranscriber()


@pytest.fixture
def app(settings, transcriber):
    return create_app(settings, transcriber=transcriber, start_processor=False)


@pytest.fixture
def client(app):
    with TestClient(app) as c:
        yield c


def upload(
    client, label, seconds=12.0, device="voice-pe", token="capture-secret", samples=None
):
    return client.post(
        "/capture",
        content=wav_bytes(samples if samples is not None else tone(seconds)),
        headers={"X-Label": label, "X-Device": device, "X-Capture-Token": token},
    )
