"""Runtime configuration, read once from the environment."""

import os
from dataclasses import dataclass
from pathlib import Path


def _env(name: str) -> str:
    """Every setting is required: templates/app-server.yaml is the one place
    their values are chosen."""
    value = os.environ.get(name)
    if value is None:
        raise RuntimeError(f"{name} must be set")
    return value


@dataclass(frozen=True)
class Settings:
    namespace: str
    data_dir: Path
    specs_dir: Path
    default_spec: str
    # What a browser uses to reach this server: the canvas origin.
    public_url: str
    # What the automation service, in another pod, uses to reach this server.
    internal_url: str
    # What sandboxes post their webhooks to; a separate port so the
    # NetworkPolicy can admit sandboxes to it and nothing else.
    webhook_url: str
    service_key: str
    # Set by oauth2-proxy on every request it lets through.
    user_header: str
    agent_server_port: int
    http_port: int
    webhook_port: int
    reconcile_interval: float
    # Agent and conversation settings schemas, dumped at pod start from the
    # same agent-server image the sandboxes run.
    schemas_dir: Path
    # First-boot settings and agent profile, from values.yaml.
    settings_seed_file: Path
    # The automation service in the canvas pod, and the key it accepts while
    # it runs in local mode.
    automation_url: str
    automation_api_key: str
    # How long a new conversation may wait for its sandbox to be RUNNING.
    start_timeout: float

    @classmethod
    def from_env(cls) -> "Settings":
        return cls(
            namespace=_env("POD_NAMESPACE"),
            data_dir=Path(_env("DATA_DIR")),
            specs_dir=Path(_env("SPECS_DIR")),
            default_spec=_env("DEFAULT_SANDBOX_SPEC"),
            public_url=_env("PUBLIC_URL").rstrip("/"),
            internal_url=_env("INTERNAL_URL").rstrip("/"),
            webhook_url=_env("WEBHOOK_URL").rstrip("/"),
            service_key=_env("SERVICE_API_KEY"),
            user_header=_env("USER_HEADER"),
            agent_server_port=int(_env("AGENT_SERVER_PORT")),
            http_port=int(_env("HTTP_PORT")),
            webhook_port=int(_env("WEBHOOK_PORT")),
            reconcile_interval=float(_env("RECONCILE_INTERVAL_SECONDS")),
            schemas_dir=Path(_env("SCHEMAS_DIR")),
            settings_seed_file=Path(_env("SETTINGS_SEED_FILE")),
            automation_url=_env("AUTOMATION_URL").rstrip("/"),
            automation_api_key=_env("AUTOMATION_API_KEY"),
            start_timeout=float(_env("START_TIMEOUT_SECONDS")),
        )
