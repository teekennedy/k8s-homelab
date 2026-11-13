"""When withdrawn media is removed from Radarr or Sonarr, and when it is kept."""

from __future__ import annotations

import copy
import urllib.parse
from datetime import timedelta

import pytest

import withdraw

REQUESTER = 7
OTHER = 8
MEDIA_ID = 40
TMDB = 603
TVDB = 7300
RADARR_ID = 12
SERIES_ID = 63
ITEM = "aa11"
REQUESTED_AT = "2026-11-13T06:00:00.000Z"
AFTER = "2026-11-13T06:00:05Z"
BEFORE = "2026-11-01T00:00:00Z"
GRACE = timedelta(minutes=1)
TOKEN = "t" * 64


def request(**changes) -> dict:
    value = {
        "id": 500,
        "type": "movie",
        "status": withdraw.COMPLETED,
        "is4k": False,
        "createdAt": REQUESTED_AT,
        "requestedBy": {"id": REQUESTER},
        "media": {"id": MEDIA_ID, "tmdbId": TMDB, "tvdbId": TVDB, "identifiers": []},
    }
    value.update(changes)
    return value


def show_request(seasons, **changes) -> dict:
    """A TV request for {season: episode numbers, or None for all of it}."""
    return request(
        type="tv",
        seasons=[{"seasonNumber": s, "episodeNumbers": e} for s, e in seasons.items()],
        **changes,
    )


def other_request(seasons=None, **changes) -> dict:
    """Someone else's request for the same media."""
    value = (
        show_request(seasons, id=501, requestedBy={"id": OTHER})
        if seasons is not None
        else request(id=501, requestedBy={"id": OTHER})
    )
    value.update(changes)
    return value


class FakeServices:
    """SeerrNG, Radarr, Sonarr and Jellyfin as they stand after the withdrawal."""

    def __init__(self, kind="movie"):
        self.kind = kind
        self.media_info = {"id": MEDIA_ID, "status": 3}
        self.requests: list[dict] = []
        service = "radarr" if kind == "movie" else "sonarr"
        self.targets = [
            {
                "serviceType": service,
                "serviceId": 0,
                "service": service.capitalize(),
                "externalId": RADARR_ID if kind == "movie" else SERIES_ID,
                "quality": "HD",
            }
        ]
        self.radarr = {RADARR_ID: {"tmdbId": TMDB, "added": AFTER, "hasFile": False}}
        self.series = {
            "id": SERIES_ID,
            "tvdbId": TVDB,
            "added": AFTER,
            "seasons": [{"seasonNumber": s, "monitored": True} for s in (1, 2, 3)],
        }
        # Three seasons of three episodes, all monitored, nothing downloaded.
        self.episodes = [
            {
                "id": s * 100 + e,
                "seasonNumber": s,
                "episodeNumber": e,
                "monitored": True,
                "hasFile": False,
                "episodeFileId": 0,
            }
            for s in (1, 2, 3)
            for e in (1, 2, 3)
        ]
        self.files: list[dict] = []
        self.jellyfin = [{"Id": "requester-jf"}, {"Id": "other-jf"}]
        self.user_data: dict[str, dict | None] = {}
        self.watched: dict[str, list[tuple[int, int]] | None] = {}
        self.deleted: list[tuple[str, dict]] = []
        self.unmonitored: list[int] = []
        self.deleted_files: list[int] = []
        self.updated_series: list[dict] = []

    def seerr(self, method, path, body=None):
        split = urllib.parse.urlsplit(path)
        if method == "GET" and path == f"/api/v1/{self.kind}/{TMDB}":
            return {"mediaInfo": self.media_info}
        if method == "GET" and split.path == "/api/v1/request":
            query = dict(urllib.parse.parse_qsl(split.query))
            assert query["filter"] == "all" and query["mediaType"] == self.kind
            skip, take = int(query["skip"]), int(query["take"])
            return {
                "pageInfo": {"results": len(self.requests)},
                "results": self.requests[skip : skip + take],
            }
        if method == "GET" and path == f"/api/v1/media/{MEDIA_ID}/library":
            return {"targets": self.targets, "token": TOKEN}
        if method == "GET" and path == f"/api/v1/user/{REQUESTER}":
            return {"id": REQUESTER, "jellyfinUserId": "requester-jf"}
        if method == "DELETE" and path == f"/api/v1/media/{MEDIA_ID}/library":
            self.deleted.append((path, body))
            return None
        raise AssertionError(f"unexpected SeerrNG call {method} {path}")

    def radarr_movie(self, movie_id):
        return self.radarr.get(movie_id)

    def sonarr_series(self, series_id):
        return copy.deepcopy(self.series) if series_id == SERIES_ID else None

    def sonarr_episodes(self, series_id):
        assert series_id == SERIES_ID
        return copy.deepcopy(self.episodes)

    def sonarr_episode_files(self, series_id):
        assert series_id == SERIES_ID
        return copy.deepcopy(self.files)

    def sonarr_unmonitor_episodes(self, episode_ids):
        self.unmonitored.extend(episode_ids)

    def sonarr_delete_episode_files(self, file_ids):
        self.deleted_files.extend(file_ids)

    def sonarr_update_series(self, series):
        self.updated_series.append(series)

    def jellyfin_users(self):
        return self.jellyfin

    def jellyfin_user_data(self, user_id, item_id):
        assert item_id == ITEM
        return self.user_data.get(user_id, {"Played": False, "PlayCount": 0})

    def jellyfin_episodes(self, user_id, series_id):
        assert series_id == ITEM
        watched = self.watched.get(user_id, [])
        if watched is None:
            return None
        return [
            {
                "ParentIndexNumber": s,
                "IndexNumber": e,
                "UserData": {"Played": (s, e) in watched},
            }
            for s in (1, 2, 3)
            for e in (1, 2, 3)
        ]

    def with_file(self):
        self.radarr[RADARR_ID]["hasFile"] = True
        self.media_info.update(status=withdraw.AVAILABLE, jellyfinMediaId=ITEM)
        return self

    def download(self, *episodes, added=AFTER, file_id=None):
        """Gives the episodes one file, added at `added`."""
        file_id = file_id or 1000 + len(self.files)
        self.files.append({"id": file_id, "dateAdded": added})
        for episode in self.episodes:
            if (episode["seasonNumber"], episode["episodeNumber"]) in episodes:
                episode.update(hasFile=True, episodeFileId=file_id)
        self.media_info.update(
            status=withdraw.PARTIALLY_AVAILABLE, jellyfinMediaId=ITEM
        )
        return file_id


