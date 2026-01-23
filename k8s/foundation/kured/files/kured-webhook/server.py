import json
import os
import re
import sys
import urllib.request
from dataclasses import dataclass
from datetime import datetime, timedelta, timezone
from http.server import BaseHTTPRequestHandler, HTTPServer
from typing import Iterable, Optional, Tuple


@dataclass(frozen=True)
class Config:
    alertmanager_url: str
    drain_silence_alerts: list[str]
    drain_silence_duration: str
    post_reboot_silence_alerts: list[str]
    post_reboot_silence_duration: str
    reboot_alert_ttl: str


CONFIG: Optional[Config] = None


def parse_duration(value: str) -> timedelta:
    value = value.strip()
    if not value:
        return timedelta(0)
    unit = value[-1]
    amount = int(value[:-1])
    if unit == "s":
        return timedelta(seconds=amount)
    if unit == "m":
        return timedelta(minutes=amount)
    if unit == "h":
        return timedelta(hours=amount)
    raise ValueError(f"unsupported duration: {value}")


def isoformat(dt: datetime) -> str:
    return dt.astimezone(timezone.utc).isoformat().replace("+00:00", "Z")


def parse_message(body: bytes) -> Tuple[Optional[str], Optional[str], str]:
    message = body.decode("utf-8").strip()
    if message.startswith("{"):
        try:
            data = json.loads(message)
            message = str(data.get("message", "")).strip()
        except json.JSONDecodeError:
            pass
    fields = dict(re.findall(r"(\w+)=([^\s]+)", message))
    return fields.get("event"), fields.get("node"), message


def build_silence_payload(
    alertname: str,
    duration: timedelta,
    node: str,
    reason: str,
    now: Optional[datetime] = None,
) -> dict:
    now = now or datetime.now(timezone.utc)
    return {
        "matchers": [
            {"name": "alertname", "value": alertname, "isRegex": False},
        ],
        "startsAt": isoformat(now),
        "endsAt": isoformat(now + duration),
        "createdBy": "kured-webhook",
        "comment": f"{reason} (node={node})",
    }


def build_reboot_alert_payload(
    node: str, resolved: bool, now: Optional[datetime] = None
) -> list[dict]:
    now = now or datetime.now(timezone.utc)
    ends_at = now if resolved else now + parse_duration(config().reboot_alert_ttl)
    return [
        {
            "labels": {
                "alertname": "KuredNodeRebooting",
                "node": node,
                "namespace": "kured",
                "severity": "info",
            },
            "annotations": {
                "summary": f"Node {node} is rebooting",
                "description": f"Kured is rebooting node {node}",
            },
            "startsAt": isoformat(now),
            "endsAt": isoformat(ends_at),
        }
    ]


def post_json(path: str, payload: dict | list) -> None:
    data = json.dumps(payload).encode("utf-8")
    req = urllib.request.Request(
        f"{config().alertmanager_url}{path}",
        data=data,
        headers={"Content-Type": "application/json"},
        method="POST",
    )
    with urllib.request.urlopen(req, timeout=5) as resp:
        resp.read()


def create_silence(alertname: str, duration: timedelta, node: str, reason: str) -> None:
    payload = build_silence_payload(alertname, duration, node, reason)
    post_json("/api/v2/silences", payload)


def set_reboot_alert(node: str, resolved: bool) -> None:
    post_json("/api/v1/alerts", build_reboot_alert_payload(node, resolved))


def config() -> Config:
    if CONFIG is None:
        raise RuntimeError("config not initialized")
    return CONFIG


def load_config(env: dict[str, str]) -> Config:
    alertmanager_url = env.get("ALERTMANAGER_URL", "").rstrip("/")
    if not alertmanager_url:
        raise ValueError("ALERTMANAGER_URL is required")

    def split_csv(value: str) -> list[str]:
        return [item for item in value.split(",") if item]

    return Config(
        alertmanager_url=alertmanager_url,
        drain_silence_alerts=split_csv(env.get("DRAIN_SILENCE_ALERTS", "")),
        drain_silence_duration=env.get("DRAIN_SILENCE_DURATION", "3h"),
        post_reboot_silence_alerts=split_csv(env.get("POST_REBOOT_SILENCE_ALERTS", "")),
        post_reboot_silence_duration=env.get("POST_REBOOT_SILENCE_DURATION", "15m"),
        reboot_alert_ttl=env.get("REBOOT_ALERT_TTL", "6h"),
    )


def handle_event(event: str, node: str) -> bool:
    if event == "drain":
        duration = parse_duration(config().drain_silence_duration)
        for alertname in config().drain_silence_alerts:
            create_silence(alertname, duration, node, "kured drain")
        set_reboot_alert(node, resolved=False)
        return True
    if event == "uncordon":
        duration = parse_duration(config().post_reboot_silence_duration)
        for alertname in config().post_reboot_silence_alerts:
            create_silence(alertname, duration, node, "kured uncordon")
        set_reboot_alert(node, resolved=True)
        return True
    return False


def read_body(handler: BaseHTTPRequestHandler) -> bytes:
    length = int(handler.headers.get("Content-Length", "0"))
    return handler.rfile.read(length) if length else b""


def format_alert_list(items: Iterable[str]) -> str:
    return ",".join(items)


class Handler(BaseHTTPRequestHandler):
    def do_POST(self) -> None:
        body = read_body(self)
        event, node, raw = parse_message(body)

        if not event or not node:
            self.send_response(400)
            self.end_headers()
            self.wfile.write(b"missing event or node")
            return

        try:
            handled = handle_event(event, node)
            if not handled:
                print(f"Unhandled event: {event} ({raw})", file=sys.stderr)
            self.send_response(200)
            self.end_headers()
            self.wfile.write(b"ok")
        except Exception as exc:
            print(f"Failed handling event {event} for {node}: {exc}", file=sys.stderr)
            self.send_response(500)
            self.end_headers()
            self.wfile.write(b"error")


def main() -> None:
    global CONFIG
    try:
        CONFIG = load_config(os.environ)
    except ValueError as exc:
        print(str(exc), file=sys.stderr)
        sys.exit(1)

    port = int(os.environ.get("PORT", "8080"))
    server = HTTPServer(("0.0.0.0", port), Handler)
    print(
        "Listening on :{} (drain alerts: {}, post-reboot alerts: {})".format(
            port,
            format_alert_list(config().drain_silence_alerts),
            format_alert_list(config().post_reboot_silence_alerts),
        )
    )
    server.serve_forever()


if __name__ == "__main__":
    main()
