"""Reporting an automation run's outcome to a chat channel.

An automation with `notify: discord` has its outcome posted when the run
completes: the agent's last message, or the reason the run failed, linked to
the conversation. Posted from here rather than by the agent, so the run
reports even when the agent did not finish, and the webhook URL never reaches
a sandbox.
"""

import logging
from pathlib import Path

import httpx

from .settings import read_secret

log = logging.getLogger(__name__)

# Discord's limit on an embed description.
MAX_DESCRIPTION = 4096
GREEN = 0x2ECC71
RED = 0xE74C3C


async def discord(
    http: httpx.AsyncClient,
    webhook_file: Path | None,
    *,
    title: str,
    url: str,
    text: str,
    ok: bool,
) -> None:
    webhook = read_secret(webhook_file)
    if not webhook:
        log.info("no Discord webhook configured; not reporting %s", title)
        return
    if len(text) > MAX_DESCRIPTION:
        text = text[: MAX_DESCRIPTION - 2] + " …"
    resp = await http.post(
        webhook,
        json={
            "username": "OpenHands",
            "embeds": [
                {
                    "title": title[:256],
                    "url": url,
                    "description": text,
                    "color": GREEN if ok else RED,
                }
            ],
            # The agent's text is quoted, never a ping.
            "allowed_mentions": {"parse": []},
        },
        timeout=10,
    )
    resp.raise_for_status()
