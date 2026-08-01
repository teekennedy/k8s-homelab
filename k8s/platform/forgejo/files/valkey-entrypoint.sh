#!/bin/sh
# Valkey server entrypoint with dynamic primary/replica role discovery.
#
# The role is decided at startup by ASKING Sentinel who the current primary is,
# rather than hardcoding it. This is what the original redis+sentinel setup got
# wrong: it configured no replication at all, so Sentinel had nothing to fail
# over to and a restarted pod always came back as its own empty master.
#
# Guarantees:
#   - a replica always replicaof the *current* primary Sentinel reports
#   - a restarted pod (even a former primary) rejoins as a replica of the live
#     primary; it never boots as a standalone master serving stale data
#   - only Sentinel ever promotes; this script never promotes
set -eu

# $HOSTNAME is a bash-ism and is unset in the image's busybox ash; derive the
# pod name (e.g. "valkey-1") from the hostname command instead.
POD_NAME="$(hostname)"
SERVICE="valkey-headless"
SENTINEL_PORT=26379
MASTER_NAME={{ .Values.valkey.masterName }}
MY_FQDN="${POD_NAME}.${SERVICE}.${POD_NAMESPACE}.svc.cluster.local"
ORDINAL="${POD_NAME##*-}"

# Ask each peer's Sentinel for the current primary address. First answer wins.
discover_primary() {
  for i in {{ range $i := until (int .Values.valkey.replicas) }}{{ $i }} {{ end }}; do
    peer="valkey-${i}.${SERVICE}.${POD_NAMESPACE}.svc.cluster.local"
    addr=$(valkey-cli -h "$peer" -p "$SENTINEL_PORT" -a "$SENTINEL_PASSWORD" \
             --no-auth-warning sentinel get-master-addr-by-name "$MASTER_NAME" \
             2>/dev/null | head -1 || true)
    if [ -n "$addr" ]; then
      echo "$addr"
      return 0
    fi
  done
  return 0
}

PRIMARY="$(discover_primary)"
REPLICAOF=""

if [ -n "$PRIMARY" ] && [ "$PRIMARY" != "$MY_FQDN" ]; then
  # Sentinel knows a primary and it isn't us -> join as its replica.
  echo "Sentinel reports primary=${PRIMARY}; starting as replica."
  REPLICAOF="--replicaof ${PRIMARY} 6379"
elif [ -z "$PRIMARY" ] && [ "$ORDINAL" != "0" ]; then
  # Cold bootstrap, no Sentinel reachable yet: seed replicas off valkey-0,
  # which matches the Sentinel monitor seed so the roles stay consistent.
  echo "No primary known and not ordinal 0; bootstrapping as replica of valkey-0."
  REPLICAOF="--replicaof valkey-0.${SERVICE}.${POD_NAMESPACE}.svc.cluster.local 6379"
else
  # Either Sentinel says we are the primary, or we are the ordinal-0 bootstrap.
  echo "Starting as primary (reported primary='${PRIMARY:-none}', ordinal=${ORDINAL})."
fi

# --replica-announce-ip pins our identity to a stable DNS name so Sentinel and
# the primary track us across reschedules (pod IPs are ephemeral).
# shellcheck disable=SC2086
exec valkey-server /etc/valkey/valkey.conf \
  --requirepass "$VALKEY_PASSWORD" \
  --masterauth "$VALKEY_PASSWORD" \
  --maxmemory "$VALKEY_MAXMEMORY" \
  --replica-announce-ip "$MY_FQDN" \
  $REPLICAOF
