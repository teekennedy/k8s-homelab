"""When a withdrawn movie is removed from Radarr, and when it is kept."""

from __future__ import annotations

from datetime import timedelta

import pytest

import withdraw

REQUESTER = 7
OTHER = 8
MEDIA_ID = 40
TMDB = 603
RADARR_ID = 12
ITEM = "aa11"
REQUESTED_AT = "2026-11-13T06:00:00.000Z"
GRACE = timedelta(minutes=1)


def request(**changes) -> dict:
    value = {
        "id": 500,
        "type": "movie",
        "is4k": False,
        "createdAt": REQUESTED_AT,
        "requestedBy": {"id": REQUESTER},
        "media": {"id": MEDIA_ID, "tmdbId": TMDB},
    }
    value.update(changes)
    return value


class FakeServices:
    """SeerrNG, Radarr and Jellyfin as they stand after the withdrawal."""

    def __init__(self):
        self.media_info = {"id": MEDIA_ID, "status": 3, "requests": []}
        self.targets = [
            {
                "serviceType": "radarr",
                "serviceId": 0,
                "service": "Radarr",
                "externalId": RADARR_ID,
                "quality": "HD",
            }
        ]
        self.radarr = {
            RADARR_ID: {
                "tmdbId": TMDB,
                "added": "2026-11-13T06:00:05Z",
                "hasFile": False,
            }
        }
        self.jellyfin = [
            {"Id": "requester-jf"},
            {"Id": "other-jf"},
        ]
        self.user_data: dict[str, dict | None] = {}
        self.deleted: list[tuple[str, dict]] = []

    def seerr(self, method, path, body=None):
        if method == "GET" and path == f"/api/v1/movie/{TMDB}":
            return {"mediaInfo": self.media_info}
        if method == "GET" and path == f"/api/v1/media/{MEDIA_ID}/library":
            return {"targets": self.targets, "token": "t" * 64}
        if method == "GET" and path == f"/api/v1/user/{REQUESTER}":
            return {"id": REQUESTER, "jellyfinUserId": "requester-jf"}
        if method == "DELETE" and path == f"/api/v1/media/{MEDIA_ID}/library":
            self.deleted.append((path, body))
            return None
        raise AssertionError(f"unexpected SeerrNG call {method} {path}")

    def radarr_movie(self, movie_id):
        return self.radarr.get(movie_id)

    def jellyfin_users(self):
        return self.jellyfin

    def jellyfin_user_data(self, user_id, item_id):
        assert item_id == ITEM
        return self.user_data.get(user_id, {"Played": False, "PlayCount": 0})

    def with_file(self):
        self.radarr[RADARR_ID]["hasFile"] = True
        self.media_info.update(status=withdraw.AVAILABLE, jellyfinMediaId=ITEM)
        return self


def clean_up(services, req=None, actor=REQUESTER):
    return withdraw.clean_up(services, req or request(), actor, 0, GRACE)


def assert_kept(services, outcome, reason):
    assert not outcome.deleted
    assert reason in outcome.reason
    assert services.deleted == []


def test_a_movie_only_its_requester_wanted_is_deleted():
    services = FakeServices()
    outcome = clean_up(services)
    assert outcome.deleted
    assert services.deleted == [
        (f"/api/v1/media/{MEDIA_ID}/library", {"token": "t" * 64})
    ]


@pytest.mark.parametrize(
    "req, actor, reason",
    [
        (request(type="tv"), REQUESTER, "only movies"),
        (request(is4k=True), REQUESTER, "4K"),
        (request(), OTHER, "someone other than the requester"),
        (request(), None, "someone other than the requester"),
        (request(media=None), REQUESTER, "no media record"),
    ],
)
def test_out_of_scope_withdrawals_are_left_alone(req, actor, reason):
    services = FakeServices()
    assert_kept(services, clean_up(services, req, actor), reason)


def test_kept_while_another_request_remains():
    services = FakeServices()
    services.media_info["requests"] = [{"id": 501, "requestedBy": {"id": OTHER}}]
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
    [
        {"serviceType": "sonarr"},
        {"serviceId": 1},
        {"quality": "4K"},
    ],
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
    services.radarr[RADARR_ID]["added"] = "2026-11-01T00:00:00Z"
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
    "user_data",
    [
        {"Played": True},
        {"PlayCount": 1},
        {"PlaybackPositionTicks": 1},
    ],
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


def test_run_keeps_the_movie_when_a_service_fails():
    services = FakeServices()

    def radarr_movie(movie_id):
        raise withdraw.Unavailable("Radarr is down")

    services.radarr_movie = radarr_movie
    lines = []
    outcome = withdraw.run(services, request(), REQUESTER, 0, GRACE, lines.append)
    assert not outcome.deleted
    assert services.deleted == []
    assert lines == [
        f"withdrawn request 500 (tmdb {TMDB}): kept, could not verify: Radarr is down"
    ]


def test_run_logs_a_deletion():
    lines = []
    withdraw.run(FakeServices(), request(), REQUESTER, 0, GRACE, lines.append)
    assert lines == [
        f"withdrawn request 500 (tmdb {TMDB}): removed, deleted from Radarr with its files"
    ]
