"""
Unit tests for the borg-2 ZFS textfile exporter.

Covers the pure functions: Prometheus line formatting, the vdev tree walks,
scrub state, vdev-to-disk resolution, and scrub-age parsing (the fiddliest
part, since `zpool status -j` embeds a timezone abbreviation that strptime
cannot read).
"""

import os
from datetime import datetime, timedelta

import pytest

import zfs_exporter


class TestMetric:
    def test_formats_labels_and_value(self):
        assert (
            zfs_exporter.metric("zpool_health", {"pool": "tank"}, 1)
            == 'zpool_health{pool="tank"} 1'
        )

    def test_joins_multiple_labels_with_commas(self):
        out = zfs_exporter.metric("m", {"pool": "tank", "vdev": "sda"}, "0")
        assert out == 'm{pool="tank",vdev="sda"} 0'

    def test_handles_empty_labels(self):
        assert zfs_exporter.metric("m", {}, 5) == "m{} 5"


class TestParseScrubAge:
    def _format(self, dt, tz="MDT"):
        # Reproduces the `zpool status -j` end_time format, e.g.
        # "Wed Jul  1 05:56:09 AM MDT 2026".
        return dt.strftime("%a %b %d %I:%M:%S %p ") + tz + dt.strftime(" %Y")

    def test_returns_age_in_seconds(self):
        age = zfs_exporter.parse_scrub_age(
            self._format(datetime.now() - timedelta(hours=1))
        )
        assert age == pytest.approx(3600, abs=60)

    def test_recent_scrub_is_near_zero(self):
        assert zfs_exporter.parse_scrub_age(
            self._format(datetime.now())
        ) == pytest.approx(0, abs=60)

    @pytest.mark.parametrize("tz", ["MDT", "MST", "UTC", "CEST", "ET"])
    def test_strips_timezone_abbreviations_of_varying_length(self, tz):
        # The regex accepts 2-4 uppercase letters; all of these must parse.
        assert (
            zfs_exporter.parse_scrub_age(self._format(datetime.now(), tz=tz))
            is not None
        )

    def test_unparseable_string_returns_none(self):
        assert zfs_exporter.parse_scrub_age("not a timestamp") is None

    def test_empty_string_returns_none(self):
        assert zfs_exporter.parse_scrub_age("") is None


class TestCollectVdevs:
    def _empty_acc(self):
        return {"read": [], "write": [], "cksum": [], "slow": []}

    def test_collects_error_counters(self):
        acc = self._empty_acc()
        vdevs = {
            "sda": {
                "read_errors": "1",
                "write_errors": "2",
                "checksum_errors": "3",
            }
        }
        zfs_exporter.collect_vdevs(vdevs, "tank", acc)
        assert acc["read"] == [("tank", "sda", "1")]
        assert acc["write"] == [("tank", "sda", "2")]
        assert acc["cksum"] == [("tank", "sda", "3")]

    def test_missing_counters_default_to_zero(self):
        acc = self._empty_acc()
        zfs_exporter.collect_vdevs({"sda": {}}, "tank", acc)
        assert acc["read"] == [("tank", "sda", "0")]
        assert acc["write"] == [("tank", "sda", "0")]
        assert acc["cksum"] == [("tank", "sda", "0")]

    def test_slow_ios_only_recorded_when_present(self):
        acc = self._empty_acc()
        zfs_exporter.collect_vdevs({"sda": {}}, "tank", acc)
        assert acc["slow"] == []

        acc = self._empty_acc()
        zfs_exporter.collect_vdevs({"sdb": {"slow_ios": "7"}}, "tank", acc)
        assert acc["slow"] == [("tank", "sdb", "7")]

    def test_recurses_into_nested_vdevs(self):
        # A real pool nests disks under a raidz/mirror vdev.
        acc = self._empty_acc()
        vdevs = {
            "raidz1-0": {
                "read_errors": "0",
                "vdevs": {
                    "sda": {"read_errors": "4"},
                    "sdb": {"read_errors": "5"},
                },
            }
        }
        zfs_exporter.collect_vdevs(vdevs, "tank", acc)
        names = {name: value for _, name, value in acc["read"]}
        assert names == {"raidz1-0": "0", "sda": "4", "sdb": "5"}

    def test_labels_carry_the_pool_name(self):
        acc = self._empty_acc()
        zfs_exporter.collect_vdevs({"sda": {}}, "mypool", acc)
        assert acc["read"][0][0] == "mypool"


