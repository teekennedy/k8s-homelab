"""Write qBittorrent's PBKDF2-SHA512 WebUI password hash to /shared/password-hash."""

import base64
import hashlib
import os
from pathlib import Path

ITERATIONS = 100000
SALT_BYTES = 16
OUTPUT_PATH = Path("/shared/password-hash")


def hash_password(password: str, salt: bytes | None = None) -> str:
    """Return the password hash in qBittorrent's config format: @ByteArray(salt:hash)."""
    if salt is None:
        salt = os.urandom(SALT_BYTES)
    hash_bytes = hashlib.pbkdf2_hmac(
        "sha512", password.encode("utf-8"), salt, ITERATIONS
    )
    salt_b64 = base64.b64encode(salt).decode("ascii")
    hash_b64 = base64.b64encode(hash_bytes).decode("ascii")
    return f"@ByteArray({salt_b64}:{hash_b64})"


def main(output_path: Path = OUTPUT_PATH) -> None:
    output_path.write_text(hash_password(os.environ["QBITTORRENT_PASSWORD"]))
    print("Password hash generated successfully")


if __name__ == "__main__":
    main()
