"""Removes a movie from Radarr when the last person who wanted it withdraws.

SeerrNG lets users withdraw their own requests, but only cancels downloads
still in progress. The movie and its files stay in Radarr until an admin uses
"Delete from Library". This runs that deletion for them, after SeerrNG has
accepted the withdrawal, only when all of these hold:

- the request is the withdrawing user's own, and is for a movie (not 4K);
- no other request for the movie remains;
- every library copy SeerrNG would delete is in the one Radarr this knows,
  and Radarr added it no earlier than the request was made, so the request is
  what brought it in;
- if Radarr has a file, no other Jellyfin user has played any of it.

Anything it can't verify counts as a reason to keep the movie.
"""

from __future__ import annotations

import http.client
import json
import urllib.parse
from dataclasses import dataclass
from datetime import datetime, timedelta
from typing import Callable, Protocol

API = "/api/v1"

# server/constants/media.ts MediaStatus
PARTIALLY_AVAILABLE = 4
AVAILABLE = 5


class Unavailable(Exception):
    """A service needed to verify the deletion did not answer as expected."""


class Services(Protocol):
    def seerr(self, method: str, path: str, body: dict | None = None): ...
    def radarr_movie(self, movie_id: int) -> dict | None: ...
    def jellyfin_users(self) -> list[dict] | None: ...
    def jellyfin_user_data(self, user_id: str, item_id: str) -> dict | None: ...


@dataclass(frozen=True)
class Outcome:
    deleted: bool
    reason: str


def parse_time(value: str) -> datetime:
    return datetime.fromisoformat(value.replace("Z", "+00:00"))


def normalize_id(value: str | None) -> str | None:
    """Jellyfin IDs appear both with and without dashes."""
    return value.replace("-", "").lower() if value else None


def played(user_data: dict) -> bool:
    return bool(
        user_data.get("Played")
        or user_data.get("PlayCount")
        or user_data.get("PlaybackPositionTicks")
    )


def clean_up(
    services: Services,
    request: dict,
    actor_id: int | None,
    radarr_server_id: int,
    added_grace: timedelta,
) -> Outcome:
    """Deletes the request's movie from Radarr if it is safe to, after the
    request itself has been withdrawn. `request` is SeerrNG's view of it from
    before the withdrawal."""
    requester = (request.get("requestedBy") or {}).get("id")
    media = request.get("media") or {}
    media_id, tmdb_id = media.get("id"), media.get("tmdbId")

    if request.get("type") != "movie":
        return Outcome(False, "only movies are cleaned up")
    if request.get("is4k"):
        return Outcome(False, "4K requests are not cleaned up")
    if actor_id is None or actor_id != requester:
        return Outcome(False, "withdrawn by someone other than the requester")
    if not media_id or not tmdb_id:
        return Outcome(False, "the request has no media record")

    movie = services.seerr("GET", f"{API}/movie/{tmdb_id}")
    media_info = movie.get("mediaInfo") or {}
    if media_info.get("id") != media_id:
        return Outcome(False, "the media record changed")
    if media_info.get("requests"):
        return Outcome(False, "other requests for it remain")

    plan = services.seerr("GET", f"{API}/media/{media_id}/library")
    targets = plan.get("targets") or []
    if not targets:
        return Outcome(False, "it is not in Radarr")

    requested_at = parse_time(request["createdAt"])
    has_file = False
    for target in targets:
        if (
            target.get("serviceType") != "radarr"
            or target.get("serviceId") != radarr_server_id
            or target.get("quality") != "HD"
        ):
            return Outcome(
                False, f"a copy is in another service ({target.get('service')})"
            )
        radarr = services.radarr_movie(int(target["externalId"]))
        if radarr is None or radarr.get("tmdbId") != tmdb_id:
            return Outcome(False, "Radarr no longer has the copy SeerrNG found")
        if parse_time(radarr["added"]) < requested_at - added_grace:
            return Outcome(False, "it was in Radarr before the request")
        has_file = has_file or bool(radarr.get("hasFile"))

    if has_file or media_info.get("status") in (PARTIALLY_AVAILABLE, AVAILABLE):
        reason = others_have_played(services, requester, media_info)
        if reason:
            return Outcome(False, reason)

    services.seerr(
        "DELETE", f"{API}/media/{media_id}/library", {"token": plan["token"]}
    )
    return Outcome(True, "deleted from Radarr with its files")


