"""Runtime settings, all from the environment (set by the Helm chart)."""

import os
from dataclasses import dataclass, field
from pathlib import Path

LABELS = ("detected", "false_accept", "false_reject", "true_reject")
POSITIVE_LABELS = ("detected", "false_reject")


def _env(name: str, default: str) -> str:
    return os.environ.get(name) or default


def _secret(name: str) -> str:
    """A value given directly, or read from the file named by <name>_FILE."""
    if value := os.environ.get(name):
        return value
    if path := os.environ.get(f"{name}_FILE"):
        return Path(path).read_text().strip()
    return ""


@dataclass
class Settings:
    state_dir: Path = field(
        default_factory=lambda: Path(_env("WWT_STATE_DIR", "/data/state"))
    )
    clips_dir: Path = field(
        default_factory=lambda: Path(_env("WWT_CLIPS_DIR", "/data/clips"))
    )
    runs_dir: Path = field(
        default_factory=lambda: Path(_env("WWT_RUNS_DIR", "/data/runs"))
    )
    capture_token: str = field(default_factory=lambda: _secret("WWT_CAPTURE_TOKEN"))
    internal_token: str = field(default_factory=lambda: _secret("WWT_INTERNAL_TOKEN"))
    whisper_model: Path = field(
        default_factory=lambda: Path(
            _env("WWT_WHISPER_MODEL", "/data/state/models/ggml-large-v3-turbo.bin")
        )
    )
    whisper_threads: int = field(
        default_factory=lambda: int(_env("WWT_WHISPER_THREADS", "4"))
    )
    max_clip_seconds: float = 60.0
    # X-Relabel-Last only moves a detection this recent.
    relabel_window_s: float = 600.0
    # A true_reject sample this close before any other upload probably holds
    # the wake word.
    ambient_discard_s: float = 30.0
    # Gate: what counts as enough new data for a nightly run.
    gate_min_positives: int = field(
        default_factory=lambda: int(_env("WWT_GATE_MIN_POSITIVES", "20"))
    )
    gate_min_false_accepts: int = field(
        default_factory=lambda: int(_env("WWT_GATE_MIN_FALSE_ACCEPTS", "10"))
    )
    gate_min_hours_between_runs: float = 20.0
    # A run still "running" after this long is recorded as abandoned.
    run_timeout_hours: float = field(
        default_factory=lambda: float(_env("WWT_RUN_TIMEOUT_HOURS", "22"))
    )
    # How often running runs are checked against their Jobs.
    reconcile_interval_s: float = 60.0
    # Train-now creates a Job from this CronJob.
    namespace: str = field(default_factory=lambda: _env("WWT_NAMESPACE", ""))
    cronjob: str = field(default_factory=lambda: _env("WWT_CRONJOB", ""))
    # oauth2-proxy (reverse-proxy mode) passes the signed-in user here.
    user_header: str = "X-Forwarded-Preferred-Username"

    @property
    def db_path(self) -> Path:
        return self.state_dir / "wake-word-trainer.sqlite3"
