"""Word-timed transcripts from whisper.cpp (pywhispercpp), on CPU.

The same model family as micro-wake-word's tools/segment_recordings.py, so the
matching rules learned there carry over.
"""

import logging
import threading
from array import array
from pathlib import Path

from .matching import Word

log = logging.getLogger(__name__)

PAD_S = 0.4


class WhisperTranscriber:
    def __init__(self, model_path: Path, threads: int = 4):
        self.model_path, self.threads = model_path, threads
        self._model = None
        self._lock = threading.Lock()

    def _load(self):
        from pywhispercpp.model import Model

        log.info("loading %s", self.model_path)
        return Model(
            str(self.model_path),
            n_threads=self.threads,
            language="en",
            print_progress=False,
            print_realtime=False,
            no_context=True,
            token_timestamps=True,
            max_len=1,
            split_on_word=True,
        )

    def __call__(self, samples: array) -> list[Word]:
        import numpy as np

        with self._lock:
            if self._model is None:
                self._model = self._load()
            x = (
                np.frombuffer(samples.tobytes(), dtype=np.int16).astype(np.float32)
                / 32768.0
            )
            pad = np.zeros(int(PAD_S * 16000), dtype=np.float32)
            segments = self._model.transcribe(np.concatenate([pad, x, pad]))
        words = []
        for s in segments:
            text = s.text.strip()
            if text:
                # whisper.cpp times are in centiseconds; undo the padding.
                t0 = max(0.0, s.t0 / 100 - PAD_S)
                t1 = max(t0, s.t1 / 100 - PAD_S)
                words.append(Word(text, t0, t1))
        return words
