"""Export Intel iGPU metrics for the Prometheus node-exporter textfile collector.

Reads one sample from `intel_gpu_top -J` and writes intel_gpu.prom.
"""

import json
import os
import subprocess
import sys
import tempfile

INTEL_GPU_TOP = os.environ.get("INTEL_GPU_TOP_BIN", "intel_gpu_top")
TEXTFILE_DIR = os.environ.get(
    "TEXTFILE_DIR", "/var/lib/prometheus/node-exporter-textfiles"
)
OUTPUT = os.path.join(TEXTFILE_DIR, "intel_gpu.prom")

# Two samples at this period: the first is a short warm-up window, the second
# is a real measurement. Kept well inside the collection interval.
SAMPLE_PERIOD_MS = int(os.environ.get("SAMPLE_PERIOD_MS", "1000"))
TIMEOUT_SECONDS = float(os.environ.get("TIMEOUT_SECONDS", "5"))


def collect(errors):
    """Run intel_gpu_top for a bounded time and return its raw stdout.

    intel_gpu_top never exits on its own, so it is always killed by the
    timeout. The JSON array it opened is always left unterminated.
    """
    args = [INTEL_GPU_TOP, "-J", "-s", str(SAMPLE_PERIOD_MS)]
    try:
        result = subprocess.run(
            args,
            capture_output=True,
            text=True,
            timeout=TIMEOUT_SECONDS,
        )
        return result.stdout
    except subprocess.TimeoutExpired as exc:
        return exc.stdout or ""
    except (OSError, ValueError) as exc:
        msg = f"failed to run intel_gpu_top: {exc}"
        print(msg, file=sys.stderr)
        errors.append(msg)
        return ""


def parse_samples(raw):
    """Pull every complete JSON object out of intel_gpu_top's stream.

    The output is an array that is opened but, when the process is killed,
    never closed, with no commas between elements. raw_decode walks the
    concatenated objects and stops cleanly at a truncated tail.
    """
    if not raw:
        return []

    decoder = json.JSONDecoder()
    samples = []
    index = 0
    length = len(raw)

    while index < length:
        # Skip whitespace and the array punctuation intel_gpu_top emits.
        while index < length and raw[index] in " \t\r\n,[]":
            index += 1
        if index >= length:
            break
        try:
            obj, end = decoder.raw_decode(raw, index)
        except ValueError:
            # A partial object at the tail: everything before it is still good.
            break
        if isinstance(obj, dict):
            samples.append(obj)
        index = end

    return samples


def latest_sample(raw):
    samples = parse_samples(raw)
    return samples[-1] if samples else None


def _escape(value):
    return str(value).replace("\\", "\\\\").replace('"', '\\"').replace("\n", "\\n")


def _number(value, default=0.0):
    try:
        return float(value)
    except (TypeError, ValueError):
        return default