def others_have_played(
    services: Services, requester: int, media_info: dict
) -> str | None:
    """Why the movie must be kept for someone else, or None if nobody else
    has played it."""
    item_id = media_info.get("jellyfinMediaId")
    if not item_id:
        return "it has a file that SeerrNG has not matched in Jellyfin yet"
    users = services.jellyfin_users()
    if users is None:
        return "there is no Jellyfin API key to check who has watched it"
    owner = services.seerr("GET", f"{API}/user/{requester}")
    requester_jellyfin = normalize_id(owner.get("jellyfinUserId"))
    for user in users:
        if normalize_id(user.get("Id")) == requester_jellyfin:
            continue
        user_data = services.jellyfin_user_data(user["Id"], item_id)
        if user_data is None:
            return "Jellyfin does not have the item SeerrNG matched"
        if played(user_data):
            return "another Jellyfin user has watched it"
    return None


class HttpServices:
    """SeerrNG as the owner, Radarr, and optionally Jellyfin."""

    def __init__(
        self,
        seerr: urllib.parse.SplitResult,
        seerr_key: str,
        radarr: urllib.parse.SplitResult,
        radarr_key: str,
        jellyfin: urllib.parse.SplitResult | None,
        jellyfin_key: str | None,
    ):
        self._seerr = (seerr, {"X-Api-Key": seerr_key})
        self._radarr = (radarr, {"X-Api-Key": radarr_key})
        self._jellyfin = (
            (jellyfin, {"Authorization": f'MediaBrowser Token="{jellyfin_key}"'})
            if jellyfin and jellyfin_key
            else None
        )

    @staticmethod
    def _call(service, method: str, path: str, body: dict | None = None):
        url, auth = service
        headers = {**auth, "Accept": "application/json"}
        payload = None
        if body is not None:
            payload = json.dumps(body).encode()
            headers["Content-Type"] = "application/json"
        connection = http.client.HTTPConnection(
            url.hostname, url.port or 80, timeout=60
        )
        try:
            connection.request(method, path, body=payload, headers=headers)
            response = connection.getresponse()
            data = response.read()
        except OSError as error:
            raise Unavailable(f"{method} {url.hostname}{path}: {error}") from error
        finally:
            connection.close()
        return response.status, (json.loads(data) if data else None)

    def seerr(self, method, path, body=None):
        status, data = self._call(self._seerr, method, path, body)
        if status >= 300:
            message = (data or {}).get("message") if isinstance(data, dict) else None
            raise Unavailable(f"SeerrNG {method} {path} returned {status}: {message}")
        return data

    def radarr_movie(self, movie_id):
        status, data = self._call(self._radarr, "GET", f"/api/v3/movie/{movie_id}")
        if status == 404:
            return None
        if status != 200:
            raise Unavailable(f"Radarr GET movie {movie_id} returned {status}")
        return data

    def jellyfin_users(self):
        if self._jellyfin is None:
            return None
        status, data = self._call(self._jellyfin, "GET", "/Users")
        if status != 200:
            raise Unavailable(f"Jellyfin GET /Users returned {status}")
        return data

    def jellyfin_user_data(self, user_id, item_id):
        query = urllib.parse.urlencode({"userId": user_id})
        status, data = self._call(
            self._jellyfin,
            "GET",
            f"/Items/{urllib.parse.quote(item_id)}?{query}",
        )
        if status in (400, 404):
            return None
        if status != 200:
            raise Unavailable(f"Jellyfin GET item returned {status}")
        return data.get("UserData") or {}


def run(
    services: Services,
    request: dict,
    actor_id: int | None,
    radarr_server_id: int,
    added_grace: timedelta,
    log: Callable[[str], None],
) -> Outcome:
    """clean_up, logging what happened and turning failures into a kept movie."""
    label = f"request {request.get('id')} (tmdb {(request.get('media') or {}).get('tmdbId')})"
    try:
        outcome = clean_up(services, request, actor_id, radarr_server_id, added_grace)
    except Exception as error:  # noqa: BLE001 -- the movie is kept on any failure
        outcome = Outcome(False, f"could not verify: {error}")
    verb = "removed" if outcome.deleted else "kept"
    log(f"withdrawn {label}: {verb}, {outcome.reason}")
    return outcome
