{{/*
The ConfigMap: valkey.conf plus the entrypoint scripts. Scripts are defined
here rather than shipped as files because, in a library chart, .Files reads
the consuming chart's files, not this one's.
Takes (dict "v" $settings "ctx" $).
*/}}
{{- define "valkeyInstance.configMap" -}}
{{- $v := .v -}}
apiVersion: v1
kind: ConfigMap
metadata:
  name: {{ $v.name }}-config
  namespace: {{ .ctx.Release.Namespace }}
  labels:
    {{- include "valkeyInstance.labels" . | nindent 4 }}
data:
  {{- include "valkeyInstance.configData" $v | nindent 2 }}
{{- end }}

{{/*
The ConfigMap's data, also hashed into the pod template so a change rolls the
pods. Takes the settings dict.
*/}}
{{- define "valkeyInstance.configData" -}}
# https://raw.githubusercontent.com/valkey-io/valkey/9.0/valkey.conf
# Passwords and maxmemory are appended at startup by valkey-entrypoint.sh, in a
# file that includes this one.
valkey.conf: |
  bind 0.0.0.0
  port 6379
  protected-mode yes
  maxmemory-policy {{ .maxmemoryPolicy }}
  {{- if .sentinel }}
  appendonly yes

  # fsync writes to the Append Only File every second (default)
  appendfsync everysec

  # Embed the RDB (database backup) as a preamble to the AOF (write append log),
  # and disable redundant separate RDB file.
  save ""
  aof-use-rdb-preamble yes

  dir /data
  {{- else }}

  # In memory only: nothing is written to disk.
  save ""
  appendonly no
  dir /run/valkey
  {{- end }}
valkey-entrypoint.sh: |
  {{- include "valkeyInstance.valkeyEntrypoint" . | nindent 2 }}
{{- if .sentinel }}
sentinel-entrypoint.sh: |
  {{- include "valkeyInstance.sentinelEntrypoint" . | nindent 2 }}
{{- end }}
{{- end }}

{{/*
Valkey server entrypoint. Takes the settings dict.
*/}}
{{- define "valkeyInstance.valkeyEntrypoint" -}}
#!/bin/sh
# Writes the runtime config (passwords, maxmemory and, in a replication set,
# the role) to a private file that includes valkey.conf, then starts Valkey.
# Passwords never go on a command line.
{{- if .sentinel }}
#
# The role is decided at startup by ASKING Sentinel who the current primary is,
# rather than hardcoding it. Guarantees:
#   - a replica always replicaof the *current* primary Sentinel reports
#   - a restarted pod (even a former primary) rejoins as a replica of the live
#     primary; it never boots as a standalone master serving stale data
#   - only Sentinel ever promotes; this script never promotes
{{- end }}
set -eu
umask 077

CONF=/run/valkey/valkey.conf
{
  echo "include /etc/valkey/valkey.conf"
  printf 'requirepass "%s"\n' "$VALKEY_PASSWORD"
  echo "maxmemory ${VALKEY_MAXMEMORY}"
} > "$CONF"
{{- if .sentinel }}

# $HOSTNAME is a bash-ism and is unset in the image's busybox ash; derive the
# pod name (e.g. "{{ .name }}-1") from the hostname command instead.
POD_NAME="$(hostname)"
SERVICE="{{ .name }}-headless"
SENTINEL_PORT=26379
MASTER_NAME={{ .masterName }}
MY_FQDN="${POD_NAME}.${SERVICE}.${POD_NAMESPACE}.svc.cluster.local"
ORDINAL="${POD_NAME##*-}"

# Ask each peer's Sentinel for the current primary address. First answer wins.
discover_primary() {
  for i in {{ range $i := until (int .replicas) }}{{ $i }} {{ end }}; do
    peer="{{ .name }}-${i}.${SERVICE}.${POD_NAMESPACE}.svc.cluster.local"
    addr=$(REDISCLI_AUTH="$SENTINEL_PASSWORD" timeout 2 valkey-cli -h "$peer" -p "$SENTINEL_PORT" \
             sentinel get-master-addr-by-name "$MASTER_NAME" \
             2>/dev/null | head -1 || true)
    if [ -n "$addr" ]; then
      echo "$addr"
      return 0
    fi
  done
  return 0
}

