#!/bin/sh
# Valkey Sentinel entrypoint.
#
# Sentinel REWRITES its own config file at runtime (to record the current
# primary, discovered replicas, and known peer sentinels). The config therefore
# cannot live on the read-only ConfigMap mount -- it is generated here on a
# writable emptyDir. Passwords are injected from the environment so they are
# never committed to git.
set -eu

# $HOSTNAME is a bash-ism and is unset in the image's busybox ash; derive the
# pod name (e.g. "valkey-1") from the hostname command instead.
POD_NAME="$(hostname)"
SERVICE="valkey-headless"
MASTER_NAME={{ .Values.valkey.masterName }}
CONF=/sentinel/sentinel.conf
MY_FQDN="${POD_NAME}.${SERVICE}.${POD_NAMESPACE}.svc.cluster.local"
SEED_PRIMARY="valkey-0.${SERVICE}.${POD_NAMESPACE}.svc.cluster.local"

mkdir -p "$(dirname "$CONF")"
cat > "$CONF" <<EOF
port 26379
requirepass ${SENTINEL_PASSWORD}
# Track nodes by DNS name, not ephemeral pod IP, so failover survives reschedule.
sentinel resolve-hostnames yes
sentinel announce-hostnames yes
sentinel announce-ip ${MY_FQDN}
# Seed: monitor valkey-0 as the initial primary, quorum {{ div (add1 (int .Values.valkey.replicas)) 2 }} of {{ .Values.valkey.replicas }} sentinels.
# After the first failover Sentinel tracks the real primary itself.
sentinel monitor ${MASTER_NAME} ${SEED_PRIMARY} 6379 {{ div (add1 (int .Values.valkey.replicas)) 2 }}
sentinel auth-pass ${MASTER_NAME} ${VALKEY_PASSWORD}
sentinel down-after-milliseconds ${MASTER_NAME} 5000
sentinel failover-timeout ${MASTER_NAME} 10000
sentinel parallel-syncs ${MASTER_NAME} 1
protected-mode no
EOF

# Sentinel needs the local Valkey up before it can usefully monitor anything.
until valkey-cli -h 127.0.0.1 -p 6379 -a "$VALKEY_PASSWORD" --no-auth-warning ping >/dev/null 2>&1; do
  echo "Waiting for local valkey to be ready..."
  sleep 2
done

exec valkey-sentinel "$CONF"
