# Dagger Engine

A single in-cluster engine (StatefulSet) that CI connects to with
`_EXPERIMENTAL_DAGGER_RUNNER_HOST=kube-pod://...`. Local runs use their own
engine (`image://`, in colima).

## Telemetry

```
dagger CLI (CI or local) --OTLP/HTTP--> otel-collector (monitoring-system)
  traces  (every call, exec and check as a span)      --> Tempo
          function and check spans --span_metrics-->      Prometheus --> VictoriaMetrics
  logs    (stdout/stderr of every span, trace-linked) --> Loki
  metrics (per-exec CPU, memory, IO, network)         --> Prometheus

engine pod
  /metrics :8080 (PodMonitor)  clients, dagql + local cache size  --> Prometheus
  telemetry.resourceMetrics    engine cgroup CPU, memory, network --> collector
```

The client is the only source of session telemetry: the engine streams a
session's spans, logs and metrics back to the CLI that opened it, and the CLI
re-exports them. A `dagger` run with no OTLP environment sends nothing, so
Renovate's `postUpgradeTasks` runs are not recorded.

### Enabling export

`scripts/dagger-otel-env.sh` sets the environment. Dagger only exports logs
and metrics when `OTEL_EXPORTER_OTLP_LOGS_ENDPOINT` and
`OTEL_EXPORTER_OTLP_METRICS_ENDPOINT` are set explicitly; with only
`OTEL_EXPORTER_OTLP_ENDPOINT` it exports traces alone.

- **CI**: every Woodpecker pipeline that runs dagger sources the script, which
  points at the in-cluster collector and tags the run with
  `deployment.environment.name=ci`, `cicd.pipeline.*` and `vcs.ref.head.*`.
- **Local**: opt in from the gitignored `devenv.local.nix`:

  ```nix
  {
    enterShell = ''
      . "$DEVENV_ROOT/scripts/dagger-otel-env.sh" https://otel.msng.to
    '';
  }
  ```

  `otel.msng.to` is the collector's OTLP/HTTP receiver on the internal gateway.
  It has no authentication, so it is reachable from the LAN only.

### Finding a run in Grafana

- Traces: Explore → Tempo, e.g.
  `{resource.service.name="dagger-cli" && resource.cicd.pipeline.run.id="1234"}`.
  CLI spans carry the CI and git attributes; engine spans carry
  `service.name=dagger-engine` (from `OTEL_SERVICE_NAME` in values.yaml) and
  the session ID.
- Span → logs and span → metrics links are set up on the Tempo datasource.
  Exec output is in Loki under `{service_name=~"dagger-(cli|engine)"}` with
  `trace_id` and `span_id` as structured metadata.