# A seeded Sentinel always names a primary, so no answer means none could be
# reached. Keep asking before taking that as a cold start: a new pod's
# connections are refused for about a second until NetworkPolicy admits its
# IP, and one failed round would otherwise boot ordinal 0 as a second primary.
DEADLINE=$(( $(date +%s) + 20 ))
PRIMARY="$(discover_primary)"
while [ -z "$PRIMARY" ] && [ "$(date +%s)" -lt "$DEADLINE" ]; do
  sleep 1
  PRIMARY="$(discover_primary)"
done

# replica-announce-ip pins our identity to a stable DNS name so Sentinel and
# the primary track us across reschedules (pod IPs are ephemeral).
{
  printf 'masterauth "%s"\n' "$VALKEY_PASSWORD"
  echo "replica-announce-ip ${MY_FQDN}"
} >> "$CONF"

if [ -n "$PRIMARY" ] && [ "$PRIMARY" != "$MY_FQDN" ]; then
  # Sentinel knows a primary and it isn't us -> join as its replica.
  echo "Sentinel reports primary=${PRIMARY}; starting as replica."
  echo "replicaof ${PRIMARY} 6379" >> "$CONF"
elif [ -z "$PRIMARY" ] && [ "$ORDINAL" != "0" ]; then
  # Cold bootstrap, no Sentinel reachable yet: seed replicas off {{ .name }}-0,
  # which matches the Sentinel monitor seed so the roles stay consistent.
  echo "No primary known and not ordinal 0; bootstrapping as replica of {{ .name }}-0."
  echo "replicaof {{ .name }}-0.${SERVICE}.${POD_NAMESPACE}.svc.cluster.local 6379" >> "$CONF"
else
  # Either Sentinel says we are the primary, or we are the ordinal-0 bootstrap.
  echo "Starting as primary (reported primary='${PRIMARY:-none}', ordinal=${ORDINAL})."
fi
{{- end }}

exec valkey-server "$CONF"
{{- end }}

{{/*
Sentinel entrypoint. Takes the settings dict.
*/}}
{{- define "valkeyInstance.sentinelEntrypoint" -}}
#!/bin/sh
# Sentinel REWRITES its own config file at runtime (to record the current
# primary, discovered replicas, and known peer sentinels). The config therefore
# cannot live on the read-only ConfigMap mount -- it is generated here on a
# writable emptyDir. Passwords are injected from the environment so they are
# never committed to git.
set -eu
umask 077

# $HOSTNAME is a bash-ism and is unset in the image's busybox ash; derive the
# pod name (e.g. "{{ .name }}-1") from the hostname command instead.
POD_NAME="$(hostname)"
SERVICE="{{ .name }}-headless"
MASTER_NAME={{ .masterName }}
CONF=/sentinel/sentinel.conf
MY_FQDN="${POD_NAME}.${SERVICE}.${POD_NAMESPACE}.svc.cluster.local"
SEED_PRIMARY="{{ .name }}-0.${SERVICE}.${POD_NAMESPACE}.svc.cluster.local"

cat > "$CONF" <<EOF
port 26379
requirepass "${SENTINEL_PASSWORD}"
# Track nodes by DNS name, not ephemeral pod IP, so failover survives reschedule.
sentinel resolve-hostnames yes
sentinel announce-hostnames yes
sentinel announce-ip ${MY_FQDN}
# Seed: monitor {{ .name }}-0 as the initial primary, quorum {{ div (add1 (int .replicas)) 2 }} of {{ .replicas }} sentinels.
# After the first failover Sentinel tracks the real primary itself.
sentinel monitor ${MASTER_NAME} ${SEED_PRIMARY} 6379 {{ div (add1 (int .replicas)) 2 }}
sentinel auth-pass ${MASTER_NAME} "${VALKEY_PASSWORD}"
sentinel down-after-milliseconds ${MASTER_NAME} 5000
sentinel failover-timeout ${MASTER_NAME} 10000
sentinel parallel-syncs ${MASTER_NAME} 1
protected-mode no
EOF

# Sentinel needs the local Valkey up before it can usefully monitor anything.
until REDISCLI_AUTH="$VALKEY_PASSWORD" valkey-cli -h 127.0.0.1 -p 6379 ping >/dev/null 2>&1; do
  echo "Waiting for local valkey to be ready..."
  sleep 2
done

exec valkey-sentinel "$CONF"
{{- end }}
