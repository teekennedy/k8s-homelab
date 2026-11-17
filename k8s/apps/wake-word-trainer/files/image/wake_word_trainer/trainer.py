"""The batch side: the nightly training run, the publisher and the one-time seed.

All three drive the okay_glados pipeline (micro-wake-word) as subprocesses,
pointing it at the volumes with its OKAY_GLADOS_* environment variables, and
talk to the collector over its internal API: the database stays with the
collector.
"""

import json
import logging
import os
import shlex
import shutil
import subprocess
import sys
import time
import traceback
import urllib.request
from dataclasses import dataclass, field
from pathlib import Path

from .settings import _env, _secret

log = logging.getLogger(__name__)

# v2g's recipe: the best model so far (docs/wake-word-trainer-plan.md).
DEFAULT_TRAIN_ARGS = (
    "--steps 18000 12000 --learning-rates 0.001 0.0003 --negative-class-weight 20 20"
    " --batch-size 256 --eval-step-interval 500 --spec-augment-stages 1 --recorded-weight 0.4"
)
MODEL_FILES = ("okay_glados.tflite", "okay_glados.json")


@dataclass
class TrainerConfig:
    collector_url: str = field(
        default_factory=lambda: _env("WWT_COLLECTOR_URL", "http://localhost:8080")
    )
    internal_token: str = field(default_factory=lambda: _secret("WWT_INTERNAL_TOKEN"))
    pipeline_dir: Path = field(
        default_factory=lambda: Path(_env("WWT_PIPELINE_DIR", "/opt/micro-wake-word"))
    )
    python: str = field(
        default_factory=lambda: _env("OKAY_GLADOS_TRAIN_PYTHON", sys.executable)
    )
    work_dir: Path = field(
        default_factory=lambda: Path(_env("OKAY_GLADOS_WORK_DIR", "/data/work"))
    )
    clips_dir: Path = field(
        default_factory=lambda: Path(_env("WWT_CLIPS_DIR", "/data/clips"))
    )
    runs_dir: Path = field(
        default_factory=lambda: Path(_env("WWT_RUNS_DIR", "/data/runs"))
    )
    published_dir: Path = field(
        default_factory=lambda: Path(_env("WWT_PUBLISHED_DIR", "/publish"))
    )
    # Only for a pod that publishes in a second container after training.
    handoff: Path | None = field(
        default_factory=lambda: Path(h) if (h := _env("WWT_HANDOFF", "")) else None
    )
    forced: bool = field(default_factory=lambda: _env("WWT_FORCE", "") in ("1", "true"))
    repeats: int = field(default_factory=lambda: int(_env("WWT_REPEATS", "3")))
    train_args: list[str] = field(
        default_factory=lambda: shlex.split(_env("WWT_TRAIN_ARGS", DEFAULT_TRAIN_ARGS))
    )
    workers: str = field(default_factory=lambda: _env("WWT_WORKERS", "4"))
    target_faph: float = field(
        default_factory=lambda: float(_env("WWT_TARGET_FAPH", "0.1"))
    )


# ------------------------------------------------------------------ helpers


def api(cfg: TrainerConfig, method: str, path: str, body=None):
    req = urllib.request.Request(
        cfg.collector_url.rstrip("/") + path,
        method=method,
        data=None if body is None else json.dumps(body).encode(),
        headers={
            "X-Internal-Token": cfg.internal_token,
            "Content-Type": "application/json",
        },
    )
    for attempt in range(5):
        try:
            with urllib.request.urlopen(req, timeout=60) as resp:
                return json.loads(resp.read() or b"null")
        except OSError:
            if attempt == 4:
                raise
            time.sleep(5 * (attempt + 1))


