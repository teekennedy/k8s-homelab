#!/usr/bin/env bash
# Runs named dev clusters that outlive a single Dagger call, and writes a
# kubeconfig for each. See "Dev clusters" in .dagger/README.md.
#
# A cluster is a k3s server run as a Dagger service, so it lives exactly as long
# as the `dagger call` that started it. `up` leaves that call running in the
# background; `down` stops it. The cluster's state is kept in the Dagger engine
# under its name, so `up` again resumes it.
set -euo pipefail

usage() {
  cat <<'EOF'
Usage: dev-cluster.sh <command> [name] [options]

Commands:
  up <name> [--port N]     Start the cluster, or resume it, and write its kubeconfig
  down <name>              Stop the cluster, keeping its state
  destroy <name>           Stop the cluster and delete its state and kubeconfig
  env <name>               Print the export that points kubectl at the cluster
  test <name> [args...]    Deploy to the cluster and run the integration tests;
                           args go to `dagger call test-kubernetes-integration`
  list                     List the clusters and whether each is running
  logs <name>              Print the cluster's log

Each cluster gets a kubeconfig of its own, holding only that cluster. Select
one for the current shell with:

  eval "$(dev-cluster.sh env <name>)"
EOF
}

repo_root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
state_root=${DEVENV_STATE:-$repo_root/.devenv/state}
clusters_dir=$state_root/dev-clusters
kube_dir=$state_root/kube

# How long `up` waits for the API server, which covers pulling the k3s image
# into an engine that has never run one.
start_timeout=300

die() {
  echo "dev-cluster: $*" >&2
  exit 1
}

# The name becomes a directory, a Dagger cache volume key and a kubeconfig
# context, so it is held to what all three accept.
check_name() {
  [[ ${1:-} =~ ^[a-z0-9]([a-z0-9-]{0,30}[a-z0-9])?$ ]] ||
    die "the cluster name must be lowercase letters, digits and hyphens, at most 32 characters; got '${1:-}'"
  # production's kubeconfig lives beside these as kube/config, and staging's
  # will too: neither may be shadowed by a dev cluster.
  case $1 in
    production | staging | config) die "'$1' is reserved" ;;
  esac
}

cluster_dir() { echo "$clusters_dir/$1"; }
kubeconfig_path() { echo "$kube_dir/$1.yaml"; }

# Succeeds if the cluster's `dagger call` is still running.
is_running() {
  local pid_file
  pid_file=$(cluster_dir "$1")/pid
  [[ -f $pid_file ]] && kill -0 "$(cat "$pid_file")" 2>/dev/null
}

free_port() {
  python3 -c 'import socket; s = socket.socket(); s.bind(("127.0.0.1", 0)); print(s.getsockname()[1])'
}

