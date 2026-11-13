"""Removes what a withdrawn request brought into the library, once nobody
else wants it.

SeerrNG lets users withdraw requests (their own, or anyone's with Manage
Requests), but only cancels downloads still in progress. Everything already
downloaded stays in Radarr or Sonarr until an admin deletes it. This runs
after SeerrNG has accepted a withdrawal, for non-4K requests only, and keeps
anything it can't verify.

A movie is deleted, through SeerrNG's "Delete from Library", when:

- no other request for it remains (declined ones aside);
- every library copy SeerrNG would delete is in the one Radarr this knows,
  added no earlier than the request, so the request is what brought it in;
- if Radarr has a file, no Jellyfin user other than the requester has played
  any of it.

A show is trimmed to what other requests still cover, if the withdrawn
request was approved and so reached Sonarr. Its episodes that no remaining
request covers are unmonitored in Sonarr, and their files deleted, except:

- episodes whose file is older than the request, which it didn't bring in;
- seasons a Jellyfin user other than the requester has played any of (or,
  without a way to check, seasons with any file at all).

A season left with nothing monitored is unmonitored itself. When no request
remains and Sonarr added the show no earlier than the request, the whole
show is deleted through "Delete from Library" instead, provided no season
had to be kept.
"""

from __future__ import annotations

import http.client
import json
import urllib.parse
from collections.abc import Iterable
from dataclasses import dataclass
from datetime import datetime, timedelta
from typing import Callable, Protocol

API = "/api/v1"
PAGE_SIZE = 100

# server/constants/media.ts MediaStatus
PARTIALLY_AVAILABLE = 4
AVAILABLE = 5
# server/constants/media.ts MediaRequestStatus
APPROVED = 2
DECLINED = 3
COMPLETED = 5


class Unavailable(Exception):
    """A service needed to verify the deletion did not answer as expected."""


class Services(Protocol):
    def seerr(self, method: str, path: str, body: dict | None = None): ...
    def radarr_movie(self, movie_id: int) -> dict | None: ...
    def sonarr_series(self, series_id: int) -> dict | None: ...
    def sonarr_episodes(self, series_id: int) -> list[dict]: ...
    def sonarr_episode_files(self, series_id: int) -> list[dict]: ...
    def sonarr_unmonitor_episodes(self, episode_ids: list[int]) -> None: ...
    def sonarr_delete_episode_files(self, file_ids: list[int]) -> None: ...
    def sonarr_update_series(self, series: dict) -> None: ...
    def jellyfin_users(self) -> list[dict] | None: ...
    def jellyfin_user_data(self, user_id: str, item_id: str) -> dict | None: ...
    def jellyfin_episodes(self, user_id: str, series_id: str) -> list[dict] | None: ...


@dataclass(frozen=True)
class Outcome:
    deleted: bool
    reason: str
    # Only part of a show was removed.
    partial: bool = False


@dataclass(frozen=True)
class Context:
    """What the cleanup needs about the withdrawn request and its media."""

    request: dict
    media_id: int
    tmdb_id: int
    media_info: dict
    requester: int | None
    requested_at: datetime
    remaining: list[dict]
    added_grace: timedelta

    @property
    def earliest(self) -> datetime:
        """Library items added from here on came after the request."""
        return self.requested_at - self.added_grace


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


def remaining_requests(services: Services, media_type: str, media_id: int):
    """Every request for the media that still wants it. SeerrNG's media
    details leave out completed requests, so this pages through all of them."""
    found, skip = [], 0
    while True:
        query = urllib.parse.urlencode(
            {"filter": "all", "mediaType": media_type, "take": PAGE_SIZE, "skip": skip}
        )
        page = services.seerr("GET", f"{API}/request?{query}")
        results = page.get("results") or []
        found.extend(
            request
            for request in results
            if (request.get("media") or {}).get("id") == media_id
            and request.get("status") != DECLINED
        )
        skip += len(results)
        if len(results) < PAGE_SIZE or skip >= (page.get("pageInfo") or {}).get(
            "results", 0
        ):
            return found


