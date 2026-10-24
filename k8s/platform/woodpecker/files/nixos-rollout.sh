#!/bin/sh
# Ordered NixOS reboot rollout.
#
# The hosts stage their new generation themselves (nixos-selfupdate.service);
# this walks the fleet afterwards and lets exactly one of them reboot at a time,
# in k3s upgrade order -- the clusterInit server first, then the other servers,
# then the agents. Sentinel creation order is what controls reboot order: kured
# takes a cluster-wide lock and picks a node itself, so creating all four
# sentinels at once would give an arbitrary order.
#
# This runs as a Job rather than inside the Woodpecker pipeline because it has
# to outlive its own node being rebooted. It is idempotent from the top: the
# first thing it asks each host is whether it even needs a reboot, so a pod
# evicted mid-rollout simply resumes from wherever the fleet actually is.
set -eu

SSH_DIR=/tmp/ssh

setup_ssh() {
  apk add --no-cache openssh-client >/dev/null 2>&1

  mkdir -p "$SSH_DIR"
  chmod 700 "$SSH_DIR"
  install -m 0644 /known-hosts/known_hosts "$SSH_DIR/known_hosts"
}

# The certificate is reissued every 6h with a 12h validity and the mounted
# Secret follows along, so copy it fresh for every connection: a rollout can
# spend days waiting on kured.
load_creds() {
  install -m 0600 /creds/id_ed25519 "$SSH_DIR/id_ed25519"
  install -m 0644 /creds/id_ed25519-cert.pub "$SSH_DIR/id_ed25519-cert.pub"
}

# Runs one of the deploybot verbs on a host. StrictHostKeyChecking stays on
# against the committed known_hosts: the credential is short-lived but it is
# still a credential, and there is no reason to hand it to an unverified host.
trigger() {
  load_creds
  ssh -n \
    -o BatchMode=yes \
    -o IdentitiesOnly=yes \
    -o StrictHostKeyChecking=yes \
    -o UserKnownHostsFile="$SSH_DIR/known_hosts" \
    -o ConnectTimeout=10 \
    -i "$SSH_DIR/id_ed25519" \
    -o CertificateFile="$SSH_DIR/id_ed25519-cert.pub" \
    "$SSH_USER@$2" "$1"
}

# Prints "<Ready status> <unschedulable>" from a single GET so the two fields
# cannot disagree, or nothing if the API call failed.
node_state() {
  kubectl get node "$1" \
    -o jsonpath='{.status.conditions[?(@.type=="Ready")].status} {.spec.unschedulable}' \
    2>/dev/null || true
}

# kured cordons before draining and uncordons once the node is back, so an
# uncordoned Ready node is the signal that it is done with this host.
node_ready() {
  state=$(node_state "$1")
  ready=${state%% *}
  unsched=${state#* }

  [ "$ready" = "True" ] || return 1
  [ -z "$unsched" ] || [ "$unsched" = "false" ]
}

# True once kured has visibly started on the node: cordoned, or already down.
# An API error is not evidence either way, so it counts as not started.
node_reboot_started() {
  case "$(node_state "$1")" in
  *" true" | "False "* | "Unknown "*) return 0 ;;
  *) return 1 ;;
  esac
}

# Firing alerts can hold kured off for days, so there is no limit on waiting
# for it to start; the Job's activeDeadlineSeconds bounds that. Once kured has
# started on the host, it gets REBOOT_TIMEOUT to drain, reboot and come back.
wait_for_host() {
  name=$1
  ip=$2
  deadline=""

  while [ -z "$deadline" ] || [ "$(date +%s)" -lt "$deadline" ]; do
    sleep "$POLL_INTERVAL"

    if [ -z "$deadline" ] && node_reboot_started "$name"; then
      deadline=$(($(date +%s) + REBOOT_TIMEOUT))
      echo "kured has started on $name; allowing ${REBOOT_TIMEOUT}s for it to come back"
    fi

    # The host itself is the authority on whether it came back on the generation
    # that was staged for it. Re-asking is safe: once booted == staged the verb
    # reports UP_TO_DATE and touches nothing.
    if out=$(trigger reboot "$ip" 2>/dev/null); then
      case "$out" in
      *UP_TO_DATE*)
        if node_ready "$name"; then
          echo "$name is back on its staged generation and Ready"
          return 0
        fi
        echo "$name booted the staged generation; waiting for the node to go Ready"
        ;;
      esac
    fi
  done

  echo "timed out after ${REBOOT_TIMEOUT}s waiting for $name to reboot" >&2
  return 1
}

setup_ssh

# Intentionally unquoted: HOST_ORDER is a space-separated name=ip list and the
# order of that list is the whole point of this job.
# shellcheck disable=SC2086
for entry in $HOST_ORDER; do
  name=${entry%%=*}
  ip=${entry#*=}

  echo "=== $name ($ip)"

  if ! out=$(trigger reboot "$ip" 2>&1); then
    echo "could not reach $name ($ip): $out" >&2
    exit 1
  fi
  echo "$out"

  case "$out" in
  *UP_TO_DATE*)
    echo "$name already runs its staged generation; nothing to do"
    continue
    ;;
  *REBOOT_PENDING*)
    echo "sentinel created on $name; waiting for kured to drain and reboot it"
    ;;
  *)
    echo "unexpected reply from $name: $out" >&2
    exit 1
    ;;
  esac

  wait_for_host "$name" "$ip"
done

echo "rollout complete"