def run_step(
    cfg: TrainerConfig, script: str, args: list[str], env: dict, log_path: Path
):
    """One pipeline script, its output teed to log_path."""
    cmd = [cfg.python, "-u", str(cfg.pipeline_dir / "okay_glados" / script), *args]
    log_path.parent.mkdir(parents=True, exist_ok=True)
    print(f"+ {shlex.join(cmd)}", flush=True)
    with open(log_path, "ab") as fh:
        fh.write(f"+ {shlex.join(cmd)}\n".encode())
        proc = subprocess.Popen(
            cmd,
            stdout=subprocess.PIPE,
            stderr=subprocess.STDOUT,
            env={**os.environ, **env},
            cwd=cfg.pipeline_dir,
        )
        for line in proc.stdout:
            fh.write(line)
            sys.stdout.buffer.write(line)
        fh.flush()
        sys.stdout.flush()
        if proc.wait() != 0:
            raise RuntimeError(
                f"{script} {shlex.join(args)} exited with {proc.returncode}"
            )


def _arg_values(args: list[str], flag: str) -> list[str]:
    if flag not in args:
        return []
    out = []
    for a in args[args.index(flag) + 1 :]:
        if a.startswith("--"):
            break
        out.append(a)
    return out


def stage_boundaries(train_args: list[str]) -> list[str]:
    steps = [int(s) for s in _arg_values(train_args, "--steps")]
    return [str(sum(steps[: i + 1])) for i in range(len(steps) - 1)]


def decide(
    candidate: dict, baseline: dict | None, target_faph: float
) -> tuple[bool, str]:
    """Promote only a model that holds the false-accept target and misses fewer real clips.

    Both numbers come from the same test data in the same run, so the
    published model is judged on exactly what the candidate is.
    """
    op = candidate.get("operating")
    faph = candidate.get("faph")
    frr = candidate.get("recorded_frr")
    if op is None or faph is None:
        return False, "no cutoff reaches the false-accept target"
    if faph > target_faph:
        return False, f"{faph:.3f} false accepts/h is above {target_faph:g}/h"
    if frr is None:
        return False, "there are no held-out real clips to compare on"
    if baseline is None:
        return True, "nothing is published yet"
    base = baseline.get("recorded_frr")
    if base is None:
        return True, "the published model could not be measured on real clips"
    if frr < base:
        return True, f"misses {100 * frr:.1f}% of real clips against {100 * base:.1f}%"
    return (
        False,
        f"misses {100 * frr:.1f}% of real clips, not fewer than the published {100 * base:.1f}%",
    )


def write_handoff(cfg: TrainerConfig, data: dict):
    if cfg.handoff is None:
        return
    cfg.handoff.parent.mkdir(parents=True, exist_ok=True)
    cfg.handoff.write_text(json.dumps(data))


# --------------------------------------------------------------------- train


def train(cfg: TrainerConfig) -> int:
    if not (cfg.work_dir / ".seeded").exists():
        # The seed Job is still building the datasets; the gate waits too, so
        # its "has not run" alert fires if seeding never finishes.
        print(f"{cfg.work_dir} is not seeded yet: no run", flush=True)
        write_handoff(cfg, {"run_id": None, "promote": False})
        return 0
    started = api(cfg, "POST", "/internal/runs", {"forced": cfg.forced})
    gate, run = started["gate"], started["run"]
    print(f"gate: {gate['reason']}", flush=True)
    if run is None:
        write_handoff(cfg, {"run_id": None, "promote": False})
        return 0
    run_id = run["id"]
    try:
        result, clip_ids = train_run(cfg, run_id)
    except Exception:
        err = traceback.format_exc()
        print(err, file=sys.stderr, flush=True)
        api(
            cfg,
            "POST",
            f"/internal/runs/{run_id}/result",
            {"status": "failed", "error": err[-4000:]},
        )
        write_handoff(cfg, {"run_id": run_id, "promote": False})
        return 1
    finally:
        for d in (cfg.work_dir / "trained_models").glob(f"okay_glados_{run_id}-r*"):
            shutil.rmtree(d, ignore_errors=True)
    api(
        cfg,
        "POST",
        f"/internal/runs/{run_id}/result",
        {"status": "succeeded", "result": result, "clip_ids": clip_ids},
    )
    write_handoff(
        cfg,
        {
            "run_id": run_id,
            "promote": result["promote"],
            "model_dir": str(cfg.runs_dir / run_id / "report"),
        },
    )
    print(
        f"run {run_id}: {'promote' if result['promote'] else 'keep the published model'}"
        f" ({result['decision']})",
        flush=True,
    )
    return 0


