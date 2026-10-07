# VictoriaMetrics: long-term metrics storage

VictoriaMetrics (`k8s/platform/victoria-metrics`) is the long-term store for
cluster metrics. Prometheus (`k8s/platform/monitoring-system`) stays the
high-resolution, short-term store: it retains ~7 days of raw scrapes
(`retention: 7d`, `retentionSize: 160GiB`) and remote_writes everything to
VictoriaMetrics at `http://victoria-metrics-server.victoria-metrics.svc:8428/api/v1/write`.

```
Prometheus (7d raw, every scrape_interval)
   |  remote_write (full resolution, unchanged)
   v
VictoriaMetrics (vmsingle)
   |  -relabelConfig   drop go_*/process_* self-monitoring metrics
   |  -streamAggr.config  downsample everything else to ~5m
   v
on-disk TSDB (retentionPeriod: 4y)
```

## Stream aggregation

VictoriaMetrics Community Edition supports [stream
aggregation](https://docs.victoriametrics.com/stream-aggregation/) directly
in the server binary (`-streamAggr.config`) -- no vmagent or Enterprise
`-downsampling.period` needed. There was no vmagent in this cluster already
(Prometheus remote_writes straight to vmsingle), so aggregation runs in
vmsingle itself rather than introducing a new component.

Rules live in `k8s/platform/victoria-metrics/files/stream-aggr-config.yml`,
mounted via a ConfigMap (`templates/stream-aggr-configmap.yaml`) at
`/etc/vm/stream-aggr/config.yml`. Four rules, matched by Prometheus/OpenMetrics
naming convention, cover every possible metric name:

| Metric shape | Match | Output(s) | Why |
|---|---|---|---|
| Counters (`*_total`) | `{__name__=~".+_total"}` | `total` | Preserves counter-reset semantics, so `rate()`/`increase()` behave the same against the aggregated series as against the raw one. |
| Histogram buckets (`*_bucket`) | `{__name__=~".+_bucket"}` | `total` | Buckets are counters too; `histogram_quantile()` keeps working. |
| Histogram/summary `_sum`/`_count` | `{__name__=~".+_(sum\|count)"}` | `total` | Also counters. |
| Everything else (gauges, summary quantiles) | `{__name__!~".+_(total\|bucket\|sum\|count)"}` | `avg`, `min`, `max` | `avg` for typical dashboard use; `min`/`max` so a brief spike between two 5m samples isn't averaged away once a dashboard looks further back than Prometheus's 7d window. |

No rule sets `by`/`without`, so aggregation is per-series (grouped by the
full original label set) -- it changes sample *resolution* only, never which
series exist or their label cardinality.

`-streamAggr.dropInput=true` is also set, so nothing bypasses these rules:
VictoriaMetrics never ends up holding a second, full-resolution copy of what
Prometheus already stores. Because the four match regexes are a complete
partition of every metric name, this shouldn't ever drop real data -- but if
a future metric genuinely doesn't match any rule, it goes missing from
VictoriaMetrics (visible as "No data" in Grafana) rather than silently
reverting to unbounded raw storage, which is the cost problem this exists to
avoid.

### Metrics excluded entirely

`server.relabel` drops `go_*` and `process_*` (Go runtime/process
self-monitoring metrics that every Go-based component in the cluster
exports) before they're considered for aggregation or storage at all.
These are only useful for live troubleshooting -- GC pressure, FD leaks, RSS
growth -- which Prometheus's 7-day raw window already covers; a multi-year
history of them isn't useful and isn't worth the series count. Nothing else
is dropped: kube-state-metrics, node-exporter, cAdvisor, and all application
metrics are aggregated and retained normally. (The kubelet ServiceMonitor's
own `metricRelabelings` in `monitoring-system/values.yaml`, which drop a
handful of extremely high-cardinality apiserver/etcd bucket series, are a
separate, pre-existing Prometheus-side scrape filter -- unrelated to this
change, and untouched by it.)

### Verifying aggregation after deploy

```sh
# Confirm the flags landed
kubectl -n victoria-metrics exec sts/victoria-metrics-server -- \
  wget -qO- localhost:8428/flags | grep streamAggr

# Aggregated series carry a ":5m_<output>" suffix. Compare to Prometheus,
# which should NOT have this suffix on the equivalent metric.
curl -s 'https://prometheus.msng.to/api/v1/label/__name__/values' | grep ':5m_'  # expect none
curl -s -u <grafana-or-direct-access> \
  'http://victoria-metrics-server.victoria-metrics.svc:8428/api/v1/label/__name__/values' \
  | grep ':5m_' | head  # expect lots, e.g. up:5m_avg, node_cpu_seconds_total:5m_total

# Confirm raw (non-aggregated) copies are NOT accumulating in VictoriaMetrics
curl -s 'http://victoria-metrics-server.victoria-metrics.svc:8428/api/v1/query?query=count({__name__=~"go_.*"})'
# expect an empty result (dropped by relabel)

# Watch actual resolution over a day or two
curl -s 'http://victoria-metrics-server.victoria-metrics.svc:8428/api/v1/query_range?query=up:5m_avg%7Bjob%3D"victoria-metrics-single-server"%7D&start=-1h&step=1m'
# expect one distinct value approximately every 5m, not every scrape_interval
```

Grafana's VictoriaMetrics datasource `jsonData.timeInterval` is set to `5m`
(`monitoring-system/values.yaml`) to match -- leaving it at the old `30s`
would make Grafana request a step finer than the data actually has,
rendering long-range panels as mostly gaps.

## Backups

**Daily, via `vmbackup`** (community edition, not the Enterprise-only
`vmbackupmanager`) -- `k8s/platform/victoria-metrics/templates/backup-cronjob.yaml`,
schedule `15 3 * * *`.

VictoriaMetrics' own docs recommend running `vmbackup` as a sidecar in the
same pod as the server, since it needs direct filesystem access to the same
`-storageDataPath` (it reads TSDB part files off disk, not over HTTP). The
official `vmbackup` image is a from-scratch image with no shell, so a
long-lived sidecar loop isn't possible; a CronJob is the closest equivalent,
pinned onto the same node as the running `victoria-metrics-server` pod via
`podAffinity` (`topologyKey: kubernetes.io/hostname`) so the two pods can
both mount the StatefulSet's Longhorn PVC
(`server-volume-victoria-metrics-server-0`) at once -- Longhorn's
ReadWriteOnce restriction is per-node, not per-pod.

The Job:
1. Calls VictoriaMetrics' own `/snapshot/create` API (via `-snapshot.createURL`)
   to get a consistent point-in-time snapshot -- not a raw copy of the live,
   actively-written data directory.
2. Backs up that snapshot incrementally to `fs:///backup-dest/latest`, which
   resolves (via the `victoria-metrics-backups` PV/PVC) to
   `/storage/nas/backups/victoriametrics/latest` on the NFS server
   (`borg-2.msng.to`, mTLS-protected, following the same static-PV pattern
   as `k8s/apps/syncthing` and `k8s/foundation/s3-proxy`).
3. Deletes the snapshot via `/snapshot/delete`.

### Retention strategy

`vmbackup`'s backup format is incremental against whatever already exists at
`-dst`: unchanged TSDB parts aren't re-uploaded on subsequent runs. That
makes a single, continuously-refreshed `latest` directory cheap to maintain
daily, but it only ever holds the *most recent* backup -- no dated history
by itself. Independently maintaining N dated directories would **not** get
the same incremental benefit on a plain filesystem destination the way it
would via `-origin` server-side copy against certain object storage
backends, so that trade was deliberately not taken here.

Historical, retained versions instead come from the **existing**
`nas-backups-weekly` restic job (`nix/hosts/borg-2/nas-backups.nix`), which
already snapshots everything under `/storage/nas/backups` -- including
`victoriametrics/latest` now -- offsite to S3 with `--keep-weekly 4
--keep-monthly 12`. No changes were needed to that job: its `paths` already
covers the whole `backups` tree. This means VictoriaMetrics backups get ~4
weeks of weekly granularity, then ~12 months of monthly granularity, for
free, from infrastructure that already exists and is already exercised by
the Postgres/Longhorn backup paths.

### Restore procedure

Restores are a **manual, deliberate operation** -- there's no automatic
restore path, by design.

1. Pick the version to restore: the live `/storage/nas/backups/victoriametrics/latest`
   (most recent daily backup), or an older weekly/monthly snapshot pulled
   out of the restic repo (`restic -r
   s3:s3.us-west-2.amazonaws.com/missingtoken-backup-us-west-2/restic/nas-backups
   restore <snapshot-id> --target /tmp/vm-restore --include
   /storage/nas/backups/victoriametrics`).
2. Scale down the VictoriaMetrics StatefulSet so nothing is writing to the
   data volume during restore:
   ```sh
   kubectl -n victoria-metrics scale statefulset victoria-metrics-server --replicas=0
   ```
3. Run `vmrestore` against the **same PVC** the server uses, with the
   backup directory from step 1 as `-src`. The quickest way is a one-off Job
   or `kubectl run` pod mounting `server-volume-victoria-metrics-server-0`
   read-write and the `victoria-metrics-backups` PVC read-only:
   ```sh
   kubectl -n victoria-metrics run vmrestore --rm -i --tty \
     --image=victoriametrics/vmrestore:v1.153.0 \
     --overrides='{
       "spec": {
         "containers": [{
           "name": "vmrestore",
           "image": "victoriametrics/vmrestore:v1.153.0",
           "args": ["-storageDataPath=/storage", "-src=fs:///backup-src/latest"],
           "volumeMounts": [
             {"name": "data", "mountPath": "/storage"},
             {"name": "backup", "mountPath": "/backup-src", "readOnly": true}
           ]
         }],
         "volumes": [
           {"name": "data", "persistentVolumeClaim": {"claimName": "server-volume-victoria-metrics-server-0"}},
           {"name": "backup", "persistentVolumeClaim": {"claimName": "victoria-metrics-backups"}}
         ]
       }
     }'
   ```
   `vmrestore` behaves like `rsync --delete` against `-storageDataPath`: any
   existing files there are replaced or removed to match the backup, so the
   volume doesn't need to be emptied first. VictoriaMetrics must be stopped
   for the duration (step 2) -- `vmrestore` doesn't coordinate with a
   running server.
4. Scale the StatefulSet back up:
   ```sh
   kubectl -n victoria-metrics scale statefulset victoria-metrics-server --replicas=1
   ```
5. Verify: check `/api/v1/query?query=vm_app_version` responds, spot-check a
   known series/time range in Grafana, and confirm `vm_data_size_bytes`
   looks like the backup's size, not empty.

### Verifying the backup after deploy

```sh
kubectl -n victoria-metrics get cronjob victoria-metrics-backup
kubectl -n victoria-metrics create job --from=cronjob/victoria-metrics-backup victoria-metrics-backup-manual
kubectl -n victoria-metrics logs job/victoria-metrics-backup-manual -f
# Confirm files landed on the NFS destination (adjust node/path for your access):
ssh borg-2 ls -la /storage/nas/backups/victoriametrics/latest
```

## Known issue found but not fixed here

`victoria-metrics-single.server.persistentVolume` has carried a `labels:`
key for the Longhorn recurring-job groups since this chart was first added,
but the chart only reads `.extraLabels` for that purpose -- `labels` is
silently ignored. In practice, the VictoriaMetrics PVC has never actually
been a member of Longhorn's `backup` or `ephemeral` recurring-job groups.
Fixing the key name is straightforward, but `volumeClaimTemplates` on an
already-created StatefulSet is immutable in Kubernetes, so shipping that fix
bundled with unrelated changes risks the whole `helm upgrade`/ArgoCD sync
being rejected. Treat it as a separate change: recreate the StatefulSet with
`kubectl delete statefulset victoria-metrics-server --cascade=orphan`
(preserving the PVC) immediately before the next helm upgrade that includes
the fix, or patch the live PVC's labels directly out-of-band in the
meantime.

With the vmbackup CronJob now providing an application-consistent backup,
the recommended fix is to put the PVC in the `ephemeral` group (daily
filesystem-trim only, matching Prometheus/Alertmanager/Loki's PVCs) rather
than `backup` -- Longhorn's own block-level snapshot of a live, uncoordinated
TSDB is exactly the "copying the live database directory" approach
VictoriaMetrics' docs advise against, and would now be redundant with
vmbackup besides.
