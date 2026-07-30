"""
Unit tests for the borg-2 ZFS textfile exporter.

Covers the pure functions: Prometheus line formatting, the vdev tree walk, and
scrub-age parsing (the fiddliest part, since `zpool status -j` embeds a timezone
abbreviation that strptime cannot read).
"""

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
