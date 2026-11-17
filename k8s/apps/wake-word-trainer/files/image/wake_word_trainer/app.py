"""The collector: capture endpoint, review UI, run history, metrics, trainer API.

Routes and who reaches them (oauth2-proxy owns the hostname):
  POST /capture              the Voice PE; skips SSO, needs X-Capture-Token
  /internal/...              the trainer Job, in-cluster; needs X-Internal-Token
  /metrics, /healthz         the scraper and the kubelet
  everything else            people, behind SSO
"""

import hmac
import logging
import re
import time
import uuid
from contextlib import asynccontextmanager
from pathlib import Path

from fastapi import Body, FastAPI, Form, HTTPException, Request, Response
from fastapi.responses import (
    FileResponse,
    HTMLResponse,
    PlainTextResponse,
    RedirectResponse,
)
from fastapi.templating import Jinja2Templates
from jinja2 import DictLoader, Environment

from . import audio, charts, kube
from .db import Store
from .processor import Processor
from .settings import LABELS, POSITIVE_LABELS, Settings
from .templates import TEMPLATES as PAGES

# Values of wake_word_trainer_last_run_status; the RunFailed alert matches 3.
RUN_STATUS_CODES = {"succeeded": 1, "running": 2, "failed": 3}

log = logging.getLogger(__name__)

TEMPLATES = Jinja2Templates(env=Environment(loader=DictLoader(PAGES), autoescape=True))
TEMPLATES.env.filters["ts"] = lambda t: time.strftime(
    "%Y-%m-%d %H:%M UTC", time.gmtime(t)
)
TEMPLATES.env.filters["pct"] = lambda v: "" if v is None else f"{100 * v:.1f}%"
TEMPLATES.env.filters["num"] = lambda v: "" if v is None else f"{v:.2f}"
REVIEW_STATES = ("pending", "auto", "flagged", "confirmed", "discarded")
RUN_FILE_NAMES = re.compile(r"^[A-Za-z0-9._/-]+$")


def _safe(value: str | None, default: str) -> str:
    return re.sub(r"[^A-Za-z0-9_-]", "_", value or "")[:64] or default


def _check_token(given: str | None, expected: str):
    if not expected or not given or not hmac.compare_digest(given, expected):
        raise HTTPException(401, "bad or missing token")