def render(sample, errors):
    """Render exposition text for one sample."""
    lines = []

    def metric(name, help_text, kind="gauge"):
        lines.append(f"# HELP {name} {help_text}")
        lines.append(f"# TYPE {name} {kind}")

    if sample is not None:
        engines = sample.get("engines") or {}

        metric(
            "intel_gpu_engine_busy_ratio",
            "Fraction of the sample window the engine had work submitted. "
            "Occupancy, not saturation: do not size capacity from this.",
        )
        for name, values in sorted(engines.items()):
            busy = _number((values or {}).get("busy")) / 100.0
            lines.append(
                f'intel_gpu_engine_busy_ratio{{engine="{_escape(name)}"}} {busy}'
            )

        metric(
            "intel_gpu_engine_wait_ratio",
            "Fraction of the sample window the engine spent waiting.",
        )
        for name, values in sorted(engines.items()):
            wait = _number((values or {}).get("wait")) / 100.0
            lines.append(
                f'intel_gpu_engine_wait_ratio{{engine="{_escape(name)}"}} {wait}'
            )

        metric(
            "intel_gpu_engine_sema_ratio",
            "Fraction of the sample window the engine spent on semaphore waits.",
        )
        for name, values in sorted(engines.items()):
            sema = _number((values or {}).get("sema")) / 100.0
            lines.append(
                f'intel_gpu_engine_sema_ratio{{engine="{_escape(name)}"}} {sema}'
            )

        frequency = sample.get("frequency") or {}
        metric(
            "intel_gpu_frequency_mhz",
            "GPU clock. Describes the render domain, which can read near zero "
            "while the media engines are busy.",
        )
        lines.append(
            f'intel_gpu_frequency_mhz{{kind="requested"}} '
            f"{_number(frequency.get('requested'))}"
        )
        lines.append(
            f'intel_gpu_frequency_mhz{{kind="actual"}} '
            f"{_number(frequency.get('actual'))}"
        )

        power = sample.get("power") or {}
        metric(
            "intel_gpu_power_watts",
            "Power draw. A zero GPU domain may mean unsupported rather than idle.",
        )
        lines.append(
            f'intel_gpu_power_watts{{domain="gpu"}} {_number(power.get("GPU"))}'
        )
        lines.append(
            f'intel_gpu_power_watts{{domain="package"}} {_number(power.get("Package"))}'
        )

        metric("intel_gpu_rc6_ratio", "Fraction of the window spent in RC6 sleep.")
        lines.append(
            f"intel_gpu_rc6_ratio {_number((sample.get('rc6') or {}).get('value')) / 100.0}"
        )

        metric("intel_gpu_interrupts_per_second", "GPU interrupt rate.")
        lines.append(
            "intel_gpu_interrupts_per_second "
            f"{_number((sample.get('interrupts') or {}).get('count'))}"
        )

        clients = sample.get("clients") or {}
        if clients:
            # Aggregated by process name: pids churn on every transcode and
            # would leave a trail of dead series.
            counts = {}
            busy_by_client = {}
            memory = {}
            for client in clients.values():
                name = (client or {}).get("name") or "unknown"
                counts[name] = counts.get(name, 0) + 1
                for engine, values in (
                    (client or {}).get("engine-classes") or {}
                ).items():
                    key = (name, engine)
                    busy_by_client[key] = busy_by_client.get(key, 0.0) + _number(
                        (values or {}).get("busy")
                    )
                system = ((client or {}).get("memory") or {}).get("system") or {}
                memory[name] = memory.get(name, 0.0) + _number(system.get("resident"))

            metric("intel_gpu_clients", "Processes with an open GPU context.")
            for name, count in sorted(counts.items()):
                lines.append(f'intel_gpu_clients{{client="{_escape(name)}"}} {count}')

            metric(
                "intel_gpu_client_engine_busy_ratio",
                "Per-process engine occupancy. Can exceed 1.0: busy is reported "
                "per engine instance and there is more than one VDBox.",
            )
            for (name, engine), busy in sorted(busy_by_client.items()):
                lines.append(
                    f'intel_gpu_client_engine_busy_ratio{{client="{_escape(name)}",'
                    f'engine="{_escape(engine)}"}} {busy / 100.0}'
                )

            metric(
                "intel_gpu_client_memory_resident_bytes",
                "Resident GPU-visible system memory per process.",
            )
            for name, value in sorted(memory.items()):
                lines.append(
                    f'intel_gpu_client_memory_resident_bytes{{client="{_escape(name)}"}} '
                    f"{value}"
                )

    metric("intel_gpu_exporter_success", "1 if the last collection produced a sample.")
    lines.append(f"intel_gpu_exporter_success {0 if sample is None else 1}")

    metric("intel_gpu_exporter_errors", "Errors during the last collection.")
    lines.append(f"intel_gpu_exporter_errors {len(errors)}")

    return "\n".join(lines) + "\n"


def write_atomically(path, content):
    """Write via a temp file and rename.

    node_exporter reads this directory on its own schedule; a partially
    written file would be parsed and rejected.
    """
    directory = os.path.dirname(path)
    os.makedirs(directory, exist_ok=True)
    handle, temp_path = tempfile.mkstemp(dir=directory, prefix=".intel_gpu.")
    try:
        with os.fdopen(handle, "w") as stream:
            stream.write(content)
        os.chmod(temp_path, 0o644)
        os.replace(temp_path, path)
    except BaseException:
        try:
            os.unlink(temp_path)
        except OSError:
            pass
        raise


def main():
    errors = []
    raw = collect(errors)
    sample = latest_sample(raw)
    if sample is None and not errors:
        msg = "intel_gpu_top produced no complete sample"
        print(msg, file=sys.stderr)
        errors.append(msg)
    write_atomically(OUTPUT, render(sample, errors))
    return 0


if __name__ == "__main__":
    sys.exit(main())
