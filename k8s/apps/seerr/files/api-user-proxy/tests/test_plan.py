"""What a request made on a user's behalf turns into."""

from __future__ import annotations

import json

import pytest

import proxy
from proxy import Deny, Forward, plan

USER = 7
REQUESTER = proxy.REQUEST
MOVIES_ONLY = proxy.REQUEST_MOVIE
MANAGER = proxy.MANAGE_REQUESTS
ADMIN = proxy.ADMIN


def post_request(permissions: int, **body) -> Forward | Deny:
    return plan("POST", "/api/v1/request", json.dumps(body).encode(), USER, permissions)


def test_request_is_made_on_the_users_behalf():
    decision = post_request(REQUESTER, mediaType="movie", mediaId=603)
    assert isinstance(decision, Forward)
    assert json.loads(decision.body) == {
        "mediaType": "movie",
        "mediaId": 603,
        "userId": USER,
    }


def test_request_overrides_a_user_id_in_the_body():
    decision = post_request(REQUESTER, mediaType="tv", mediaId=1, userId=1)
    assert json.loads(decision.body)["userId"] == USER


@pytest.mark.parametrize(
    "permissions, media_type, is_4k, allowed",
    [
        (REQUESTER, "movie", False, True),
        (REQUESTER, "tv", False, True),
        (MOVIES_ONLY, "movie", False, True),
        # SeerrNG skips this check for on-behalf requests, so the proxy is the
        # only thing stopping these.
        (MOVIES_ONLY, "tv", False, False),
        (REQUESTER, "movie", True, False),
        (proxy.REQUEST_4K_TV, "tv", True, True),
        (0, "movie", False, False),
        (ADMIN, "tv", True, True),
        (REQUESTER, "adult", False, False),
    ],
)
def test_request_needs_the_users_own_permission(
    permissions, media_type, is_4k, allowed
):
    decision = post_request(permissions, mediaType=media_type, mediaId=1, is4k=is_4k)
    if allowed:
        assert isinstance(decision, Forward)
    else:
        assert decision == Deny(403, "You do not have permission to make this request.")


@pytest.mark.parametrize("body", [b"not json", b"[]", b'{"mediaType": "game"}'])
def test_malformed_requests_are_refused(body):
    decision = plan("POST", "/api/v1/request", body, USER, ADMIN)
    assert isinstance(decision, Deny)
    assert decision.status == 400


@pytest.mark.parametrize("action", ["approve", "decline"])
def test_approving_needs_manage_requests(action):
    target = f"/api/v1/request/42/{action}"
    assert plan("POST", target, b"", USER, MANAGER) == Forward(target, b"")
    assert isinstance(plan("POST", target, b"", USER, REQUESTER), Deny)


@pytest.mark.parametrize(
    "method, target",
    [
        ("POST", "/api/v1/issue"),
        ("POST", "/api/v1/watchlist"),
        ("DELETE", "/api/v1/request/42"),
        ("PUT", "/api/v1/user/7/settings/main"),
        ("POST", "/api/v1/request/42/retry"),
    ],
)
def test_other_writes_are_refused_rather_than_made_as_the_owner(method, target):
    decision = plan(method, target, b"{}", USER, ADMIN)
    assert decision == Deny(403, "This proxy only makes requests on a user's behalf.")


def test_request_list_is_limited_to_the_users_own():
    decision = plan(
        "GET", "/api/v1/request?take=10&requestedBy=1", b"", USER, REQUESTER
    )
    assert decision == Forward("/api/v1/request?take=10&requestedBy=7", b"")


@pytest.mark.parametrize("permissions", [MANAGER, proxy.REQUEST_VIEW, ADMIN])
def test_request_list_is_whole_for_users_who_may_see_it(permissions):
    target = "/api/v1/request?take=10"
    assert plan("GET", target, b"", USER, permissions) == Forward(target, b"")


def test_watchlist_is_the_users_own():
    decision = plan("GET", "/api/v1/discover/watchlist?page=2", b"", USER, REQUESTER)
    assert decision == Forward("/api/v1/user/7/watchlist?page=2", b"")


def test_other_reads_pass_through():
    target = "/api/v1/movie/603/similar?page=1"
    assert plan("GET", target, b"", USER, 0) == Forward(target, b"")