def export_dataset(
    cfg: TrainerConfig, run_id: str, run_dir: Path
) -> tuple[Path, list[str]]:
    rows = api(cfg, "GET", f"/internal/runs/{run_id}/dataset")["clips"]
    manifest = cfg.work_dir / "device_clips.tsv"
    manifest.parent.mkdir(parents=True, exist_ok=True)
    manifest.write_text(
        "path\tkind\tsplit\n"
        + "".join(
            f"{cfg.clips_dir / r['path']}\t{r['kind']}\t{r['split']}\n" for r in rows
        )
    )
    # The record of exactly what this run saw, by clip id and split.
    (run_dir / "dataset.tsv").write_text(
        "clip_id\tlabel\tkind\tsplit\tpath\n"
        + "".join(
            f"{r['clip_id']}\t{r['label']}\t{r['kind']}\t{r['split']}\t{r['path']}\n"
            for r in rows
        )
    )
    return manifest, sorted({r["clip_id"] for r in rows})


def train_run(cfg: TrainerConfig, run_id: str) -> tuple[dict, list[str]]:
    run_dir = cfg.runs_dir / run_id
    logs = run_dir / "logs"
    run_dir.mkdir(parents=True, exist_ok=True)
    manifest, clip_ids = export_dataset(cfg, run_id, run_dir)
    base_env = {
        "OKAY_GLADOS_WORK_DIR": str(cfg.work_dir),
        "OKAY_GLADOS_LOG_DIR": str(logs),
        "OKAY_GLADOS_DEVICE_CLIPS": str(manifest),
    }
    workers = ["--workers", cfg.workers]

    # Only the recorded sets change between runs; the synthetic ones were built
    # once by the seed Job.
    run_step(
        cfg,
        "step3_build_features.py",
        ["--only", "recorded"],
        {**base_env, "OKAY_GLADOS_REPORT_DIR": str(run_dir / "report")},
        logs / "step3.log",
    )

    # Run-to-run variance is large: train several times, keep the best by
    # validation score (which ranked earlier runs in the same order as the test set).
    repeats = []
    for k in range(1, cfg.repeats + 1):
        name = f"{run_id}-r{k}"
        env = {
            **base_env,
            "OKAY_GLADOS_RUN": name,
            "OKAY_GLADOS_REPORT_DIR": str(run_dir / "repeats" / f"r{k}"),
        }
        entry = {"name": name}
        try:
            run_step(
                cfg,
                "step4_train.py",
                ["--fresh", *cfg.train_args],
                env,
                logs / f"r{k}-train.log",
            )
            run_step(
                cfg,
                "tools/select_checkpoint.py",
                [
                    "--select",
                    "all",
                    "--apply",
                    "--log",
                    str(logs / f"step4_train_{name}.log"),
                    *workers,
                ],
                env,
                logs / f"r{k}-select.log",
            )
            selection = json.loads(
                (
                    run_dir / "repeats" / f"r{k}" / "checkpoint_selection.json"
                ).read_text()
            )
            best = min(selection, key=lambda e: e["score"])
            entry.update(score=best["score"], step=best["step"])
            # Convert the chosen weights to the streaming quantized TFLite model.
            run_step(
                cfg,
                "step4_train.py",
                ["--train", "0", *cfg.train_args],
                env,
                logs / f"r{k}-convert.log",
            )
        except Exception as exc:
            entry["error"] = str(exc)
        repeats.append(entry)
    scored = [r for r in repeats if "score" in r and "error" not in r]
    if not scored:
        raise RuntimeError(
            "every repeat failed: " + "; ".join(r.get("error", "") for r in repeats)
        )
    best = min(scored, key=lambda r: r["score"])
    k = best["name"].rsplit("-r", 1)[1]

    report = run_dir / "report"
    report.mkdir(parents=True, exist_ok=True)
    for f in (run_dir / "repeats" / f"r{k}").glob("checkpoint_selection.*"):
        shutil.copy(f, report / f.name)
    run_step(
        cfg,
        "step5_evaluate.py",
        [
            "--log",
            str(logs / f"step4_train_{best['name']}.log"),
            "--stages",
            *stage_boundaries(cfg.train_args),
            *workers,
        ],
        {
            **base_env,
            "OKAY_GLADOS_RUN": best["name"],
            "OKAY_GLADOS_REPORT_DIR": str(report),
        },
        logs / "step5.log",
    )
    candidate = json.loads((report / "metrics.json").read_text())

    baseline = None
    published = cfg.published_dir / "okay_glados.tflite"
    if published.exists():
        base_dir = run_dir / "baseline"
        base_dir.mkdir(parents=True, exist_ok=True)
        for name in MODEL_FILES:
            if (cfg.published_dir / name).exists():
                shutil.copy(cfg.published_dir / name, base_dir / name)
        cutoff = json.loads((base_dir / "okay_glados.json").read_text())["micro"][
            "probability_cutoff"
        ]
        run_step(
            cfg,
            "step5_evaluate.py",
            [
                "--model",
                str(base_dir / "okay_glados.tflite"),
                "--cutoff",
                str(cutoff),
                *workers,
            ],
            {**base_env, "OKAY_GLADOS_REPORT_DIR": str(base_dir)},
            logs / "baseline.log",
        )
        baseline = json.loads((base_dir / "metrics.json").read_text())

    promote, why = decide(candidate, baseline, cfg.target_faph)
    result = {
        "promote": promote,
        "decision": why,
        "candidate": candidate,
        "baseline": baseline,
        "seeds": repeats,
        "best": best["name"],
        "train_args": cfg.train_args,
        "clips": len(clip_ids),
    }
    (run_dir / "decision.json").write_text(json.dumps(result, indent=2) + "\n")
    return result, clip_ids