def clean_up(services, req=None):
    return withdraw.clean_up(services, req or request(), 0, 0, GRACE)


def assert_kept(services, outcome, reason):
    assert not outcome.deleted
    assert reason in outcome.reason
    assert services.deleted == []
    assert services.unmonitored == []
    assert services.deleted_files == []


def episode_ids(*episodes):
    return sorted(s * 100 + e for s, e in episodes)


def season(s):
    return [(s, e) for e in (1, 2, 3)]


# Movies


def test_a_movie_only_its_requester_wanted_is_deleted():
    services = FakeServices()
    outcome = clean_up(services)
    assert outcome.deleted
    assert services.deleted == [(f"/api/v1/media/{MEDIA_ID}/library", {"token": TOKEN})]


@pytest.mark.parametrize(
    "req, reason",
    [
        (request(type="music"), "only movies and shows"),
        (request(is4k=True), "4K"),
        (request(media=None), "no media record"),
    ],
)
def test_out_of_scope_withdrawals_are_left_alone(req, reason):
    services = FakeServices()
    assert_kept(services, clean_up(services, req), reason)


@pytest.mark.parametrize("status", [1, withdraw.APPROVED, 4, withdraw.COMPLETED])
def test_kept_while_another_request_remains(status):
    services = FakeServices()
    services.requests = [other_request(status=status)]
    assert_kept(services, clean_up(services), "other requests")


def test_a_declined_request_does_not_keep_it():
    services = FakeServices()
    services.requests = [other_request(status=withdraw.DECLINED)]
    assert clean_up(services).deleted


def test_requests_for_other_media_do_not_count():
    services = FakeServices()
    services.requests = [other_request(media={"id": MEDIA_ID + 1})]
    assert clean_up(services).deleted


def test_remaining_requests_are_found_on_later_pages():
    services = FakeServices()
    services.requests = [
        other_request(id=1000 + n, media={"id": 1000 + n})
        for n in range(withdraw.PAGE_SIZE)
    ] + [other_request()]
    assert_kept(services, clean_up(services), "other requests")


def test_kept_when_the_media_record_was_replaced():
    services = FakeServices()
    services.media_info["id"] = MEDIA_ID + 1
    assert_kept(services, clean_up(services), "media record changed")


def test_nothing_to_do_when_radarr_does_not_have_it():
    services = FakeServices()
    services.targets = []
    assert_kept(services, clean_up(services), "not in Radarr")


@pytest.mark.parametrize(
    "change",
    [{"serviceType": "sonarr"}, {"serviceId": 1}, {"quality": "4K"}],
)
def test_kept_when_a_copy_is_somewhere_else(change):
    services = FakeServices()
    services.targets[0].update(change)
    assert_kept(services, clean_up(services), "another service")


