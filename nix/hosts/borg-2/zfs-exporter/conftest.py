"""
zfs_exporter reads ZPOOL_BIN and TEXTFILE_DIR from the environment at import
time (systemd supplies them, see ../zfs-textfile-exporter.nix), so they have to
exist before the module can be imported at all. conftest.py runs first, and the
values are never used by the pure functions under test — nothing here shells out
to zpool or writes to the textfile directory.
"""

import os

os.environ.setdefault("ZPOOL_BIN", "/nonexistent/zpool")
os.environ.setdefault("TEXTFILE_DIR", "/nonexistent/textfile-dir")
