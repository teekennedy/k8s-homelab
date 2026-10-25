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
  covered in more depth in [Dagger traces in Tempo](#dagger-traces-in-tempo).
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

## Dagger traces in Tempo

What Dagger puts in its OpenTelemetry traces, why those traces are hard on
Tempo, what the collector strips before they get there, and what it would take
to replace Dagger Cloud's trace UI with something self-hosted. It saves
re-reading the engine and TUI source. Everything here was checked against
Dagger `v1.0.0-beta.20` (the engine this repo runs) and real CI traces from
October 2026. Source links point at that tag. How telemetry is wired up (OTLP
endpoints, Loki, span metrics, finding a run in Grafana) is under
[Telemetry](#telemetry) above.

### Summary

- A fully cached 30-second `dagger --env ci check` run is **128,750 spans and
  26 MB** of protobuf. Longer runs are bigger. Tempo's query frontend refuses
  responses over 16 MB, so Grafana could not open most CI traces, and
  rebuilding one of them in memory during compaction OOMKilled Tempo at 2Gi.
- **85% of those spans are engine debug output**: one span per string
  comparison while resolving `dag://` addresses. They have no children, no
  attributes worth keeping, and Dagger's own UI never shows them.
- The collector now drops those spans and `dagql.publishResult`, and strips
  attributes only Dagger Cloud or Dagger's offline profiler read. The same CI
  trace goes from 26.2 MB to 5.1 MB (81% smaller, 13,742 spans).
- What remains is still shaped for Dagger's UI, which hides `ui.internal`
  subtrees and splices out `ui.passthrough` spans. Doing the same before
  Tempo needs whole-trace context the collector doesn't have; a custom UI
  built on Dagger's open-source `dagui` package could do it.

### The problem

#### Size

| Run | Spans | Size |
|---|---|---|
| `dagger generate dagger-go-sdk` (25s) | 4,789 | 4.7 MB as JSON |
| `dagger --env ci check`, fully cached (30s) | 128,750 | 26.2 MB as protobuf |
| `dagger --env ci check`, cached (43s) | | 25.8 MB as protobuf |
| `dagger --env ci check`, partly executed (11 min) | | not measured: fetching it OOMKilled Tempo |

About 200 bytes of protobuf per span, roughly 1 KB once Tempo renders it as
JSON. Over a week Tempo received about 1.3 million spans.

#### What it broke

- **Tempo compaction OOM (2026-10-23).** The live-store cuts a block every
  30s (`max_block_duration`), so any Dagger run longer than that lands in
  several blocks, and compaction later merges each trace's pieces in memory.
  One merge needed more than the 2Gi limit. The backend scheduler hands an
  unfinished job back after every restart until `dead_job_timeout` (24h), so
  Tempo crash-looped on the same job. The limit is now 6Gi with
  `GOMEMLIMIT=5400MiB`.
- **Traces Grafana can't open.** Trace-by-ID requests go through the query
  frontend, whose gRPC message limit is 16 MB:
  `response larger than the max (25807779 vs 16777216)`. The querier serves
  the same trace over plain HTTP at `/querier/api/traces/<id>`, which is how
  the numbers in this document were measured.
- **Fetching a trace is expensive.** Opening one 11-minute CI trace by ID
  OOMKilled Tempo at 2Gi. With 6Gi it holds, but memory still spikes to
  about 5 GB during compaction of large traces.

### How Dagger shapes a trace

#### Export model

The engine and the CLI each export their own spans: `service.name`
`dagger-cli` for the handful of client spans (13 in a CI run), and
`dagger-engine` for everything else. CI and git resource attributes
(`deployment.environment.name`, `cicd.*`, `vcs.*`) are only on the CLI's
resource. Engine spans, including check spans, are in the same trace and
descend from the CLI's root span, so "this check ran in CI" is the TraceQL
`{resource.deployment.environment.name = "ci"} >> {span."dagger.io/check.name" != nil}`.

Dagger's own UI receives **live** spans: a snapshot when a span starts, then
the finished span (`LiveSpanProcessor` in
[otel-go `live.go`](https://github.com/dagger/otel-go/blob/132b1dd82eb2/live.go)).
Exporters configured through `OTEL_EXPORTER_OTLP_*` are wrapped in
`FilterLiveSpansExporter`
([`exporters.go`](https://github.com/dagger/otel-go/blob/132b1dd82eb2/exporters.go),
[`init.go`](https://github.com/dagger/otel-go/blob/132b1dd82eb2/init.go)),
so the collector only ever sees **finished** spans. Children finish first, so
they usually arrive before their parents. That is what makes any rule like
"drop everything under an internal span" impossible span by span.

#### Anatomy of a check

`yaml/lint` in a CI run where its input changed, with debug spans and
`dagql.publishResult` left out:

```
dag://?check=yaml/lint                    3.9s  check.name, check.passed; ui.rollup.logs, ui.rollup.spans
  Artifact.value                          1.4s  uncached: resolves the check's inputs
    Address.container                     1.2s  executed, internal: resolves dag://nix/devenv/ci-toolchain
      Address.container                         passthrough
        "cue/export".Glob(...) -> no match      debug spans, ~200 per resolution
    Query.yaml                                  hit, internal
      uploading /woodpecker/src/.../k8s-homelab  encapsulated, encapsulate
    Yaml.lint                                   executed: the check function, returns a lazy container
  Check.sync                              2.5s  passthrough
    exec.run                              2.5s  passthrough
      exec.containerStart                 0.0s  engine overhead
      exec.processRun                     2.4s  argv ["/runtime"]: the module's Go code
  POST /query                             2.4s  passthrough: an API call the module code made
    Container.withMountedDirectory, Container.withWorkdir, Container.withExec   executed
      exec.run                            2.4s  only when the exec really ran
        exec.containerStart
        exec.processRun                   2.4s  argv ["yamllint", "--config-file", ...]
    resume withExec (execOutputs)         2.4s  the lazy evaluation that forced it
```

Things that aren't obvious from the names:

- **Two kinds of exec.** Every check runs its module's `/runtime` (the
  module's compiled Go code) under `Check.sync`, cached or not. The check's
  own work is a separate exec under the API calls that code makes. A check
  really ran when it has an `exec.processRun` whose `wcprof.exec.argv` isn't
  `["/runtime"]`; over a week that was 11% of check runs. The exec spans come
  from [`engine/engineutil/otelprof.go`](https://github.com/dagger/dagger/blob/v1.0.0-beta.20/engine/engineutil/otelprof.go#L49)
  and [`executor.go`](https://github.com/dagger/dagger/blob/v1.0.0-beta.20/engine/engineutil/executor.go#L205).
- **`cache.outcome` on a call is a lookup fact, not "did a process run".**
  `Container.withExec` returns a lazy container in about 1ms. Its outcome can
  be `hit` on a lazy shell that still has to be evaluated. The work shows up
  in `resume <field> (<what>)` spans
  ([`dagql/otelprof_lazy.go`](https://github.com/dagger/dagger/blob/v1.0.0-beta.20/dagql/otelprof_lazy.go#L149))
  and the `exec.run` beneath them.
- **UI parent vs causal parent.** Lazy work is parented under the call that
  returned it, so it renders in a sensible place, even though that call
  already ended. `wcprof.parent` names the real cause when it differs.
- **Check spans have no `cache.outcome`.** Cache hit or miss for a check has
  to be derived from its descendants, as above.
- **Check wall time includes waiting.** A check's span covers resolving its
  inputs and waiting on work another check started (`cache.outcome =
  joined`). The slowest check in a week by wall time, `ci/integration-test`
  at 395s, spent 213s in its own function; the rest was before `Check.sync`
  started.

#### The debug spans

[`core/modtree.go`](https://github.com/dagger/dagger/blob/v1.0.0-beta.20/core/modtree.go#L263)
has a `debugTrace` helper that starts and ends a span for every comparison
while matching a path against the module tree:
`"%q.Glob(%q) -> no match"`, `"%v.Contains(%v) -> %v"`, `"%v.Equals(%v)..."`,
`"%q: does not match %v. Skipping"`. Resolving a `dag://` address walks the
whole tree through that matcher
([`matchWorkspaceInclude`](https://github.com/dagger/dagger/blob/v1.0.0-beta.20/core/schema/workspace.go#L4618)).
Every module in `dagger.toml` except `nix` has
`container = "dag://nix/devenv/ci-toolchain"`, and the engine resolves it
528 times in one CI run, so the run emits 110,088 of these spans (85% of
spans, 66% of bytes). They sit under an internal `Address.container` span,
carry only `wcprof.engine_span`, never have children, and never error.

### Attribute reference

"Kept" means it still reaches Tempo. "Dropped" means
`transform/dagger-trace-attributes` removes it. Shares are approximate
(attribute key and value length) of the 26 MB CI trace before filtering.

#### UI hints (`dagger.io/ui.*`)

Defined in [otel-go `attrs.go`](https://github.com/dagger/otel-go/blob/132b1dd82eb2/attrs.go);
read in `dagui`'s attribute switch
([`dagql/dagui/spans.go`](https://github.com/dagger/dagger/blob/v1.0.0-beta.20/dagql/dagui/spans.go#L532))
and applied in `Span.Hidden` and `EncapsulationHidden`
([spans.go#L1070](https://github.com/dagger/dagger/blob/v1.0.0-beta.20/dagql/dagui/spans.go#L1070))
and `FrontendOpts.ShouldShow`
([opts.go#L134](https://github.com/dagger/dagger/blob/v1.0.0-beta.20/dagql/dagui/opts.go#L134)).

| Attribute | What Dagger's UI does | In a CI run | Kept? |
|---|---|---|---|
| `ui.internal` | Hides the span and its subtree unless verbosity is raised | 7,820 spans | Kept |
| `ui.passthrough` | Hides the span and shows its children in its place, moving its logs to the parent. Still scanned for failures (a `POST /query` never fails itself) | 12,787 | Kept |
| `ui.encapsulate` | Hides the span's children unless the span fails | 308 | Kept |
| `ui.encapsulated` | Hides this span unless its parent fails | 307 | Kept |
| `ui.reveal` | Shows the span up to the top level even inside hidden subtrees | | Kept |
| `ui.boundary` | Stops reveal and rollups from bubbling past this span | | Kept |
| `ui.rollup.logs`, `ui.rollup.spans` | Aggregates children's logs and progress into this span (set on check spans) | 258 each | Kept |
| `ui.message`, `ui.actor.emoji` | LLM conversation display | | Kept |

The span-count numbers aren't the cost: a boolean attribute is a few bytes.
The cost is the spans these flags mark, which Tempo stores and Grafana shows
like any other span.

#### Call identity (`dagger.io/dag.*`)

| Attribute | Meaning | Share | Kept? |
|---|---|---|---|
| `dag.call` | The call as base64 protobuf `callpbv1.Call` ([call.proto](https://github.com/dagger/dagger/blob/v1.0.0-beta.20/dagql/call/callpbv1/call.proto#L48)): field, receiver digest, arguments, module. Dagger's UI renders call chains and arguments from it | 4.0% | Dropped |
| `dag.digest` | Digest of the call, a node ID in the DAG. Changes when any input changes | 2.4% | Kept |
| `dag.inputs` | Digests of the calls this one depends on | 1.6% | Kept |
| `dag.output` | Digest of the call that produced the returned object; the UI uses it to show `foo().bar()` instead of the `a().b().c()` it returned (`DB.Simplify`) | 1.0% | Kept |
| `dag.content_preferred_digest` | Identity based on content where known; the source says not to read it as a cache decision ([attrs.go#L32](https://github.com/dagger/dagger/blob/v1.0.0-beta.20/engine/telemetryattrs/attrs.go#L32)) | 1.5% | Dropped |
| `dag.cached`, `dag.pending`, `dag.partial` | Lazy evaluation state | small | Kept |
| `execution.content_preferred_digest` | On `exec.run`: the content-based identity of the call that owns the exec | small | Kept |

#### Cache evidence (`dagger.io/cache.*`)

The cache contract is documented in detail at
[`engine/telemetryattrs/attrs.go#L523`](https://github.com/dagger/dagger/blob/v1.0.0-beta.20/engine/telemetryattrs/attrs.go#L523).
These are stamped only on call spans that recorded a cache decision, and
`cache.contract` gives the version (`1`).

| Attribute | Meaning | Kept? |
|---|---|---|
| `cache.outcome` | `hit`: reused a cached result. `executed`: miss, the resolver ran. `joined`: an identical call was already running, so this one waited and shared its result. `uncached`: marked never-cache, no lookup and no sharing. In a CI run: 2,935 executed, 1,250 hit, 321 uncached, 2 joined | Kept |
| `cache.hit.route` | How a hit was found: `recipe` (exact recipe digest), `digest` (equivalent content digest), `structural` (same operation over equivalent inputs). CI run: 823 recipe, 427 structural | Kept |
| `cache.hit.remote`, `cache.hit.merged_at_unix_ns` | The value came from the remote (Cloud) cache, and when it was merged | Kept |
| `cache.miss.saw_expired` | A match existed but had expired | Kept |
| `cache.miss.incompatible_candidates` | Matches existed but needed secrets or sockets this session hadn't loaded | Kept |
| `cache.miss.unknown_input` | Index into `structural_inputs` of an input the cache had never seen, which skipped the structural lookup. Not the same as "first run" | Kept, but the list it indexes is dropped |
| `cache.miss.producer_released`, `cache.miss.requirements_grew` | A hit was chosen but couldn't be served | Kept |
| `cache.pairing_digest` | The operation and its literal arguments, over whatever inputs and in whatever session. Equal values across runs are the same call, so this is the key for "why did this hit yesterday and miss today" | Kept (1.1%) |
| `cache.self_digest` | Like `pairing_digest` but including per-session implicit inputs; usually identical | Dropped (1.1%) |
| `cache.structural_inputs` | Ordered input digests the structural lookup keys on | Dropped (1.8%) |
| `cache.output.content_digest` | Recorded content digest of the result | Kept |
| `cache.result.id`, `cache.deps` | The result's engine-local number and its dependencies' numbers, with `dagger.io/engine.cache` naming the cache: an ownership graph of the cache | Dropped (3.4%) |
| `cache.lookup.at_unix_ns` | When the lookup chose its candidates, for ordering against remote merges | Dropped (1.2%) |
| `cache.type`, `cache.parts`, `cache.retained`, `cache.retention.expires`, `cache.expires`, `cache.replacements` | The cache entry's type, stored parts and lifetime | Kept |
| `cache.offer.*` | A part installed from a Cloud cache offer | Kept |

#### Profiler (`wcprof.*`)

The engine mirrors its offline wall-clock profiler ([`engine/wcprof`](https://github.com/dagger/dagger/tree/v1.0.0-beta.20/engine/wcprof))
onto OTel spans ([attrs.go#L414](https://github.com/dagger/dagger/blob/v1.0.0-beta.20/engine/telemetryattrs/attrs.go#L414)).

| Attribute | Meaning | Kept? |
|---|---|---|
| `wcprof.exec.argv` | The command an `exec.processRun` ran, as a JSON array string | Kept |
| `wcprof.work_type` | `user`, `engine` or `external`: whose time this was | Kept |
| `wcprof.op.kind` | `exec`, `exec_phase`, `lazy`, `call_exec`, ... | Kept |
| `wcprof.parent` | Causal parent when the OTel parent is only the UI parent | Kept |
| `wcprof.engine_span` | `true` on every engine span, so the profiler can count them against `wcprof.session_span_count` and refuse incomplete traces. Filtering already makes every trace incomplete | Dropped (18.7% of bytes) |

Span links carry the rest: `dagger.io/link.purpose = wait` with
`wcprof.wait.reason`, `wcprof.wait.start_unix_ns` and `wcprof.wait.end_unix_ns`
(5,075 in a CI run), plus `cause` and `error_origin`. Links are untouched.

#### Everything else

`check.name` and `check.passed` (check spans); `module.function.name`,
`module.ref`, `module.caller.*` (module function calls); `engine.cache` and
`engine.session` (resource). All kept.

### What helps analysis

#### Performance

- **`exec.processRun` with `wcprof.exec.argv`** is where user time goes,
  named by command. `exec.containerStart` is engine overhead before the
  process starts.
- **`/runtime` execs dominate cached runs.** In the 30s cached CI run, the
  modules' `/runtime` ran 231 times for 90s of process time, against 8
  workload execs totalling 11s. Each module function call starts its module's
  runtime container, cached or not.
- **Wait links** explain where a span spent time blocked and on what. `dagui`
  turns them into a self-time breakdown
  ([`dagql/dagui/selftime.go`](https://github.com/dagger/dagger/blob/v1.0.0-beta.20/dagql/dagui/selftime.go)):
  time spent working vs waiting on another op, resolved through chains of
  waits.
- **`resume <field> (...)`** spans are lazy evaluation, the point where
  deferred work was forced and by whom.
- **`Artifact.value` and `Address.container`** under a check are input
  resolution; `uploading <path>` and `copy` beneath them are host uploads.
- **`joined` outcomes** mark work a check waited on that another check
  started, so its wall time isn't its own cost.

#### Cache

- Per call: `cache.outcome`, then `cache.hit.route` or `cache.miss.*` for
  why.
- Across runs: `cache.pairing_digest` matches the same call between runs;
  comparing `dag.digest` (changes when inputs change) for two runs with the
  same pairing digest shows which input moved.
- Per check: a check was a cache hit when no non-`/runtime` process ran under
  it:

  ```
  {name = "exec.processRun" && span."wcprof.exec.argv" != "[\"/runtime\"]"} << {span."dagger.io/check.name" != nil}
  ```

### What the collector does

All in `k8s/platform/monitoring-system/values.yaml` under
`opentelemetry-collector.alternateConfig`. Each pipeline gets its own copy of
every span, so filtering one doesn't affect the others.

| Pipeline | Processing | Output |
|---|---|---|
| `traces` | `filter/dagger-trace-noise`, `transform/dagger-trace-attributes` | Tempo |
| `traces/span-metrics` | Keeps only check and module function spans, drops resource attributes | `traces_span_metrics_*` in Prometheus, by check name, function, cache outcome and pass/fail |
| `traces/dagger-ci-runs` | Keeps only CI `dagger check` root spans | `dagger_ci_runs_calls_total`, one series per CI run |

`filter/dagger-trace-noise` drops the debug spans (matched by name, and only
when the span has no `dag.digest`, so a real API call can't match) and
`dagql.publishResult` spans unless they errored. All of these are leaves, so
nothing is left without a parent. `transform/dagger-trace-attributes` removes
the attributes marked Dropped above.

Measured on the 30s CI trace:

| Step | Size | Saved |
|---|---|---|
| Original (128,750 spans) | 26.2 MB | |
| Drop debug spans | 9.0 MB | 65.6% |
| Also drop `dagql.publishResult` | 8.0 MB | 69.4% |
| Also strip attributes | 5.1 MB (13,742 spans) | 80.6% |

### Options

#### A. Tempo, trimmed by the collector (current)

What's in place. Traces keep Dagger's full structure, minus noise. Grafana
shows them as generic trace waterfalls: every passthrough and internal span
is visible, and there's no notion of hidden subtrees, rollups or call chains.

- Simple, no custom code, stays inside the existing Grafana/Tempo stack.
- TraceQL metrics over the trimmed spans power the dashboard.
- Grafana's trace view is noisy for Dagger: ~14k spans per cached CI run, of
  which Dagger's UI would show a few hundred.
- Large executed runs may still exceed 16 MB. The limit is the querier's
  gRPC send size to the frontend
  (`querier.frontend_worker.grpc_client_config.max_send_msg_size`) and the
  server's receive size (`server.grpc_server_max_recv_msg_size`). Raising
  both is possible, at a memory cost whenever such a trace is opened.

#### B. Custom Dagger UI that receives spans directly

A service that accepts OTLP from Dagger (CLI and engine point at it instead
of the collector, or the collector forwards to it), loads spans into a
`dagui.DB` per trace, keeps what the UI would display, and forwards a slimmed
copy to the collector or Tempo.

- Reuses Dagger's own visibility rules instead of reimplementing them: hidden
  internal subtrees, passthrough splicing, encapsulation revealed on failure,
  rollups, call simplification, self time.
- Can decode `dag.call` for arguments and call chains, so that attribute
  would be kept again.
- Whole-trace context makes "drop the hidden subtree" possible. It has to
  wait for a trace (or a span's ancestry) to be complete, since children
  arrive first; a run can last many minutes.
- It is a stateful service with its own storage, retention and UI to build
  and keep working. `dagui` is internal to the Dagger repo with no API
  stability promise, and the 1.0 beta is still moving.
- Tempo and the dashboard keep working on the forwarded copy. Anything the
  service drops is gone from Tempo too, so the dashboard's queries must only
  depend on spans it forwards (the exec spans and check spans above).

#### C. Subtree-aware collector processor

A custom processor built into the collector with the OpenTelemetry Collector
Builder (OCB), behind the `groupbytrace` processor (which holds spans until a
trace has been quiet for a while), applying the same hidden-subtree rules as
B before export to Tempo. No UI.

- Shrinks what Tempo stores to roughly what Dagger's UI shows, without a new
  storage system.
- A custom collector image to build and track against upstream releases, and
  `groupbytrace` must buffer whole traces in memory (tens of MB each, a few
  concurrent).
- Keeping failures and `ui.reveal` spans visible means reimplementing part of
  `dagui`'s rules, or importing `dagui` into the processor.
- Span links (wait edges, error origins) can point at dropped spans.

#### D. Fix it at the source

The debug spans are the bulk of the volume and look unintentional: a
debugging aid left on unconditionally. Reporting it upstream (or asking for it
behind a debug flag) would remove most of the volume for every Dagger user.
Separately, resolving `dag://nix/devenv/ci-toolchain` 528 times per run is
real engine work; resolving it once per module would also help, either
upstream or by how this repo wires the setting.

#### E. Dagger Cloud

Dagger Cloud's UI is the thing being replaced. It needs the hosted service
and is out of scope here, but it's the reference for what B would show.

#### Comparison

| | A (current) | B (custom UI) | C (processor) | D (upstream) |
|---|---|---|---|---|
| Work to build | Done | Large | Medium | Small (report) |
| Ongoing upkeep | Low | High | Medium | None |
| Size reduction | ~81% | Highest | High | ~66% alone |
| Dagger-like view | No | Yes | No | No |
| `dag.call` useful | No | Yes | No | No |
| Risk | Low | Tracks `dagui` internals | Custom collector | Depends on upstream |

A and D combine well. B is the only one that gets a Dagger-style view.

### Starting points for a custom UI

These are the pieces of the Dagger repo a custom UI would build on.

- **Ingest.** `telemetry.SpansFromPB` in
  [otel-go `transform.go`](https://github.com/dagger/otel-go/blob/132b1dd82eb2/transform.go)
  turns OTLP `ResourceSpans` into `sdktrace.ReadOnlySpan`, which
  `DB.ExportSpans` takes
  ([`dagql/dagui/db.go#L596`](https://github.com/dagger/dagger/blob/v1.0.0-beta.20/dagql/dagui/db.go#L596)).
  The engine itself does this when importing traces
  ([`engine/telemetry/traceimport.go`](https://github.com/dagger/dagger/blob/v1.0.0-beta.20/engine/telemetry/traceimport.go)).
  `NewDB` is at [db.go#L293](https://github.com/dagger/dagger/blob/v1.0.0-beta.20/dagql/dagui/db.go#L293);
  `LogExporter` ([#L609](https://github.com/dagger/dagger/blob/v1.0.0-beta.20/dagql/dagui/db.go#L609))
  and `MetricExporter` ([#L788](https://github.com/dagger/dagger/blob/v1.0.0-beta.20/dagql/dagui/db.go#L788))
  take logs and metrics the same way.
- **Visibility and layout.** `DB.RowsView(opts)` and `RowsView.Rows(opts)`
  ([`types.go#L150`](https://github.com/dagger/dagger/blob/v1.0.0-beta.20/dagql/dagui/types.go#L150))
  produce the rows the TUI renders, with `FrontendOpts` controlling verbosity
  and debug. `walk_lazy.go` builds only the parts of the tree a view shows.
- **Call rendering.** `DB.Simplify` ([db.go#L1741](https://github.com/dagger/dagger/blob/v1.0.0-beta.20/dagql/dagui/db.go#L1741))
  picks a call's highest-level creator. `cmd/dump-id`
  ([`main.go`](https://github.com/dagger/dagger/blob/v1.0.0-beta.20/cmd/dump-id/main.go))
  decodes base64 call protobufs; `callpbv1` is in
  [`dagql/call/callpbv1`](https://github.com/dagger/dagger/blob/v1.0.0-beta.20/dagql/call/callpbv1/call.proto).
- **Self time.** [`selftime.go`](https://github.com/dagger/dagger/blob/v1.0.0-beta.20/dagql/dagui/selftime.go),
  driven by wait links.
- **Existing renderers.** The TUI can be served over HTTP
  (`DAGGER_TUI_CONSOLE=<addr>`,
  [`dagql/idtui/frontend_console.go`](https://github.com/dagger/dagger/blob/v1.0.0-beta.20/dagql/idtui/frontend_console.go#L28)),
  and the engine renders trace reports from its own per-client telemetry
  store for its LLM builtins
  ([`core/trace_inspect.go`](https://github.com/dagger/dagger/blob/v1.0.0-beta.20/core/trace_inspect.go)).
  Both show what `dagui` output looks like rendered.
- **Logs.** Exec output is in Loki with `trace_id` and `span_id` (see
  [Finding a run in Grafana](#finding-a-run-in-grafana)). Dagger's UI shows logs inline per span, and
  passthrough spans move theirs to the parent.

Gotchas:

- Spans arrive child-first, and only finished spans reach an OTLP exporter.
  A UI fed by the collector sees nothing of a run until its spans finish;
  live progress needs the CLI's live spans.
- `dagui` keys a lot off `dag.call`, `dag.digest` and `dag.output`. A UI fed
  from Tempo after trimming won't have `dag.call`.
- Things this document assumes about `dagui` were read from source, not run.
  A small harness that loads one exported trace into `dagui.DB` and prints
  `Rows` would confirm them before building anything bigger.

### Measuring it again

To repeat the measurements without loading Tempo's query frontend:

```sh
kubectl -n monitoring-system port-forward svc/monitoring-system-tempo 3200:3200
curl -H 'Accept: application/protobuf' -o trace.pb \
  "http://localhost:3200/querier/api/traces/<trace id>?mode=all"
```

The response is a `tempopb.TraceByIDResponse`; its field 1 is wire-compatible
with OTLP `TracesData`, so `opentelemetry-proto`'s Python or Go types decode
it. Pick a short run: a full 11-minute CI trace takes gigabytes of Tempo
memory to assemble.