# ------------------------------------------------------------------- publish


def _atomic_copy(src: Path, dest: Path):
    tmp = dest.with_name(f".{dest.name}.tmp")
    shutil.copyfile(src, tmp)
    os.replace(tmp, dest)


def publish_files(model_dir: Path, target: Path, run_id: str) -> list[str]:
    """Archive the outgoing pair under previous/<run_id>/, then replace it, manifest last.

    The device only reads okay_glados.json (which names the .tflite), so the
    model must already be in place when the manifest that points at it lands.
    """
    for name in MODEL_FILES:
        if not (model_dir / name).exists():
            raise FileNotFoundError(model_dir / name)
    archived = []
    prev = target / "previous" / run_id
    for name in MODEL_FILES:
        if (target / name).exists():
            prev.mkdir(parents=True, exist_ok=True)
            shutil.copyfile(target / name, prev / name)
            archived.append(str(prev / name))
    _atomic_copy(model_dir / "okay_glados.tflite", target / "okay_glados.tflite")
    _atomic_copy(model_dir / "okay_glados.json", target / "okay_glados.json")
    return archived


def publish_run(cfg: TrainerConfig, run_id: str, dry_run: bool, live_dir: Path | None):
    """Publish one promoted run's model and report the outcome to the collector."""
    target = cfg.published_dir
    if dry_run and live_dir is not None:
        # Rehearse on a scratch copy of the live folder.
        for name in MODEL_FILES:
            if (live_dir / name).exists():
                shutil.copyfile(live_dir / name, target / name)
    try:
        archived = publish_files(cfg.runs_dir / run_id / "report", target, run_id)
    except Exception as exc:
        api(
            cfg,
            "POST",
            f"/internal/runs/{run_id}/published",
            {"published": False, "dry_run": dry_run, "error": str(exc)},
        )
        raise
    tree = sorted(str(p.relative_to(target)) for p in target.rglob("*") if p.is_file())
    print(
        ("dry run: would publish" if dry_run else "published")
        + f" run {run_id}: {tree}",
        flush=True,
    )
    api(
        cfg,
        "POST",
        f"/internal/runs/{run_id}/published",
        {
            "published": not dry_run,
            "dry_run": dry_run,
            "archived": archived,
            "tree": tree,
        },
    )