def clean_up(
    services: Services,
    request: dict,
    radarr_server_id: int,
    sonarr_server_id: int,
    added_grace: timedelta,
) -> Outcome:
    """Removes what the request brought in if it is safe to, after the request
    itself has been withdrawn. `request` is SeerrNG's view of it from before
    the withdrawal."""
    kind = request.get("type")
    media = request.get("media") or {}
    media_id, tmdb_id = media.get("id"), media.get("tmdbId")

    if kind not in ("movie", "tv"):
        return Outcome(False, "only movies and shows are cleaned up")
    if request.get("is4k"):
        return Outcome(False, "4K requests are not cleaned up")
    if not media_id or not tmdb_id:
        return Outcome(False, "the request has no media record")

    details = services.seerr("GET", f"{API}/{kind}/{tmdb_id}")
    media_info = details.get("mediaInfo") or {}
    if media_info.get("id") != media_id:
        return Outcome(False, "the media record changed")

    context = Context(
        request=request,
        media_id=media_id,
        tmdb_id=tmdb_id,
        media_info=media_info,
        requester=(request.get("requestedBy") or {}).get("id"),
        requested_at=parse_time(request["createdAt"]),
        remaining=remaining_requests(services, kind, media_id),
        added_grace=added_grace,
    )
    if kind == "movie":
        return clean_up_movie(services, context, radarr_server_id)
    return clean_up_show(services, context, sonarr_server_id)


def library_plan(
    services: Services, context: Context, service_type: str, server_id: int
) -> tuple[dict, list[dict]] | Outcome:
    """SeerrNG's deletion plan, if every copy in it is in our one server."""
    plan = services.seerr("GET", f"{API}/media/{context.media_id}/library")
    targets = plan.get("targets") or []
    if not targets:
        return Outcome(False, f"it is not in {service_type.capitalize()}")
    for target in targets:
        if (
            target.get("serviceType") != service_type
            or target.get("serviceId") != server_id
            or target.get("quality") != "HD"
        ):
            return Outcome(
                False, f"a copy is in another service ({target.get('service')})"
            )
    return plan, targets


def delete_from_library(services: Services, context: Context, plan: dict):
    services.seerr(
        "DELETE",
        f"{API}/media/{context.media_id}/library",
        {"token": plan["token"]},
    )


def clean_up_movie(
    services: Services, context: Context, radarr_server_id: int
) -> Outcome:
    if context.remaining:
        return Outcome(False, "other requests for it remain")

    planned = library_plan(services, context, "radarr", radarr_server_id)
    if isinstance(planned, Outcome):
        return planned
    plan, targets = planned

    has_file = False
    for target in targets:
        radarr = services.radarr_movie(int(target["externalId"]))
        if radarr is None or radarr.get("tmdbId") != context.tmdb_id:
            return Outcome(False, "Radarr no longer has the copy SeerrNG found")
        if parse_time(radarr["added"]) < context.earliest:
            return Outcome(False, "it was in Radarr before the request")
        has_file = has_file or bool(radarr.get("hasFile"))

    if has_file or context.media_info.get("status") in (
        PARTIALLY_AVAILABLE,
        AVAILABLE,
    ):
        reason = others_have_played_movie(services, context)
        if reason:
            return Outcome(False, reason)

    delete_from_library(services, context, plan)
    return Outcome(True, "deleted from Radarr with its files")


def other_jellyfin_users(services: Services, context: Context):
    """Jellyfin users other than the requester, or None without a key."""
    users = services.jellyfin_users()
    if users is None:
        return None
    if context.requester is None:
        return users
    owner = services.seerr("GET", f"{API}/user/{context.requester}")
    requester = normalize_id(owner.get("jellyfinUserId"))
    return [user for user in users if normalize_id(user.get("Id")) != requester]


def others_have_played_movie(services: Services, context: Context) -> str | None:
    """Why the movie must be kept for someone else, or None if nobody else
    has played it."""
    item_id = context.media_info.get("jellyfinMediaId")
    if not item_id:
        return "it has a file that SeerrNG has not matched in Jellyfin yet"
    users = other_jellyfin_users(services, context)
    if users is None:
        return "there is no Jellyfin API key to check who has watched it"
    for user in users:
        user_data = services.jellyfin_user_data(user["Id"], item_id)
        if user_data is None:
            return "Jellyfin does not have the item SeerrNG matched"
        if played(user_data):
            return "another Jellyfin user has watched it"
    return None


Episode = tuple[int, int]


def requested_episodes(request: dict, episodes: Iterable[Episode]) -> set[Episode]:
    """The episodes a request covers. A season without episode numbers is
    the whole season."""
    covered = set()
    for season in request.get("seasons") or []:
        number = season.get("seasonNumber")
        chosen = season.get("episodeNumbers")
        covered.update(
            (s, e)
            for s, e in episodes
            if s == number and (chosen is None or e in chosen)
        )
    return covered


def has_season_mappings(media: dict) -> bool:
    return any(
        identifier.get("seasonMappings") != []
        for identifier in media.get("identifiers") or []
    )