def test_kept_when_radarr_lost_the_copy():
    services = FakeServices()
    services.radarr = {}
    assert_kept(services, clean_up(services), "no longer has")


def test_kept_when_radarr_had_it_before_the_request():
    services = FakeServices()
    services.radarr[RADARR_ID]["added"] = BEFORE
    assert_kept(services, clean_up(services), "before the request")


def test_radarr_clock_skew_within_the_grace_is_tolerated():
    services = FakeServices()
    services.radarr[RADARR_ID]["added"] = "2026-11-13T05:59:30Z"
    assert clean_up(services).deleted


def test_a_file_nobody_else_has_played_is_deleted():
    services = FakeServices().with_file()
    services.user_data["requester-jf"] = {"Played": True, "PlayCount": 1}
    assert clean_up(services).deleted


@pytest.mark.parametrize(
    "user_data", [{"Played": True}, {"PlayCount": 1}, {"PlaybackPositionTicks": 1}]
)
def test_kept_when_another_user_has_watched_any_of_it(user_data):
    services = FakeServices().with_file()
    services.user_data["other-jf"] = user_data
    assert_kept(services, clean_up(services), "another Jellyfin user")


def test_requester_is_matched_regardless_of_id_format():
    services = FakeServices().with_file()
    services.jellyfin = [{"Id": "AA-BB"}]
    services.user_data["AA-BB"] = {"Played": True}

    def seerr(method, path, body=None, original=services.seerr):
        if path == f"/api/v1/user/{REQUESTER}":
            return {"id": REQUESTER, "jellyfinUserId": "aabb"}
        return original(method, path, body)

    services.seerr = seerr
    assert clean_up(services).deleted


def test_kept_without_a_jellyfin_key_when_there_is_a_file():
    services = FakeServices().with_file()
    services.jellyfin = None
    assert_kept(services, clean_up(services), "no Jellyfin API key")


def test_no_jellyfin_key_is_needed_before_anything_was_downloaded():
    services = FakeServices()
    services.jellyfin = None
    assert clean_up(services).deleted


def test_kept_when_a_file_is_not_matched_in_jellyfin_yet():
    services = FakeServices()
    services.radarr[RADARR_ID]["hasFile"] = True
    assert_kept(services, clean_up(services), "not matched in Jellyfin")


def test_kept_when_jellyfin_does_not_have_the_matched_item():
    services = FakeServices().with_file()
    services.user_data["other-jf"] = None
    assert_kept(services, clean_up(services), "Jellyfin does not have")


# Shows


def test_a_show_only_its_requester_wanted_is_deleted():
    services = FakeServices("tv")
    services.download(*season(1))
    outcome = clean_up(services, show_request({1: None}))
    assert outcome.deleted and not outcome.partial
    assert services.deleted == [(f"/api/v1/media/{MEDIA_ID}/library", {"token": TOKEN})]
    assert services.unmonitored == []


def test_unrequested_episodes_are_trimmed_when_another_request_remains():
    services = FakeServices("tv")
    kept_file = services.download(*season(1))
    gone_file = services.download(*season(2))
    services.requests = [other_request({1: None})]
    outcome = clean_up(services, show_request({1: None, 2: None}))
    assert outcome.deleted and outcome.partial
    assert services.deleted == []
    assert services.unmonitored == episode_ids(*season(2))
    assert services.deleted_files == [gone_file]
    assert kept_file not in services.deleted_files
    [series] = services.updated_series
    assert [s["monitored"] for s in series["seasons"]] == [True, False, True]


def test_single_episodes_are_trimmed_from_a_season_someone_else_wants_part_of():
    services = FakeServices("tv")
    services.requests = [other_request({2: [1]})]
    outcome = clean_up(services, show_request({2: [1, 2]}))
    assert outcome.partial
    assert services.unmonitored == episode_ids((2, 2))
    # Episodes 1 and 3 are still monitored, so the season stays monitored.
    assert services.updated_series == []


def test_a_multi_episode_file_goes_only_with_all_its_episodes():
    services = FakeServices("tv")
    shared = services.download((2, 1), (2, 2))
    services.requests = [other_request({2: [1]})]
    clean_up(services, show_request({2: None}))
    assert services.unmonitored == episode_ids((2, 2), (2, 3))
    assert shared not in services.deleted_files


def test_files_older_than_the_request_are_kept_and_stay_monitored():
    services = FakeServices("tv")
    services.series["added"] = BEFORE
    old = services.download((2, 1), added=BEFORE)
    new = services.download((2, 2))
    outcome = clean_up(services, show_request({2: None}))
    assert outcome.partial
    assert services.unmonitored == episode_ids((2, 2), (2, 3))
    assert services.deleted_files == [new]
    assert old not in services.deleted_files


