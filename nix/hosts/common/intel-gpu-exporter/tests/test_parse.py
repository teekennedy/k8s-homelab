"""Parsing intel_gpu_top -J output.

`intel_gpu_top -J` opens a JSON array and emits one object per sample, but
it writes no commas between them and only closes the array on a clean
exit. Killing it after N seconds -- which is what a oneshot timer does --
therefore leaves output that no JSON parser will accept as-is. These
tests pin down the recovery.
"""

from __future__ import annotations

import pytest

import intel_gpu_exporter as exporter

SAMPLE = """
{
  "period": { "duration": 52.944081, "unit": "ms" },
  "frequency": { "requested": 509.972021, "actual": 37.775705, "unit": "MHz" },
  "interrupts": { "count": 3494.252738, "unit": "irq/s" },
  "rc6": { "value": 71.168220, "unit": "%" },
  "power": { "GPU": 0.289359, "Package": 18.026694, "unit": "W" },
  "engines": {
    "Render/3D": { "busy": 0.000000, "sema": 0.000000, "wait": 0.000000, "unit": "%" },
    "Blitter": { "busy": 0.000000, "sema": 0.000000, "wait": 0.000000, "unit": "%" },
    "Video": { "busy": 94.129140, "sema": 0.000000, "wait": 1.500000, "unit": "%" },
    "VideoEnhance": { "busy": 99.242089, "sema": 0.000000, "wait": 0.000000, "unit": "%" },
    "Compute": { "busy": 0.000000, "sema": 0.000000, "wait": 0.000000, "unit": "%" }
  },
  "clients": {
    "4294329556": {
      "name": "ffmpeg",
      "pid": "637740",
      "memory": { "system": { "total": "161628160", "resident": "155996160" } },
      "engine-classes": {
        "Render/3D": { "busy": "0.000000", "unit": "%" },
        "Video": { "busy": "200.661203", "unit": "%" },
        "VideoEnhance": { "busy": "101.363664", "unit": "%" }
      }
    }
  }
}
"""

IDLE = """
{
  "period": { "duration": 1047.245062, "unit": "ms" },
  "frequency": { "requested": 0.000000, "actual": 0.000000, "unit": "MHz" },
  "interrupts": { "count": 0.000000, "unit": "irq/s" },
  "rc6": { "value": 100.000000, "unit": "%" },
  "power": { "GPU": 0.000000, "Package": 10.211176, "unit": "W" },
  "engines": {
    "Video": { "busy": 0.000000, "sema": 0.000000, "wait": 0.000000, "unit": "%" }
  },
  "clients": {}
}
"""


class TestSampleExtraction:
    def test_parses_a_truncated_unterminated_array(self):
        """The array is opened but never closed when the process is killed."""
        raw = "[\n" + SAMPLE + IDLE
        samples = exporter.parse_samples(raw)
        assert len(samples) == 2

    def test_parses_a_properly_closed_array(self):
        raw = "[\n" + SAMPLE + IDLE + "]"
        assert len(exporter.parse_samples(raw)) == 2

    def test_tolerates_missing_commas_between_objects(self):
        raw = "[" + SAMPLE + SAMPLE + SAMPLE
        assert len(exporter.parse_samples(raw)) == 3

    def test_tolerates_commas_between_objects(self):
        raw = "[" + SAMPLE + "," + IDLE
        assert len(exporter.parse_samples(raw)) == 2

    def test_ignores_a_trailing_partial_object(self):
        """SIGKILL can land mid-write."""
        raw = "[" + SAMPLE + '{ "period": { "dur'
        assert len(exporter.parse_samples(raw)) == 1

    def test_empty_output_yields_no_samples(self):
        assert exporter.parse_samples("") == []
        assert exporter.parse_samples("[\n") == []

    def test_last_complete_sample_is_selected(self):
        """The first sample is a short warm-up window; prefer the newest."""
        raw = "[" + SAMPLE + IDLE
        latest = exporter.latest_sample(raw)
        assert latest["rc6"]["value"] == 100.0

    def test_latest_sample_is_none_when_nothing_parses(self):
        assert exporter.latest_sample("[") is None


