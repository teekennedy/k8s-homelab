"""Runtime configuration, read once from the environment."""

import os
from dataclasses import dataclass
from pathlib import Path


def _env(name: str, default: str | None = None) -> str:
    value = os.environ.get(name, default)
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
    user_header: str = "X-Forwarded-User"
    agent_server_port: int = 8000
    http_port: int = 8080
    webhook_port: int = 8081
    reconcile_interval: float = 60.0

    @classmethod
    def from_env(cls) -> "Settings":
        return cls(
            namespace=_env("POD_NAMESPACE"),
            data_dir=Path(_env("DATA_DIR", "/data")),
            specs_dir=Path(_env("SPECS_DIR", "/etc/openhands-app-server/specs")),
            default_spec=_env("DEFAULT_SANDBOX_SPEC"),
            public_url=_env("PUBLIC_URL").rstrip("/"),
            internal_url=_env("INTERNAL_URL").rstrip("/"),
            webhook_url=_env("WEBHOOK_URL").rstrip("/"),
            service_key=_env("SERVICE_API_KEY"),
            user_header=_env("USER_HEADER", "X-Forwarded-User"),
            agent_server_port=int(_env("AGENT_SERVER_PORT", "8000")),
            http_port=int(_env("HTTP_PORT", "8080")),
            webhook_port=int(_env("WEBHOOK_PORT", "8081")),
            reconcile_interval=float(_env("RECONCILE_INTERVAL_SECONDS", "60")),
        )