- Exec metrics are `dagger_io_metrics_*{job="dagger-engine"}`, labelled with
  `dagger_io_metrics_trace` and `dagger_io_metrics_span`, and some with
  `dagger_io_dag_digest`. The digest hashes the call's inputs, so it changes
  whenever a file the exec reads changes; it is not a stable name across runs.
  Each exec is a new series, so these stay in Prometheus' 7-day window
  (VictoriaMetrics' allowlist leaves them out).
- Each check is a span named after its `dag://` URL, e.g.
  `dag://?check=helmfile/releases/render&helmfile-release=dagger-engine`,
  with the same value in `dagger.io/check.name` and `dagger.io/check.passed`.
  Each module function call is a span with `dagger.io/module.function.name`
  (e.g. `Yaml.lint`) and `dagger.io/cache.outcome` (`hit`, `executed`,
  `joined` or `uncached`). Both names are stable across runs. Check and
  function spans carry the engine's resource, not the CLI's
  `deployment.environment.name` and `cicd.*` tags; only the trace's root
  `dagger-cli` span has those.
- A check's span is the parent of its function's span, not the same span:

  ```
  dag://?check=yaml/lint          check.name, check.passed
    Artifact.value                resolves the check's inputs
      Query.yaml, Yaml.lint       function.name, cache.outcome
    Check.sync
      exec.run                    the module's /runtime, on every run
    POST /query                   calls the check function makes
      Container.withExec          cache.outcome
        exec.run                  only when the exec really ran
          exec.processRun         wcprof.exec.argv: ["yamllint", ...]
  ```

  Check functions return a lazy container, so the function span takes ~0s
  and reports `executed` even when everything was cached. A check really ran
  when it has an `exec.processRun` other than the module's `["/runtime"]`.
  Check span durations mix cached and real runs (a cached check still takes a
  few seconds), and include time waiting for inputs and for work another
  check started (`cache.outcome = joined`). The span tree and attributes are
  covered in more depth in [docs/dagger-traces.md](../../../docs/dagger-traces.md).
- TraceQL metrics reach back up to a week (`max_duration`), e.g. p90 duration
  and failure rate per check, and p90 of only the checks whose execs ran.
  Quantiles come from power-of-two buckets, so they read as e.g. 8.6s or
  17.2s:

  ```
  {span."dagger.io/check.name" != nil} | quantile_over_time(duration, .9) by (span."dagger.io/check.name")
  {span."dagger.io/check.passed" = false} | rate() by (span."dagger.io/check.name")
  {name = "exec.processRun" && span."wcprof.exec.argv" != "[\"/runtime\"]"} << {span."dagger.io/check.name" != nil}
    | quantile_over_time(duration, .9) by (span."dagger.io/check.name")
  ```

### Duration trends

The collector's `span_metrics` connector turns function and check spans into
`traces_span_metrics_duration_seconds` histograms, labelled with
`dagger_io_module_function_name` and `dagger_io_cache_outcome`, or
`dagger_io_check_name` and `dagger_io_check_passed`. Other spans and all
resource attributes are dropped first, so runs don't create new series. The
buckets stay in Prometheus for 7 days; `_sum` and `_count` are kept in
VictoriaMetrics for the long term:

```
# p90 wall clock per check, last 7 days (Prometheus)
histogram_quantile(0.9, sum by (le, dagger_io_check_name) (
  increase(traces_span_metrics_duration_seconds_bucket{dagger_io_check_name!=""}[1d])))

# mean wall clock per check, over months (VictoriaMetrics)
sum by (dagger_io_check_name) (increase(traces_span_metrics_duration_seconds_sum{dagger_io_check_name!=""}[1d]))
  / sum by (dagger_io_check_name) (increase(traces_span_metrics_duration_seconds_count{dagger_io_check_name!=""}[1d]))

# check failure ratio per check
sum by (dagger_io_check_name) (increase(traces_span_metrics_duration_seconds_count{dagger_io_check_passed="false"}[1d]))
  / sum by (dagger_io_check_name) (increase(traces_span_metrics_duration_seconds_count{dagger_io_check_name!=""}[1d]))

# mean time spent in each module function's own body, e.g. toolchain setup
sum by (dagger_io_module_function_name) (increase(traces_span_metrics_duration_seconds_sum{dagger_io_module_function_name!=""}[1d]))
  / sum by (dagger_io_module_function_name) (increase(traces_span_metrics_duration_seconds_count{dagger_io_module_function_name!=""}[1d]))
```

These metrics can't separate cached from executed checks: that is only known
from a check's `exec.processRun` descendants, and span metrics see one
span at a time. Use the TraceQL query above for that, within Tempo's
retention. Per-function resource usage has no equivalent either: exec metrics
are labelled only with their exec's span, which nothing joins back to a
function name.

### Dashboard

The Dagger dashboard (`monitoring-system/dashboards/dagger.json`) combines the
queries above: check and function durations, cache hit rate, and failures.
The CI failure rate per check is failed runs over all runs of that check
under a CI root span, from TraceQL.

Check spans don't carry the commit or pipeline, so the collector turns each
CI run's root `dagger-cli` span into its own `dagger_ci_runs_calls_total`
series, labelled with `vcs_ref_head_revision`, `cicd_pipeline_run_id` and
`status_code` (`STATUS_CODE_ERROR` when any check failed). The CI runs count
comes from it.

### Engine resource metrics

The entrypoint (`files/dagger-entrypoint.sh`) moves the engine process into an
`init` leaf cgroup and runs execs under `buildkit/`, so
`telemetry.resourceMetrics` measures the engine process alone. Totals including
execs are the pod's cAdvisor metrics.

The engine also has a `telemetry.workloadExport` setting that would export
every session's spans from the engine itself, whatever client opened them. It
is left off: it is an internal option outside the documented config schema,
and with CI already exporting it would send every CI span to Tempo twice.