class TestScrubInProgress:
    def test_running_scrub(self):
        scan = {"function": "SCRUB", "state": "SCANNING", "scrub_pause": "-"}
        assert zfs_exporter.scrub_in_progress(scan) == 1

    def test_paused_scrub_is_not_in_progress(self):
        # A paused scrub stays SCANNING but issues no I/O.
        scan = {
            "function": "SCRUB",
            "state": "SCANNING",
            "scrub_pause": "Sun Nov  1 03:00:00 AM MST 2026",
        }
        assert zfs_exporter.scrub_in_progress(scan) == 0

    def test_missing_pause_field_means_running(self):
        scan = {"function": "SCRUB", "state": "SCANNING"}
        assert zfs_exporter.scrub_in_progress(scan) == 1

    @pytest.mark.parametrize("state", ["FINISHED", "CANCELED"])
    def test_finished_or_canceled_scrub(self, state):
        scan = {"function": "SCRUB", "state": state, "scrub_pause": "-"}
        assert zfs_exporter.scrub_in_progress(scan) == 0


@pytest.fixture
def fake_devices(tmp_path):
    """
    Builds a /dev/disk/by-id and /sys/class/block layout like the kernel's:
    sdb is a whole disk, sdb1 its partition (with a `partition` file and a
    parent directory of sdb), and sdc a whole disk used without partitions.
    """
    dev = tmp_path / "dev"
    by_id = dev / "disk" / "by-id"
    by_id.mkdir(parents=True)
    for name in ("sdb", "sdb1", "sdc"):
        (dev / name).touch()
    os.symlink("../../sdb1", by_id / "wwn-0xb-part1")
    os.symlink("../../sdc", by_id / "wwn-0xc")

    devices = tmp_path / "sys" / "devices" / "sdb"
    (devices / "sdb1").mkdir(parents=True)
    (devices / "sdb1" / "partition").write_text("1\n")
    (tmp_path / "sys" / "devices" / "sdc").mkdir()

    sys_block = tmp_path / "sys" / "class" / "block"
    sys_block.mkdir(parents=True)
    os.symlink(devices, sys_block / "sdb")
    os.symlink(devices / "sdb1", sys_block / "sdb1")
    os.symlink(tmp_path / "sys" / "devices" / "sdc", sys_block / "sdc")
    return by_id, str(sys_block)


class TestWholeDisk:
    def test_partition_resolves_to_parent_disk(self, fake_devices):
        by_id, sys_block = fake_devices
        assert zfs_exporter.whole_disk(str(by_id / "wwn-0xb-part1"), sys_block) == "sdb"

    def test_whole_disk_resolves_to_itself(self, fake_devices):
        by_id, sys_block = fake_devices
        assert zfs_exporter.whole_disk(str(by_id / "wwn-0xc"), sys_block) == "sdc"


class TestCollectDisks:
    def test_maps_leaf_disks_under_raidz(self, fake_devices):
        by_id, sys_block = fake_devices
        vdevs = {
            "storage": {
                "vdev_type": "root",
                "vdevs": {
                    "raidz2-0": {
                        "vdev_type": "raidz",
                        "vdevs": {
                            "wwn-0xb-part1": {
                                "vdev_type": "disk",
                                "path": str(by_id / "wwn-0xb-part1"),
                            },
                            "wwn-0xc": {
                                "vdev_type": "disk",
                                "path": str(by_id / "wwn-0xc"),
                            },
                        },
                    }
                },
            }
        }
        acc = []
        zfs_exporter.collect_disks(vdevs, "storage", acc, sys_block)
        assert acc == [
            ("storage", "wwn-0xb-part1", "sdb"),
            ("storage", "wwn-0xc", "sdc"),
        ]

    def test_skips_disks_without_a_path(self):
        # A missing or removed disk is reported without a path.
        acc = []
        zfs_exporter.collect_disks({"sdz": {"vdev_type": "disk"}}, "tank", acc)
        assert acc == []
