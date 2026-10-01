"""Entry point: the API on one port, the sandbox webhook receiver on another."""

import asyncio
import contextlib
import logging
import os
import re

import httpx
import uvicorn
from fastapi import FastAPI

from . import proxy, routes_sandboxes, webhooks
from .db import Database
from .kube import Kube
from .sandboxes import SandboxManager, load_specs
from .settings import Settings

log = logging.getLogger("app_server")

# Browsers authenticate the runtime WebSocket with ?session_api_key=, and both
# uvicorn and httpx log request URLs.
_SECRET_PARAM = re.compile(r"(session_api_key=)[^&\s\"']+")


class RedactSecrets(logging.Filter):
    def filter(self, record: logging.LogRecord) -> bool:
        message = record.getMessage()
        redacted = _SECRET_PARAM.sub(r"\1<redacted>", message)
        if redacted != message:
            record.msg, record.args = redacted, None
        return True


class State:
    """Everything the routes share, attached to both apps' `state`."""

    def __init__(self, settings: Settings, kube: Kube, db: Database):
        self.settings = settings
        self.kube = kube
        self.db = db
        self.sandboxes = SandboxManager(
            db=db,
            kube=kube,
            namespace=settings.namespace,
            specs=load_specs(settings.specs_dir),
            webhook_url=settings.webhook_url,
            agent_server_port=settings.agent_server_port,
        )
        # Streams can stay open for a whole agent turn.
        self.http = httpx.AsyncClient(timeout=httpx.Timeout(30, read=None))
        self.event_sink = None

    def attach(self, app: FastAPI) -> FastAPI:
        for k, v in vars(self).items():
            setattr(app.state, k, v)
        return app


def build_api(state: State) -> FastAPI:
    app = FastAPI(title="openhands-app-server", docs_url=None, redoc_url=None)
    app.include_router(routes_sandboxes.router)
    app.include_router(proxy.router)

    @app.get("/healthz", include_in_schema=False)
    async def healthz() -> dict[str, bool]:
        return {"ok": True}

    return state.attach(app)


def build_webhooks(state: State) -> FastAPI:
    app = FastAPI(docs_url=None, redoc_url=None, openapi_url=None)
    app.include_router(webhooks.router)
    return state.attach(app)


async def reconcile_forever(state: State) -> None:
    while True:
        try:
            await state.sandboxes.reconcile()
        except Exception:
            log.exception("reconcile failed")
        await asyncio.sleep(state.settings.reconcile_interval)


async def serve() -> None:
    settings = Settings.from_env()
    settings.data_dir.mkdir(parents=True, exist_ok=True)
    state = State(settings, Kube.in_cluster(), Database(settings.data_dir / "app.db"))
    log.info(
        "sandbox specs: %s (default %s)",
        ", ".join(state.sandboxes.specs) or "none",
        settings.default_spec,
    )
    servers = [
        uvicorn.Server(
            uvicorn.Config(
                build_api(state),
                host="0.0.0.0",
                port=settings.http_port,
                proxy_headers=False,
                log_config=None,
            )
        ),
        uvicorn.Server(
            uvicorn.Config(
                build_webhooks(state),
                host="0.0.0.0",
                port=settings.webhook_port,
                log_config=None,
            )
        ),
    ]
    reconciler = asyncio.create_task(reconcile_forever(state))
    try:
        # Only one of the two receives the signal; whichever stops first takes
        # the other down with it.
        tasks = [asyncio.create_task(s.serve()) for s in servers]
        await asyncio.wait(tasks, return_when=asyncio.FIRST_COMPLETED)
        for s in servers:
            s.should_exit = True
        await asyncio.gather(*tasks, return_exceptions=True)
    finally:
        reconciler.cancel()
        with contextlib.suppress(asyncio.CancelledError):
            await reconciler
        await state.http.aclose()
        await state.kube.close()
        state.db.close()


def main() -> None:
    logging.basicConfig(
        level=os.environ.get("LOG_LEVEL", "INFO").upper(),
        format="%(asctime)s %(levelname)s %(name)s %(message)s",
    )
    # On the handler, so it covers every logger that propagates to root.
    for handler in logging.getLogger().handlers:
        handler.addFilter(RedactSecrets())
    asyncio.run(serve())


if __name__ == "__main__":
    main()
