"""Users as the Jellyfin Enhanced plugin can parse them."""

from __future__ import annotations

import json

import pytest

import proxy
from proxy import clamp_permissions

# Every bit SeerrNG uses past what a signed 32-bit int holds.
HIGH = (
    1 << 31
    | proxy.REQUEST_MUSIC
    | proxy.REQUEST_BOOK
    | proxy.REQUEST_COMIC
    | proxy.REQUEST_MAGAZINE
    | proxy.REQUEST_SPORTS
    | proxy.REQUEST_ADULT
)
LOW = proxy.REQUEST | proxy.REQUEST_MOVIE | proxy.MANAGE_REQUESTS


def clamp(document) -> object:
    return json.loads(clamp_permissions(json.dumps(document).encode()))


def test_high_bits_are_dropped_from_a_page_of_users():
    page = {
        "pageInfo": {"pages": 1, "results": 2},
        "results": [
            {"id": 2, "displayName": "a", "permissions": LOW | HIGH},
            {"id": 3, "displayName": "b", "permissions": proxy.ADMIN},
        ],
    }
    assert clamp(page) == {
        "pageInfo": {"pages": 1, "results": 2},
        "results": [
            {"id": 2, "displayName": "a", "permissions": LOW},
            {"id": 3, "displayName": "b", "permissions": proxy.ADMIN},
        ],
    }


def test_high_bits_are_dropped_from_one_user():
    assert clamp({"id": 2, "permissions": LOW | HIGH}) == {
        "id": 2,
        "permissions": LOW,
    }


def test_every_clamped_value_fits_a_signed_int32():
    assert clamp({"permissions": (1 << 63) - 1})["permissions"] == 2**31 - 1


@pytest.mark.parametrize(
    "payload",
    [
        b'{"id": 3, "permissions": 50}',
        b'{"results": [{"id": 3, "permissions": 50}]}',
        b'{"results": [{"id": 3}]}',
        b'{"results": "unexpected"}',
        b'{"permissions": "50"}',
        b"[1, 2]",
        b"not json",
        b"",
    ],
)
def test_anything_else_is_returned_byte_for_byte(payload):
    assert clamp_permissions(payload) == payload