class TestRender:
    def render(self, raw=SAMPLE):
        sample = exporter.latest_sample("[" + raw)
        return exporter.render(sample, errors=[])

    def parse(self, text):
        out = {}
        for line in text.splitlines():
            if not line or line.startswith("#"):
                continue
            name, _, value = line.rpartition(" ")
            out[name] = float(value)
        return out

    def test_engine_busy_is_reported_as_a_ratio(self):
        m = self.parse(self.render())
        assert m['intel_gpu_engine_busy_ratio{engine="Video"}'] == pytest.approx(
            0.9412914
        )
        assert m['intel_gpu_engine_busy_ratio{engine="Render/3D"}'] == 0.0

    def test_engine_wait_is_reported(self):
        m = self.parse(self.render())
        assert m['intel_gpu_engine_wait_ratio{engine="Video"}'] == pytest.approx(0.015)

    def test_frequency_and_power_are_reported(self):
        m = self.parse(self.render())
        assert m['intel_gpu_frequency_mhz{kind="actual"}'] == pytest.approx(37.775705)
        assert m['intel_gpu_frequency_mhz{kind="requested"}'] == pytest.approx(
            509.972021
        )
        assert m['intel_gpu_power_watts{domain="gpu"}'] == pytest.approx(0.289359)
        assert m['intel_gpu_power_watts{domain="package"}'] == pytest.approx(18.026694)

    def test_rc6_and_interrupts_are_reported(self):
        m = self.parse(self.render())
        assert m["intel_gpu_rc6_ratio"] == pytest.approx(0.7116822)
        assert m["intel_gpu_interrupts_per_second"] == pytest.approx(3494.252738)

    def test_per_client_busy_is_aggregated_by_process_name(self):
        """pid would churn every transcode; the name is what we can chart."""
        m = self.parse(self.render())
        key = 'intel_gpu_client_engine_busy_ratio{client="ffmpeg",engine="Video"}'
        assert m[key] == pytest.approx(2.00661203)

    def test_per_client_ratios_may_exceed_one(self):
        """Busy is per engine instance, and there is more than one VDBox."""
        m = self.parse(self.render())
        key = 'intel_gpu_client_engine_busy_ratio{client="ffmpeg",engine="Video"}'
        assert m[key] > 1.0

    def test_client_count_is_reported(self):
        m = self.parse(self.render())
        assert m['intel_gpu_clients{client="ffmpeg"}'] == 1

    def test_client_memory_is_reported(self):
        m = self.parse(self.render())
        key = 'intel_gpu_client_memory_resident_bytes{client="ffmpeg"}'
        assert m[key] == 155996160

    def test_idle_gpu_renders_without_clients(self):
        text = self.render(IDLE)
        assert "intel_gpu_client_engine_busy_ratio" not in text
        assert 'intel_gpu_engine_busy_ratio{engine="Video"} 0.0' in text

    def test_success_metric_is_emitted(self):
        m = self.parse(self.render())
        assert m["intel_gpu_exporter_success"] == 1

    def test_failure_is_reported_when_no_sample_parsed(self):
        text = exporter.render(None, errors=["intel_gpu_top exploded"])
        m = self.parse(text)
        assert m["intel_gpu_exporter_success"] == 0
        assert m["intel_gpu_exporter_errors"] == 1

    def test_metric_names_are_escaped_for_odd_client_names(self):
        sample = exporter.latest_sample("[" + SAMPLE)
        sample["clients"]["1"] = {
            "name": 'we"ird',
            "pid": "1",
            "engine-classes": {"Video": {"busy": "1.0"}},
        }
        text = exporter.render(sample, errors=[])
        assert 'client="we\\"ird"' in text