def create_app(
    settings: Settings | None = None, transcriber=None, start_processor=True
) -> FastAPI:
    settings = settings or Settings()
    store = Store(settings.db_path)
    if transcriber is None:
        from .transcribe import WhisperTranscriber

        transcriber = WhisperTranscriber(
            settings.whisper_model, settings.whisper_threads
        )
    processor = Processor(store, settings.clips_dir, transcriber)

    @asynccontextmanager
    async def lifespan(_app):
        if start_processor:
            processor.start()
        yield
        processor.stop.set()
        processor.wake.set()

    app = FastAPI(
        title="Wake word trainer", lifespan=lifespan, docs_url=None, redoc_url=None
    )
    app.state.store, app.state.settings, app.state.processor = (
        store,
        settings,
        processor,
    )

    def user_of(request: Request) -> str:
        return (
            request.headers.get(settings.user_header)
            or request.headers.get("X-Forwarded-User")
            or "unknown"
        )

    def page(request: Request, name: str, **ctx):
        return TEMPLATES.TemplateResponse(
            request, name, {"user": user_of(request), **ctx}
        )

    # ------------------------------------------------------------- capture

    async def capture(request: Request):
        _check_token(request.headers.get("X-Capture-Token"), settings.capture_token)
        device = _safe(request.headers.get("X-Device"), "device")
        now = time.time()
        body = await request.body()
        if relabel := request.headers.get("X-Relabel-Last"):
            label = _safe(relabel, "")
            if label not in LABELS:
                raise HTTPException(400, f"unknown label {label!r}")
            clip_id = store.relabel_last(device, label, now, settings.relabel_window_s)
            if clip_id is None:
                raise HTTPException(404, "no recent detection to re-tag")
            log.info("re-tagged %s from %s as %s", clip_id, device, label)
            processor.wake.set()
            return Response(status_code=204)
        label = _safe(request.headers.get("X-Label"), "")
        if label not in LABELS:
            raise HTTPException(400, f"unknown label {label!r}")
        if len(body) > 8 * 1024 * 1024:
            raise HTTPException(413, "too large")
        try:
            samples = audio.parse_wav(body, settings.max_clip_seconds)
        except audio.AudioError as exc:
            raise HTTPException(400, str(exc)) from exc
        clip_id = uuid.uuid4().hex
        rel = f"raw/{time.strftime('%Y/%m', time.gmtime(now))}/{clip_id}.wav"
        audio.write(settings.clips_dir / rel, samples)
        if label != "true_reject":
            # An ambient sample just before a detection or a tagged miss
            # probably contains (part of) the wake word.
            store.discard_recent_ambient(device, now, settings.ambient_discard_s)
        store.add_clip(clip_id, device, label, rel, audio.duration(samples), now)
        processor.wake.set()
        log.info(
            "saved %s from %s as %s (%.1f s)",
            clip_id,
            device,
            label,
            audio.duration(samples),
        )
        return Response(status_code=204)

    app.post("/capture")(capture)
    # The stand-in receiver's contract was POST to the root.
    app.post("/")(capture)

    # ------------------------------------------------------------------ UI

    @app.get("/", response_class=HTMLResponse)
    def index(request: Request):
        counts = store.clip_counts()
        table = {lbl: {s: 0 for s in REVIEW_STATES} for lbl in (*LABELS, "discarded")}
        for r in counts:
            table.setdefault(r["label"], {s: 0 for s in REVIEW_STATES})
            table[r["label"]][r["review"]] = (
                table[r["label"]].get(r["review"], 0) + r["n"]
            )
        runs = store.runs()
        return page(
            request,
            "index.html",
            table=table,
            states=REVIEW_STATES,
            queue=sum(
                r["n"]
                for r in counts
                if r["status"] == "pending" and r["label"] != "discarded"
            ),
            errors=sum(r["n"] for r in counts if r["status"] == "error"),
            flagged=sum(r["n"] for r in counts if r["review"] == "flagged"),
            new=store.new_usable_counts(),
            gate=store.meta("last_gate"),
            runs=list(reversed(runs))[:5],
            settings=settings,
            processor_error=processor.last_error,
        )

    @app.get("/review", response_class=HTMLResponse)
    def review_page(request: Request):
        clips = store.query(
            "SELECT * FROM clips WHERE review = 'flagged' OR status = 'error' ORDER BY received_at"
        )
        return page(request, "review.html", clips=clips, labels=LABELS)

    @app.get("/clips", response_class=HTMLResponse)
    def clips_page(
        request: Request, label: str = "", review: str = "", limit: int = 200
    ):
        sql, args = "SELECT * FROM clips WHERE 1 = 1", []
        if label:
            sql, args = sql + " AND label = ?", [*args, label]
        if review:
            sql, args = sql + " AND review = ?", [*args, review]
        clips = store.query(
            sql + " ORDER BY received_at DESC LIMIT ?", *args, min(limit, 1000)
        )
        return page(
            request,
            "clips.html",
            clips=clips,
            labels=LABELS,
            states=REVIEW_STATES,
            label=label,
            review=review,
        )

    def get_clip(clip_id: str) -> dict:
        clip = store.one("SELECT * FROM clips WHERE id = ?", clip_id)
        if clip is None:
            raise HTTPException(404)
        return clip

    @app.get("/clips/{clip_id}", response_class=HTMLResponse)
    def clip_page(request: Request, clip_id: str):
        return page(request, "clip.html", clip=get_clip(clip_id), labels=LABELS)

    @app.get("/clips/{clip_id}/audio")
    def clip_audio(clip_id: str, n: int | None = None):
        clip = get_clip(clip_id)
        rel = clip["path"]
        if n is not None:
            derived = clip.get("derived") or []
            if not 0 <= n < len(derived):
                raise HTTPException(404)
            rel = derived[n]["path"]
        return FileResponse(settings.clips_dir / rel, media_type="audio/wav")

    @app.post("/clips/{clip_id}/review")
    def clip_review(
        request: Request,
        clip_id: str,
        action: str = Form(...),
        label: str = Form(""),
        back: str = Form("/review"),
    ):
        if action not in ("confirm", "relabel", "discard", "retry"):
            raise HTTPException(400, "unknown action")
        if action == "relabel" and label not in LABELS:
            raise HTTPException(400, "unknown label")
        try:
            store.review(clip_id, action, user_of(request), time.time(), label or None)
        except KeyError as exc:
            raise HTTPException(404) from exc
        processor.wake.set()
        if not back.startswith("/") or back.startswith("//"):
            back = "/review"
        return RedirectResponse(back, status_code=303)

    @app.get("/runs", response_class=HTMLResponse)
    def runs_page(request: Request):
        runs = store.runs()
        done = [r for r in runs if r["status"] == "succeeded" and r.get("result")]

        def metric(r, *path):
            v = r["result"]
            for p in path:
                v = (v or {}).get(p)
            return v

        frr = {
            "new model, real clips": [
                (r["started_at"], metric(r, "candidate", "recorded_frr")) for r in done
            ],
            "published model, real clips": [
                (r["started_at"], metric(r, "baseline", "recorded_frr")) for r in done
            ],
        }
        faph = {
            "false accepts/h": [
                (r["started_at"], metric(r, "candidate", "faph")) for r in done
            ]
        }
        cutoff = {
            "cutoff": [
                (r["started_at"], metric(r, "candidate", "operating", "cutoff"))
                for r in done
            ]
        }
        promoted = [r["started_at"] for r in done if r["promoted"]]

        week = 7 * 86400
        by_label: dict[float, dict[str, int]] = {}
        by_review: dict[float, dict[str, int]] = {}
        for c in store.query("SELECT received_at, label, review FROM clips"):
            b = c["received_at"] - c["received_at"] % week
            by_label.setdefault(b, {}).setdefault(c["label"], 0)
            by_label[b][c["label"]] += 1
            by_review.setdefault(b, {}).setdefault(c["review"], 0)
            by_review[b][c["review"]] += 1
        return page(
            request,
            "runs.html",
            runs=list(reversed(runs)),
            frr_chart=charts.line_chart(
                frr, lambda v: f"{100 * v:.0f}%", promoted, "false reject rate"
            ),
            faph_chart=charts.line_chart(
                faph, lambda v: f"{v:.2f}", promoted, "false accepts per hour"
            ),
            cutoff_chart=charts.line_chart(
                cutoff, lambda v: f"{v:.2f}", promoted, "cutoff"
            ),
            label_chart=charts.stacked_bars(
                sorted(by_label.items()), [*LABELS, "discarded"], "clips by label"
            ),
            review_chart=charts.stacked_bars(
                sorted(by_review.items()), list(REVIEW_STATES), "clips by review state"
            ),
        )

    def run_dir(run_id: str) -> Path:
        if not re.fullmatch(r"[A-Za-z0-9-]+", run_id):
            raise HTTPException(404)
        return settings.runs_dir / run_id

    @app.get("/runs/{run_id}", response_class=HTMLResponse)
    def run_page(request: Request, run_id: str):
        run = store.run(run_id)
        if run is None:
            raise HTTPException(404)
        d = run_dir(run_id)
        files = (
            sorted(str(p.relative_to(d)) for p in d.rglob("*") if p.is_file())
            if d.exists()
            else []
        )
        report = (
            (d / "report" / "REPORT.md").read_text()
            if (d / "report" / "REPORT.md").exists()
            else None
        )
        return page(request, "run.html", run=run, files=files, report=report)

    @app.get("/runs/{run_id}/files/{path:path}")
    def run_file(run_id: str, path: str):
        d = run_dir(run_id).resolve()
        target = (d / path).resolve()
        if (
            not RUN_FILE_NAMES.match(path)
            or d not in target.parents
            or not target.is_file()
        ):
            raise HTTPException(404)
        media = (
            "text/plain; charset=utf-8"
            if target.suffix in (".md", ".log", ".tsv", ".json", ".yaml")
            else None
        )
        return FileResponse(target, media_type=media)

    @app.post("/train-now")
    def train_now(request: Request):
        if not (settings.namespace and settings.cronjob):
            raise HTTPException(503, "no CronJob configured")
        if store.one("SELECT id FROM runs WHERE status = 'running'"):
            raise HTTPException(409, "a run is already in progress")
        name = kube.create_training_job(settings.namespace, settings.cronjob)
        log.info("%s started training job %s", user_of(request), name)
        return RedirectResponse("/runs", status_code=303)

    # ------------------------------------------------- probes and metrics

    @app.get("/healthz", response_class=PlainTextResponse)
    def healthz():
        store.one("SELECT 1 AS ok")
        return "ok"

    @app.get("/metrics", response_class=PlainTextResponse)
    def metrics():
        return render_metrics(store, processor)

    # --------------------------------------------------------- trainer API

    def internal(request: Request):
        _check_token(request.headers.get("X-Internal-Token"), settings.internal_token)

    @app.post("/internal/runs")
    def internal_start(request: Request, payload: dict = Body(default={})):
        internal(request)
        run, gate = store.start_run(settings, bool(payload.get("forced")), time.time())
        return {"run": run, "gate": gate}

    @app.get("/internal/runs/{run_id}/dataset")
    def internal_dataset(request: Request, run_id: str):
        internal(request)
        if store.run(run_id) is None:
            raise HTTPException(404)
        rows = []
        for clip in store.usable_clips():
            for d in clip.get("derived") or []:
                rows.append(
                    {
                        "clip_id": clip["id"],
                        "label": clip["label"],
                        "split": clip["split"],
                        "kind": d["kind"],
                        "path": d["path"],
                    }
                )
        return {"clips": rows}

    @app.post("/internal/runs/{run_id}/result")
    def internal_result(request: Request, run_id: str, payload: dict = Body(...)):
        internal(request)
        if store.run(run_id) is None:
            raise HTTPException(404)
        status = payload.get("status")
        if status not in ("succeeded", "failed"):
            raise HTTPException(400, "status must be succeeded or failed")
        store.finish_run(
            run_id,
            status,
            time.time(),
            payload.get("result"),
            payload.get("error"),
            payload.get("clip_ids") or (),
        )
        return {"ok": True}

    @app.get("/internal/publish")
    def internal_to_publish(request: Request):
        internal(request)
        return {"run": store.to_publish()}

    @app.post("/internal/runs/{run_id}/published")
    def internal_published(request: Request, run_id: str, payload: dict = Body(...)):
        internal(request)
        if store.run(run_id) is None:
            raise HTTPException(404)
        store.set_published(run_id, {**payload, "at": time.time()})
        return {"ok": True}

    return app


