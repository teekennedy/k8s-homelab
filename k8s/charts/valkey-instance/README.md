# valkey-instance

A Helm **library** chart rendering one hardened Valkey instance. It renders
nothing on its own; a consumer includes `valkeyInstance.all`. Used by
`k8s/platform/forgejo`, `k8s/platform/auth-system` and `k8s/apps/openhands`.

## Topologies

- **`sentinel`** (default): a replication set of `replicas` pods (odd, ≥ 3),
  each running Valkey, a co-located Sentinel and a redis_exporter. AOF on a
  `longhorn-strict-local` volume per pod. Clients connect through Sentinel.
- **`standalone`**: one in-memory pod (no AOF, no RDB) with an exporter. For
  stores whose loss is cheap, such as a session lock store.

Both render a Secret (unless `auth.existingSecret`), ConfigMap, Service(s),
StatefulSet, NetworkPolicy and PodMonitor; `sentinel` also renders a PDB.

## Design

**Storage follows the pods, not the other way round.** A replication set's
volumes are `longhorn-strict-local` (one Longhorn replica, on the pod's node),
and its pods only schedule onto `node.msng.to/cnpg-storage: "true"` nodes, the
same pattern as the CNPG clusters. Valkey's own replication is the redundancy.
With exactly as many storage nodes as pods, a pod evicted by a drain has
nowhere else to go, so it waits for its node; its volume detaches and Longhorn
releases the drain. The previous setup (`longhorn-rc1`, best-effort locality,
no node constraint) let an evicted pod restart on a node with no Longhorn disk,
reattach to its only replica remotely, and block the drain until kured's 30m
timeout force-rebooted the node out from under it.

The tradeoff, shared with CNPG: if a storage node is lost for good, its pod
stays Pending until its PVC is deleted (the pod then resyncs from the primary).

**Role discovery retries.** At startup a pod asks the peer Sentinels for the
current primary and joins it as a replica. If none answers it treats the start
as a cold bootstrap, where ordinal 0 becomes primary. A new pod's connections
are refused for about a second until NetworkPolicy admits its IP, so discovery
keeps asking for 20s before concluding that; one failed round used to boot
ordinal 0 as a second primary. A full cold start is 20s slower as a result.

**No passwords on command lines.** The entrypoint writes passwords and
`maxmemory` to a mode-0600 file on an in-memory emptyDir that includes the
ConfigMap's `valkey.conf`; probes and hooks pass `REDISCLI_AUTH` instead of
`valkey-cli -a`.

**`maxmemory` is the memory request**, so Valkey starts evicting before the
kernel OOM-kills the container at the limit.

**Default-deny NetworkPolicy.** Ingress from `networkPolicy.clients` (required)
and the peers, Prometheus to the exporter; egress only DNS and the peers.

**Hardened pods**: RuntimeDefault seccomp, all capabilities dropped,
read-only root filesystem, no service account token, no service links.

**Library, not a subchart**, to match `oauth2-proxy-instance` and keep values
at the consumer's top level. In a library chart `.Files` reads the
*consumer's* files, so the entrypoint scripts are `define` blocks in
`templates/_config.tpl` rather than files.

## Usage

```yaml
# Chart.yaml
dependencies:
  - name: valkey-instance
    version: 0.1.0
    repository: file://../../charts/valkey-instance
```

```yaml
# templates/valkey-instance.yaml
{{- include "valkeyInstance.all" . }}
```

```yaml
# values.yaml
valkeyInstance:
  topology: sentinel          # or standalone
  name: valkey                # pods <name>-N, Services <name>-headless / <name>-sentinel
                              # (sentinel) or <name> (standalone), ConfigMap <name>-config
  component: valkey           # app.kubernetes.io/component; defaults to name
  replicas: 3                 # sentinel only
  masterName: mymaster        # Sentinel monitor name
  image: valkey/valkey:9-alpine@sha256:...
  exporterImage: oliver006/redis_exporter:...
  labelsTemplate: ""          # optional named template for the consumer's common labels
  maxmemoryPolicy: allkeys-lru
  metrics:
    enabled: true
  auth:
    existingSecret: ""        # set to use a Secret the consumer owns
    secretName: ""            # Secret to create; defaults to name. Hex, 32 chars.
    passwordKey: valkey-password
    sentinelPasswordKey: sentinel-password
  persistence:                # sentinel only
    storageClass: longhorn-strict-local
    size: 1Gi
  nodeSelector:               # sentinel default: node.msng.to/cnpg-storage: "true"
  resources:
    valkey: {}                # defaults: 100m / 256Mi request, 512Mi limit
    sentinel: {}
    exporter: {}
  networkPolicy:
    clients:                  # required: NetworkPolicyPeers allowed to connect
      - podSelector:
          matchLabels:
            app.kubernetes.io/name: my-app
```

The selector labels are fixed at `app.kubernetes.io/name: valkey` plus
`app.kubernetes.io/component`, so two instances in one namespace need distinct
`name`s and `component`s.

## Changing an immutable StatefulSet field

`volumeClaimTemplates`, `selector`, `serviceName` and `podManagementPolicy`
cannot be updated in place, and the StatefulSet deliberately has no ArgoCD
`Replace=true` (that would restart every pod at once on any change). To change
one, e.g. the storage class:

1. Merge the change; the ArgoCD sync fails on the StatefulSet.
2. `kubectl -n <ns> delete statefulset <name> --cascade=orphan`. The pods keep
   running; ArgoCD recreates the StatefulSet, which adopts them and rolls
   them one at a time (each keeps its existing PVC).
3. For each ordinal in turn, wait until the set is healthy (one primary, all
   replicas `master_link_status:up`), then
   `kubectl -n <ns> delete pvc data-<name>-N --wait=false` and
   `kubectl -n <ns> delete pod <name>-N`. The pod returns with a new PVC from
   the template and resyncs from the primary; its preStop hands off the
   primary role first if it held it.