def clean_up_show(
    services: Services, context: Context, sonarr_server_id: int
) -> Outcome:
    # Episodes it never sent to Sonarr may be monitored for someone else.
    if context.request.get("status") not in (APPROVED, COMPLETED):
        return Outcome(False, "the request was never sent to Sonarr")
    if has_season_mappings(context.request.get("media") or {}):
        return Outcome(False, "SeerrNG maps its season numbers onto Sonarr's")

    planned = library_plan(services, context, "sonarr", sonarr_server_id)
    if isinstance(planned, Outcome):
        return planned
    plan, targets = planned
    if len(targets) != 1:
        return Outcome(False, "it is in Sonarr more than once")

    series = services.sonarr_series(int(targets[0]["externalId"]))
    tvdb_id = context.request.get("overrideTvdbId") or (
        context.request.get("media") or {}
    ).get("tvdbId")
    if series is None or (tvdb_id and series.get("tvdbId") != tvdb_id):
        return Outcome(False, "Sonarr no longer has the show SeerrNG found")

    episodes = services.sonarr_episodes(series["id"])
    files = {f["id"]: f for f in services.sonarr_episode_files(series["id"])}
    by_number = {(e["seasonNumber"], e["episodeNumber"]): e for e in episodes}

    wanted = set()
    for other in context.remaining:
        wanted |= requested_episodes(other, by_number)
    withdrawn = requested_episodes(context.request, by_number) - wanted

    def file_of(episode: dict) -> dict | None:
        return (
            files.get(episode.get("episodeFileId")) if episode.get("hasFile") else None
        )

    # An episode whose file predates the request wasn't brought in by it.
    candidates = {
        number
        for number in withdrawn
        if not (file := file_of(by_number[number]))
        or parse_time(file["dateAdded"]) >= context.earliest
    }

    kept_seasons, kept_reason = seasons_others_need(services, context, episodes, files)
    candidates = {(s, e) for s, e in candidates if s not in kept_seasons}

    if (
        not context.remaining
        and not kept_seasons
        and parse_time(series["added"]) >= context.earliest
    ):
        delete_from_library(services, context, plan)
        return Outcome(True, "deleted the show from Sonarr with its files")

    if not candidates:
        reason = kept_reason or "other requests cover all of its episodes"
        return Outcome(False, reason)

    return trim_show(
        services, series, episodes, files, by_number, candidates, kept_reason
    )


def seasons_others_need(
    services: Services,
    context: Context,
    episodes: list[dict],
    files: dict[int, dict],
) -> tuple[set[int], str | None]:
    """Seasons to keep for someone other than the requester, and why."""
    with_files = {e["seasonNumber"] for e in episodes if e.get("hasFile")}
    if not with_files:
        return set(), None
    item_id = context.media_info.get("jellyfinMediaId")
    users = other_jellyfin_users(services, context) if item_id else None
    if users is None:
        why = (
            "SeerrNG has not matched it in Jellyfin yet"
            if not item_id
            else "there is no Jellyfin API key to check who has watched it"
        )
        return with_files, f"kept seasons with files: {why}"
    watched = set()
    for user in users:
        user_episodes = services.jellyfin_episodes(user["Id"], item_id)
        if user_episodes is None:
            return (
                with_files,
                "kept seasons with files: Jellyfin does not have the show SeerrNG matched",
            )
        watched.update(
            episode.get("ParentIndexNumber")
            for episode in user_episodes
            if played(episode.get("UserData") or {})
        )
    watched.discard(None)
    if watched:
        seasons = ", ".join(str(s) for s in sorted(watched))
        return watched, f"kept seasons another Jellyfin user has watched ({seasons})"
    return set(), None


def trim_show(
    services: Services,
    series: dict,
    episodes: list[dict],
    files: dict[int, dict],
    by_number: dict[Episode, dict],
    candidates: set[Episode],
    kept_reason: str | None,
) -> Outcome:
    """Unmonitors the candidate episodes and deletes their files."""
    episode_ids = sorted(by_number[n]["id"] for n in candidates)
    # A file can hold several episodes; it goes only if all of them do.
    episodes_in_file: dict[int, set[Episode]] = {}
    for episode in episodes:
        if episode.get("hasFile") and episode.get("episodeFileId") in files:
            episodes_in_file.setdefault(episode["episodeFileId"], set()).add(
                (episode["seasonNumber"], episode["episodeNumber"])
            )
    file_ids = sorted(
        file_id
        for file_id, numbers in episodes_in_file.items()
        if numbers <= candidates
    )

    # Unmonitor first, so Sonarr doesn't search again for what it deletes.
    services.sonarr_unmonitor_episodes(episode_ids)
    if file_ids:
        services.sonarr_delete_episode_files(file_ids)

    monitored_after = {
        e["seasonNumber"]
        for e in episodes
        if e.get("monitored")
        and (e["seasonNumber"], e["episodeNumber"]) not in candidates
    }
    touched = {s for s, _ in candidates}
    emptied = [
        season
        for season in series.get("seasons") or []
        if season.get("seasonNumber") in touched
        and season.get("monitored")
        and season["seasonNumber"] not in monitored_after
    ]
    if emptied:
        for season in emptied:
            season["monitored"] = False
        services.sonarr_update_series(series)

    seasons = ", ".join(str(s) for s in sorted(touched))
    summary = (
        f"unmonitored {len(episode_ids)} episodes in season(s) {seasons} "
        f"and deleted {len(file_ids)} files"
    )
    if kept_reason:
        summary += f"; {kept_reason}"
    return Outcome(True, summary, partial=True)