def render_metrics(store: Store, processor: Processor) -> str:
    lines = []

    def gauge(name, help_, samples):
        lines.append(f"# HELP {name} {help_}")
        lines.append(f"# TYPE {name} gauge")
        for labels, value in samples:
            lbl = ",".join(f'{k}="{v}"' for k, v in labels.items())
            lines.append(f"{name}{{{lbl}}} {value}" if lbl else f"{name} {value}")

    counts = store.clip_counts()
    by = {}
    for r in counts:
        key = (r["label"], r["review"])
        by[key] = by.get(key, 0) + r["n"]
    gauge(
        "wake_word_trainer_clips",
        "Clips by current label and review state.",
        [({"label": k[0], "review": k[1]}, v) for k, v in sorted(by.items())],
    )
    gauge(
        "wake_word_trainer_queue_depth",
        "Clips waiting to be transcribed.",
        [
            (
                {},
                sum(
                    r["n"]
                    for r in counts
                    if r["status"] == "pending" and r["label"] != "discarded"
                ),
            )
        ],
    )
    gauge(
        "wake_word_trainer_processing_errors",
        "Clips whose processing failed.",
        [({}, sum(r["n"] for r in counts if r["status"] == "error"))],
    )
    new = store.new_usable_counts()
    gauge(
        "wake_word_trainer_new_usable_clips",
        "Usable clips no run has trained on yet.",
        [({"label": k}, v) for k, v in sorted(new.items())],
    )
    gauge(
        "wake_word_trainer_new_usable_positives",
        "New usable positives, as the gate counts them.",
        [({}, sum(new.get(k, 0) for k in POSITIVE_LABELS))],
    )
    last_capture = store.one("SELECT MAX(received_at) AS t FROM clips")["t"]
    if last_capture:
        gauge(
            "wake_word_trainer_last_capture_timestamp_seconds",
            "When the newest clip arrived.",
            [({}, last_capture)],
        )
    runs = store.runs()
    status_counts = {}
    for r in runs:
        status_counts[r["status"]] = status_counts.get(r["status"], 0) + 1
    gauge(
        "wake_word_trainer_runs",
        "Training runs by status.",
        [({"status": s}, n) for s, n in sorted(status_counts.items())],
    )
    if runs:
        last = runs[-1]
        gauge(
            "wake_word_trainer_last_run_start_timestamp_seconds",
            "Start of the newest run.",
            [({"status": last["status"]}, last["started_at"])],
        )
        gauge(
            "wake_word_trainer_last_run_status",
            "Status of the newest run: 1 succeeded, 2 running, 3 failed.",
            [({}, RUN_STATUS_CODES.get(last["status"], 0))],
        )
    ok = [r for r in runs if r["status"] == "succeeded"]
    if ok:
        gauge(
            "wake_word_trainer_last_successful_run_timestamp_seconds",
            "End of the newest successful run.",
            [({}, ok[-1]["finished_at"])],
        )
    promoted = [r for r in ok if r["promoted"]]
    if promoted:
        gauge(
            "wake_word_trainer_last_promotion_timestamp_seconds",
            "End of the newest run whose model beat the published one.",
            [({}, promoted[-1]["finished_at"])],
        )
    gauge(
        "wake_word_trainer_publish_pending",
        "1 while the newest promoted model is not published (or its publish failed).",
        [({}, int(store.to_publish() is not None))],
    )
    gate = store.meta("last_gate")
    if gate:
        gauge(
            "wake_word_trainer_last_gate_timestamp_seconds",
            "When the nightly gate last ran.",
            [({}, gate["checked_at"])],
        )
    gauge(
        "wake_word_trainer_processor_alive",
        "1 while the processing thread runs.",
        [({}, int(processor.is_alive()))],
    )
    return "\n".join(lines) + "\n"