def publish(cfg: TrainerConfig, dry_run: bool, live_dir: Path | None) -> int:
    """Once, from the trainer's hand-off file."""
    if cfg.handoff is None or not cfg.handoff.exists():
        print("no hand-off from the trainer: nothing to publish", flush=True)
        return 0
    handoff = json.loads(cfg.handoff.read_text())
    run_id = handoff.get("run_id")
    if not run_id or not handoff.get("promote"):
        print(f"run {run_id or '-'}: nothing to publish", flush=True)
        return 0
    publish_run(cfg, run_id, dry_run, live_dir)
    return 0


def publish_pending(
    cfg: TrainerConfig,
    dry_run: bool,
    live_dir: Path | None,
    retry_seconds: float,
    now: float,
) -> str | None:
    """One reconcile pass: publish the newest promoted run if the collector
    says it is not published yet. A failed attempt is retried after
    retry_seconds. Returns the run published, if any."""
    run = api(cfg, "GET", "/internal/publish")["run"]
    if run is None:
        return None
    last = run.get("published") or {}
    if last.get("error") and now - last.get("at", 0) < retry_seconds:
        return None
    publish_run(cfg, run["id"], dry_run, live_dir)
    return run["id"]


def watch(
    cfg: TrainerConfig,
    dry_run: bool,
    live_dir: Path | None,
    interval: float,
    retry_seconds: float,
    heartbeat: Path | None = None,
):
    """Publish promoted runs as they appear, for as long as the pod runs.
    `heartbeat` is touched before the first pass and after every pass, failed
    or not, for the container's probes."""
    while True:
        if heartbeat is not None:
            heartbeat.touch()
        try:
            publish_pending(cfg, dry_run, live_dir, retry_seconds, time.time())
        except Exception:
            log.exception("publish pass failed; retrying")
        if heartbeat is not None:
            heartbeat.touch()
        time.sleep(interval)


# ---------------------------------------------------------------------- seed


def fetch(url: str, dest: Path):
    if dest.exists() and dest.stat().st_size > 0:
        print(f"have {dest}", flush=True)
        return
    dest.parent.mkdir(parents=True, exist_ok=True)
    tmp = dest.with_name(f".{dest.name}.part")
    print(f"downloading {url} -> {dest}", flush=True)
    with urllib.request.urlopen(url, timeout=120) as resp, open(tmp, "wb") as fh:
        shutil.copyfileobj(resp, fh, length=1 << 20)
    os.replace(tmp, dest)


def seed(cfg: TrainerConfig, segment_python: str, whisper_url: str) -> int:
    """Download the datasets and build the static feature sets: steps 1-3, once."""
    marker = cfg.work_dir / ".seeded"
    if marker.exists():
        print(f"{marker} exists: already seeded", flush=True)
        return 0
    env = {
        "OKAY_GLADOS_WORK_DIR": str(cfg.work_dir),
        "OKAY_GLADOS_LOG_DIR": str(cfg.work_dir / "logs"),
    }
    logs = cfg.work_dir / "logs"
    run_step(cfg, "step1_download_data.py", [], env, logs / "seed-step1.log")
    run_step(cfg, "step2_generate_tts.py", [], env, logs / "seed-step2.log")
    run_step(
        cfg,
        "step3_build_features.py",
        ["--only", "positive", "adversarial", "ambient"],
        env,
        logs / "seed-step3.log",
    )
    recordings = Path(os.environ.get("OKAY_GLADOS_RECORDINGS_DIR", ""))
    if recordings.is_dir() and any(recordings.rglob("*.*")):
        # The phone recording sessions are split into takes once, with the
        # same Whisper model family the collector uses.
        model = cfg.work_dir / "models" / Path(whisper_url).name
        fetch(whisper_url, model)
        cmd = [
            segment_python,
            str(cfg.pipeline_dir / "okay_glados" / "tools" / "segment_recordings.py"),
            "--model",
            str(model),
        ]
        print(f"+ {shlex.join(cmd)}", flush=True)
        subprocess.run(cmd, check=True, env={**os.environ, **env}, cwd=cfg.pipeline_dir)
    marker.write_text(time.strftime("%Y-%m-%dT%H:%M:%SZ\n", time.gmtime()))
    return 0