cmd_up() {
  local name=$1 port=""
  shift
  while [[ $# -gt 0 ]]; do
    case $1 in
      --port)
        port=${2:?--port needs a value}
        shift 2
        ;;
      *) die "unknown option for up: $1" ;;
    esac
  done

  local dir kubeconfig
  dir=$(cluster_dir "$name")
  kubeconfig=$(kubeconfig_path "$name")
  if is_running "$name"; then
    die "cluster $name is already running; its kubeconfig is $kubeconfig"
  fi

  mkdir -p "$dir" "$kube_dir"
  chmod 700 "$dir"

  # Kept between runs, so that the kubeconfig stays valid when the cluster is
  # resumed. Whoever holds the token is cluster-admin.
  if [[ ! -s $dir/token ]]; then
    (umask 077 && od -An -N32 -tx1 /dev/urandom | tr -d ' \n' >"$dir/token")
  fi
  if [[ -z $port ]]; then
    if [[ -s $dir/port ]]; then
      port=$(cat "$dir/port")
    else
      port=$(free_port)
    fi
  fi
  echo "$port" >"$dir/port"

  echo "Starting dev cluster $name on https://127.0.0.1:$port ..." >&2
  (
    cd "$repo_root"
    nohup dagger call --progress plain dev-cluster \
      --name "$name" --token "file:$dir/token" \
      up --ports "$port:6443" >"$dir/log" 2>&1 &
    echo $! >"$dir/pid"
  )

  # /cacerts is the one endpoint k3s serves without credentials, and it is what
  # supplies the CA, so this request alone skips verification. It stays on the
  # loopback tunnel to the engine.
  local ca=$dir/ca.crt deadline=$((SECONDS + start_timeout))
  until curl --fail --silent --insecure --max-time 5 --output "$ca" "https://127.0.0.1:$port/cacerts"; do
    if ! is_running "$name"; then
      tail -n 30 "$dir/log" >&2 || true
      die "cluster $name exited while starting; its log is $dir/log"
    fi
    if ((SECONDS >= deadline)); then
      cmd_down "$name"
      die "cluster $name did not answer within ${start_timeout}s; its log is $dir/log"
    fi
    sleep 2
  done

  # Built from scratch each time, and holding this cluster alone: a shell whose
  # KUBECONFIG names this file has no other cluster to reach by mistake.
  rm -f "$kubeconfig"
  (umask 077 && : >"$kubeconfig")
  kubectl config set-cluster "$name" --kubeconfig "$kubeconfig" \
    --server "https://127.0.0.1:$port" --certificate-authority "$ca" --embed-certs >/dev/null
  kubectl config set-credentials "$name" --kubeconfig "$kubeconfig" --token "$(cat "$dir/token")" >/dev/null
  kubectl config set-context "$name" --kubeconfig "$kubeconfig" --cluster "$name" --user "$name" >/dev/null
  kubectl config use-context "$name" --kubeconfig "$kubeconfig" >/dev/null
  rm -f "$ca"

  # A resumed cluster answers, restarts k3s once and answers again (see
  # k3sEntrypoint in .dagger/k3s.go), so one good answer is not enough: wait for
  # the node to stay Ready across several consecutive checks.
  local steady=0
  while ((steady < 5)); do
    ((SECONDS < deadline)) || die "cluster $name did not become steadily ready within ${start_timeout}s; its log is $dir/log"
    if kubectl --kubeconfig "$kubeconfig" wait --for condition=Ready node --all --timeout 5s >/dev/null 2>&1; then
      steady=$((steady + 1))
    else
      steady=0
    fi
    sleep 1
  done

  echo "Dev cluster $name is up. To use it in this shell:" >&2
  cmd_env "$name"
}

cmd_down() {
  local name=$1 dir pid
  dir=$(cluster_dir "$name")
  if ! is_running "$name"; then
    echo "Dev cluster $name is not running." >&2
    rm -f "$dir/pid"
    return 0
  fi
  pid=$(cat "$dir/pid")
  # The service stops with the Dagger session, which ends with this process.
  kill "$pid"
  local deadline=$((SECONDS + 60))
  while kill -0 "$pid" 2>/dev/null; do
    ((SECONDS < deadline)) || die "cluster $name (pid $pid) did not stop within 60s"
    sleep 1
  done
  rm -f "$dir/pid"
  echo "Dev cluster $name is stopped; its state is kept." >&2
}

cmd_destroy() {
  local name=$1 dir
  dir=$(cluster_dir "$name")
  cmd_down "$name"
  (cd "$repo_root" && dagger call --progress plain delete-dev-cluster --name "$name")
  rm -f "$(kubeconfig_path "$name")" "$dir/token" "$dir/port" "$dir/log" "$dir/ca.crt"
  rmdir "$dir" 2>/dev/null || true
}

cmd_env() {
  local kubeconfig
  kubeconfig=$(kubeconfig_path "$1")
  [[ -f $kubeconfig ]] || die "cluster $1 has no kubeconfig; start it with: dev-cluster.sh up $1"
  echo "export KUBECONFIG=$kubeconfig"
}

cmd_test() {
  local name=$1 dir kubeconfig
  shift
  dir=$(cluster_dir "$name")
  kubeconfig=$(kubeconfig_path "$name")
  is_running "$name" || die "cluster $name is not running; start it with: dev-cluster.sh up $name"
  cd "$repo_root"
  exec dagger call test-kubernetes-integration \
    --cluster "tcp://localhost:$(cat "$dir/port")" --kubeconfig "file:$kubeconfig" "$@"
}

cmd_list() {
  local dir name
  [[ -d $clusters_dir ]] || return 0
  for dir in "$clusters_dir"/*/; do
    [[ -d $dir ]] || continue
    name=$(basename "$dir")
    if is_running "$name"; then
      echo "$name  running  https://127.0.0.1:$(cat "$dir/port")"
    else
      echo "$name  stopped"
    fi
  done
}

cmd_logs() {
  cat "$(cluster_dir "$1")/log"
}

command=${1:-}
[[ -n $command ]] || {
  usage >&2
  exit 2
}
shift

case $command in
  up | down | destroy | env | test | logs)
    check_name "${1:-}"
    "cmd_$command" "$@"
    ;;
  list) cmd_list ;;
  -h | --help | help) usage ;;
  *)
    usage >&2
    exit 2
    ;;
esac
