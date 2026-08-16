# intel-gpu-textfile-exporter

Exports Intel iGPU metrics for the Prometheus node-exporter textfile collector.
A oneshot systemd unit runs every minute and writes `intel_gpu.prom` into
`/var/lib/prometheus/node-exporter-textfiles`.

## Understanding metric values

`intel_gpu_engine_busy_ratio` measures how much of the sample window an engine
had work submitted, not how close it is to its throughput limit. Do not size
capacity from it and do not alert on it.

To illustrate with an example: a single Jellyfin 1080p→720p H.264 transcode
showed `Video` at 94% busy and `VideoEnhance` at 99%, which looks like a
saturated GPU. The same ffmpeg process was encoding at **45x realtime** (1080
fps) while drawing **0.29 W**. It was sprinting to fill its buffer, not
struggling. Engine-busy says "work was queued", and ffmpeg queues work as fast
as it possibly can.

For capacity estimation, use the count of concurrent video transcodes from the
jellyfin-exporter, or ffmpeg's own `speed=` figure. Anything under roughly 1x
realtime is the real ceiling.

Other things to keep in mind:

- **Per-client ratios can exceed 1.0.** Busy is reported per engine *instance*
  and these parts have more than one VDBox, so one ffmpeg using two of them
  fully reports 200%.
- **`intel_gpu_power_watts{domain="gpu"}` and `intel_gpu_frequency_mhz` describe
  the render domain.** On parts where the media engines live in a separate power
  well, both read near zero during video work. Zero there does not mean idle.

## Metrics

| Metric | Labels | Notes |
| --- | --- | --- |
| `intel_gpu_engine_busy_ratio` | `engine` | occupancy, see above |
| `intel_gpu_engine_wait_ratio` | `engine` | |
| `intel_gpu_engine_sema_ratio` | `engine` | |
| `intel_gpu_frequency_mhz` | `kind` | `requested` / `actual` |
| `intel_gpu_power_watts` | `domain` | `gpu` / `package` |
| `intel_gpu_rc6_ratio` | | sleep residency |
| `intel_gpu_interrupts_per_second` | | |
| `intel_gpu_clients` | `client` | processes holding a GPU context |
| `intel_gpu_client_engine_busy_ratio` | `client`, `engine` | may exceed 1.0 |
| `intel_gpu_client_memory_resident_bytes` | `client` | |
| `intel_gpu_exporter_success` | | 0 if no sample parsed |
| `intel_gpu_exporter_errors` | | |

Per-client series are aggregated by process **name**, not pid: pids churn on
every transcode and would leave a trail of dead series behind.

## Parsing challenges

`intel_gpu_top -J` opens a JSON array, emits one object per sample with **no
commas between them**, and only closes the array on a clean exit. It never exits
on its own, so a timer-driven collection always kills it and always ends up with
input that no JSON parser accepts as-is:

```
[
{ "period": ... }
{ "period": ... }
```

`parse_samples` walks the stream with `JSONDecoder.raw_decode`, which handles the
missing commas, the unterminated array, and a half-written object at the tail
where the kill landed mid-write. The first sample is a short warm-up window, so
the exporter reports the last complete one.

## Privileges

Reading i915 perf counters is privileged. The unit takes `CAP_PERFMON` rather
than running as full root, and is otherwise confined (`ProtectSystem=strict`,
`PrivateNetwork=true`, write access only to the textfile directory).

## Running tests

```sh
cd nix/hosts/common/intel-gpu-exporter
uv sync --dev
uv run pytest
```

The script is built with `pkgs.writers.writePython3Bin`, which runs flake8 at
build time; `flakeIgnore` in the nix module keeps it from fighting black over
line length.