def test_a_show_in_sonarr_before_the_request_is_trimmed_not_deleted():
    services = FakeServices("tv")
    services.series["added"] = BEFORE
    outcome = clean_up(services, show_request({3: None}))
    assert outcome.partial
    assert services.deleted == []
    assert services.unmonitored == episode_ids(*season(3))


def test_kept_when_other_requests_cover_every_episode():
    services = FakeServices("tv")
    services.requests = [other_request({1: None, 2: None})]
    assert_kept(services, clean_up(services, show_request({2: [1, 2]})), "cover all")


def test_seasons_another_user_watched_are_kept():
    services = FakeServices("tv")
    services.download(*season(1))
    services.download(*season(2))
    services.watched["other-jf"] = [(1, 2)]
    outcome = clean_up(services, show_request({1: None, 2: None}))
    assert outcome.partial
    assert services.deleted == []
    assert services.unmonitored == episode_ids(*season(2))
    assert "another Jellyfin user has watched (1)" in outcome.reason


def test_the_requesters_own_viewing_does_not_keep_a_show():
    services = FakeServices("tv")
    services.download(*season(1))
    services.watched["requester-jf"] = season(1)
    assert clean_up(services, show_request({1: None})).deleted


def test_without_a_jellyfin_key_seasons_with_files_are_kept():
    services = FakeServices("tv")
    services.download((1, 1))
    services.jellyfin = None
    outcome = clean_up(services, show_request({1: None, 2: None}))
    assert outcome.partial
    assert services.deleted == []
    assert services.unmonitored == episode_ids(*season(2))
    assert "no Jellyfin API key" in outcome.reason


def test_a_show_with_nothing_downloaded_needs_no_jellyfin_key():
    services = FakeServices("tv")
    services.jellyfin = None
    assert clean_up(services, show_request({1: None})).deleted


def test_kept_when_jellyfin_does_not_have_the_matched_show():
    services = FakeServices("tv")
    services.download(*season(1))
    services.watched["other-jf"] = None
    outcome = clean_up(services, show_request({1: None}))
    assert_kept(services, outcome, "Jellyfin does not have the show")


@pytest.mark.parametrize("status", [1, withdraw.DECLINED, 4])
def test_requests_that_never_reached_sonarr_are_left_alone(status):
    services = FakeServices("tv")
    outcome = clean_up(services, show_request({1: None}, status=status))
    assert_kept(services, outcome, "never sent to Sonarr")


def test_kept_when_seerr_renumbers_seasons_for_sonarr():
    services = FakeServices("tv")
    req = show_request({1: None})
    req["media"]["identifiers"] = [
        {"provider": "tvdb", "seasonMappings": [{"seasonNumber": 1}]}
    ]
    assert_kept(services, clean_up(services, req), "maps its season numbers")


def test_kept_when_sonarr_has_a_different_show():
    services = FakeServices("tv")
    services.series["tvdbId"] = TVDB + 1
    assert_kept(services, clean_up(services, show_request({1: None})), "no longer has")


def test_kept_when_the_show_is_in_another_service():
    services = FakeServices("tv")
    services.targets[0]["quality"] = "4K"
    assert_kept(
        services, clean_up(services, show_request({1: None})), "another service"
    )


# run


def test_run_keeps_the_media_when_a_service_fails():
    services = FakeServices()

    def radarr_movie(movie_id):
        raise withdraw.Unavailable("Radarr is down")

    services.radarr_movie = radarr_movie
    lines = []
    outcome = withdraw.run(services, request(), 0, 0, GRACE, lines.append)
    assert not outcome.deleted
    assert services.deleted == []
    assert lines == [
        f"withdrawn request 500 (movie, tmdb {TMDB}): kept, could not verify: Radarr is down"
    ]


def test_run_logs_a_deletion():
    lines = []
    withdraw.run(FakeServices(), request(), 0, 0, GRACE, lines.append)
    assert lines == [
        f"withdrawn request 500 (movie, tmdb {TMDB}): removed, deleted from Radarr with its files"
    ]


def test_run_logs_a_trim():
    services = FakeServices("tv")
    services.requests = [other_request({1: None})]
    lines = []
    withdraw.run(services, show_request({1: None, 2: None}), 0, 0, GRACE, lines.append)
    assert lines == [
        f"withdrawn request 500 (tv, tmdb {TMDB}): trimmed, unmonitored 3 episodes"
        " in season(s) 2 and deleted 0 files"
    ]