class HttpServices:
    """SeerrNG as the owner, Radarr, Sonarr, and optionally Jellyfin."""

    def __init__(
        self,
        seerr: urllib.parse.SplitResult,
        seerr_key: str,
        radarr: urllib.parse.SplitResult | None,
        radarr_key: str | None,
        sonarr: urllib.parse.SplitResult | None,
        sonarr_key: str | None,
        jellyfin: urllib.parse.SplitResult | None,
        jellyfin_key: str | None,
    ):
        self._seerr = (seerr, {"X-Api-Key": seerr_key})
        self._radarr = (radarr, {"X-Api-Key": radarr_key}) if radarr else None
        self._sonarr = (sonarr, {"X-Api-Key": sonarr_key}) if sonarr else None
        self._jellyfin = (
            (jellyfin, {"Authorization": f'MediaBrowser Token="{jellyfin_key}"'})
            if jellyfin and jellyfin_key
            else None
        )

    @staticmethod
    def _call(service, method: str, path: str, body=None):
        if service is None:
            raise Unavailable(f"no service configured for {method} {path}")
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

    def _sonarr(self, method, path, body=None, expected=(200,)):
        status, data = self._call(self._sonarr, method, path, body)
        if status not in expected:
            raise Unavailable(f"Sonarr {method} {path} returned {status}")
        return data

    def sonarr_series(self, series_id):
        status, data = self._call(self._sonarr, "GET", f"/api/v3/series/{series_id}")
        if status == 404:
            return None
        if status != 200:
            raise Unavailable(f"Sonarr GET series {series_id} returned {status}")
        return data

    def sonarr_episodes(self, series_id):
        return self._sonarr("GET", f"/api/v3/episode?seriesId={series_id}")

    def sonarr_episode_files(self, series_id):
        return self._sonarr("GET", f"/api/v3/episodefile?seriesId={series_id}")

    def sonarr_unmonitor_episodes(self, episode_ids):
        self._sonarr(
            "PUT",
            "/api/v3/episode/monitor",
            {"episodeIds": episode_ids, "monitored": False},
            expected=(200, 202),
        )

    def sonarr_delete_episode_files(self, file_ids):
        self._sonarr(
            "DELETE",
            "/api/v3/episodefile/bulk",
            {"episodeFileIds": file_ids},
            expected=(200, 204),
        )

    def sonarr_update_series(self, series):
        self._sonarr(
            "PUT", f"/api/v3/series/{series['id']}", series, expected=(200, 202)
        )

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

    def jellyfin_episodes(self, user_id, series_id):
        query = urllib.parse.urlencode({"userId": user_id})
        status, data = self._call(
            self._jellyfin,
            "GET",
            f"/Shows/{urllib.parse.quote(series_id)}/Episodes?{query}",
        )
        if status in (400, 404):
            return None
        if status != 200:
            raise Unavailable(f"Jellyfin GET episodes returned {status}")
        return data.get("Items") or []


def run(
    services: Services,
    request: dict,
    radarr_server_id: int,
    sonarr_server_id: int,
    added_grace: timedelta,
    log: Callable[[str], None],
) -> Outcome:
    """clean_up, logging what happened and turning failures into kept media."""
    label = f"request {request.get('id')} ({request.get('type')}, tmdb {(request.get('media') or {}).get('tmdbId')})"
    try:
        outcome = clean_up(
            services, request, radarr_server_id, sonarr_server_id, added_grace
        )
    except Exception as error:  # noqa: BLE001 -- the media is kept on any failure
        outcome = Outcome(False, f"could not verify: {error}")
    verb = "trimmed" if outcome.partial else "removed" if outcome.deleted else "kept"
    log(f"withdrawn {label}: {verb}, {outcome.reason}")
    return outcome
