# VictoriaMetrics

Long-term metrics store. Prometheus keeps 7d of raw data and remote_writes
everything here; VictoriaMetrics downsamples it and keeps it for 4y.

```
Prometheus (7d raw) --remote_write--> vmsingle
                                        -relabelConfig      drop go_*/process_*
                                        -streamAggr.config  downsample to 5m
                                        -> TSDB (retentionPeriod: 4y)
```

## Stream aggregation

[Stream aggregation](https://docs.victoriametrics.com/stream-aggregation/)
runs in vmsingle itself. Rules are in `files/stream-aggr-config.yml`:

| Metrics | Outputs |
|---|---|
| `*_total`, `*_bucket`, `*_sum`, `*_count` (counters) | `total` |
| everything else (gauges, summary quantiles) | `avg`, `min`, `max` |

Aggregation is per-series (no `by`/`without`), so only resolution changes.
Aggregated series are named `<metric>:5m_<output>`, e.g.
`node_cpu_seconds_total:5m_total`. `-streamAggr.dropInput=true` means no raw
samples are stored. Grafana's VictoriaMetrics datasource uses
`timeInterval: 5m` to match.

Verify after deploy:

```sh
VM=http://victoria-metrics-server.victoria-metrics.svc:8428
curl -s $VM/api/v1/label/__name__/values | grep -c ':5m_'      # > 0
curl -s "$VM/api/v1/query?query=count({__name__=~\"go_.*\"})"  # empty
```

## Backups

`templates/backup-cronjob.yaml` runs `vmbackup` daily at 03:15. It mounts the
server's Longhorn PVC read-only (pinned to the same node via `podAffinity`),
takes a snapshot through `/snapshot/create`, and writes it incrementally to
`/storage/nas/backups/victoriametrics/latest` on borg-2 over mTLS NFS.
History comes from the `nas-backups-weekly` restic job
(`nix/hosts/borg-2/nas-backups.nix`), which ships that directory offsite.

Since vmbackup is the backup path, the server PVC is in Longhorn's
`ephemeral` recurring-job group (trim only) rather than `backup`.

Run a backup by hand:

```sh
kubectl -n victoria-metrics create job --from=cronjob/victoria-metrics-backup vm-backup-manual
kubectl -n victoria-metrics logs job/vm-backup-manual -f
```

### Restore

1. To restore an older version, first restore it from restic into
   `/storage/nas/backups/victoriametrics` on borg-2.
2. Stop the server:
   `kubectl -n victoria-metrics scale sts victoria-metrics-server --replicas=0`
3. Run `vmrestore` against the server PVC:
   ```sh
   kubectl -n victoria-metrics run vmrestore --rm -i --restart=Never \
     --image=victoriametrics/vmrestore:v1.153.0 --overrides='{"spec":{
       "securityContext":{"runAsUser":65534,"runAsGroup":65534,"fsGroup":65534},
       "containers":[{"name":"vmrestore","image":"victoriametrics/vmrestore:v1.153.0",
         "args":["-storageDataPath=/storage","-src=fs:///backup/latest"],
         "volumeMounts":[{"name":"data","mountPath":"/storage"},
                         {"name":"backup","mountPath":"/backup","readOnly":true}]}],
       "volumes":[
         {"name":"data","persistentVolumeClaim":{"claimName":"server-volume-victoria-metrics-server-0"}},
         {"name":"backup","persistentVolumeClaim":{"claimName":"victoria-metrics-backups"}}]}}'
   ```
4. Start the server again with `--replicas=1`.

## Upgrading PVC labels

PVC labels are set through `server.persistentVolume.extraLabels`, which the
chart renders into the StatefulSet's `volumeClaimTemplates`. That field is
immutable, and it only applies to newly created PVCs. To change the labels,
orphan-delete the StatefulSet (pods and PVC stay), relabel the existing PVC,
then let Argo CD recreate the StatefulSet:

```sh
kubectl -n victoria-metrics delete sts victoria-metrics-server --cascade=orphan
kubectl -n victoria-metrics label pvc server-volume-victoria-metrics-server-0 \
  recurring-job.longhorn.io/source=enabled \
  recurring-job-group.longhorn.io/ephemeral=enabled
argocd app sync victoria-metrics
```
