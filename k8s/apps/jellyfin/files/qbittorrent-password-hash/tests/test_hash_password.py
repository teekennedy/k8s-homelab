import base64
import hashlib
import re

import pytest

import hash_password

BYTE_ARRAY = re.compile(r"^@ByteArray\(([A-Za-z0-9+/=]+):([A-Za-z0-9+/=]+)\)$")


def decode(result: str) -> tuple[bytes, bytes]:
    match = BYTE_ARRAY.match(result)
    assert match, result
    return base64.b64decode(match[1]), base64.b64decode(match[2])


def test_known_salt_gives_known_hash():
    salt = bytes(range(16))
    expected = hashlib.pbkdf2_hmac("sha512", b"hunter2", salt, 100000)

    assert hash_password.hash_password("hunter2", salt) == (
        f"@ByteArray({base64.b64encode(salt).decode()}"
        f":{base64.b64encode(expected).decode()})"
    )


def test_generated_salt_and_hash_sizes():
    salt, digest = decode(hash_password.hash_password("hunter2"))

    assert len(salt) == 16
    assert len(digest) == 64  # SHA-512


def test_generated_hash_verifies_against_its_salt():
    salt, digest = decode(hash_password.hash_password("hunter2"))

    assert digest == hashlib.pbkdf2_hmac("sha512", b"hunter2", salt, 100000)


def test_each_call_uses_a_fresh_salt():
    first = hash_password.hash_password("hunter2")
    second = hash_password.hash_password("hunter2")

    assert decode(first)[0] != decode(second)[0]


def test_non_ascii_password_is_utf8_encoded():
    salt = b"\x00" * 16
    _, digest = decode(hash_password.hash_password("pässwörd", salt))

    assert digest == hashlib.pbkdf2_hmac(
        "sha512", "pässwörd".encode("utf-8"), salt, 100000
    )


def test_main_writes_hash_from_env(tmp_path, monkeypatch, capsys):
    monkeypatch.setenv("QBITTORRENT_PASSWORD", "hunter2")
    output = tmp_path / "password-hash"

    hash_password.main(output)

    salt, digest = decode(output.read_text())
    assert digest == hashlib.pbkdf2_hmac("sha512", b"hunter2", salt, 100000)
    assert "successfully" in capsys.readouterr().out


def test_main_requires_password_env(tmp_path, monkeypatch):
    monkeypatch.delenv("QBITTORRENT_PASSWORD", raising=False)

    with pytest.raises(KeyError):
        hash_password.main(tmp_path / "password-hash")
