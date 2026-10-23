#!/usr/bin/env bash
# Profile one dagger invocation: plain progress log, engine cache size, engine
# memory and engine disk, before, during and after the run.
set -uo pipefail

usage() {
  cat <<EOF
Usage: $0 [-o DIR] [-i SECONDS] [-n NAME] [--] DAGGER_ARGS...

Run \`dagger DAGGER_ARGS...\` with plain progress and record:
  dagger.log    the full plain progress output (stdout and stderr)
  engine.log    the engine container's own log for the duration of the run
  samples.tsv   engine memory and disk, every SECONDS (default 5)
  summary.txt   before/after cache size, memory, disk, GC prunes, wall time

The log directory is kept, so the same run can be grepped again for anything
else; a slow or fast run is often not reproducible. Exits with dagger's status.

Options:
  -o DIR        Write into DIR (default: a new directory under \$TMPDIR)
  -i SECONDS    Sampling interval (default 5)
  -n NAME       Label for the run, used in the default directory name
  -h            Show this help

Environment:
  _EXPERIMENTAL_DAGGER_RUNNER_HOST
                            A kube-pod://POD?namespace=NS[&context=CTX]
                            runner is profiled through kubectl; anything else
                            through a local Docker engine container
  DAGGER_ENGINE_CONTAINER   Docker engine container name (default: the
                            running dagger-engine-v<cli version> container)

Examples:
  $0 check
  $0 -n go-warm check --go
  $0 generate --dagger-go-sdk
EOF
}

out=""
interval=5
name="run"
while getopts ":o:i:n:h" opt; do
  case $opt in
    o) out=$OPTARG ;;
    i) interval=$OPTARG ;;
    n) name=$OPTARG ;;
    h) usage; exit 0 ;;
    *) usage >&2; exit 2 ;;
  esac
done
shift $((OPTIND - 1))
[ "${1:-}" = "--" ] && shift
if [ $# -eq 0 ]; then
  usage >&2
  exit 2
fi

if [ -z "$out" ]; then
  out=$(mktemp -d "${TMPDIR:-/tmp}/dagger-profile.$(date +%Y%m%d-%H%M%S).$name.XXXX")
fi
mkdir -p "$out"

# Metrics come from the engine itself: a local Docker container, or the pod a
# kube-pod:// runner host names. Any other runner host records the log only.
engine=""     # a label for the summary
kube=()       # kubectl args selecting the engine pod's namespace and context
pod=""
runner=${_EXPERIMENTAL_DAGGER_RUNNER_HOST:-}
case $runner in
kube-pod://*)
  rest=${runner#kube-pod://}
  pod=${rest%%\?*}
  query=""
  [ "$rest" != "$pod" ] && query=${rest#*\?}
  IFS='&' read -ra params <<<"$query"
  for param in "${params[@]}"; do
    case $param in
    namespace=*) kube+=(--namespace "${param#namespace=}") ;;
    context=*) kube+=(--context "${param#context=}") ;;
    container=*) kube_container=${param#container=} ;;
    esac
  done
  if command -v kubectl >/dev/null && kubectl "${kube[@]}" get pod "$pod" >/dev/null 2>&1; then
    engine="pod/$pod"
  fi
  ;;
"" | docker-container://* | image://*)
  engine=${DAGGER_ENGINE_CONTAINER:-}
  if [ -z "$engine" ] && command -v docker >/dev/null; then
    version=$(dagger version 2>/dev/null | sed -n 's/^version: *//p')
    engine=$(docker ps --format '{{.Names}}' --filter "name=^dagger-engine-${version}\$" | head -1)
    [ -n "$engine" ] || engine=$(docker ps --format '{{.Names}}' --filter 'name=^dagger-engine-' | head -1)
  fi
  ;;
esac
if [ -z "$engine" ]; then
  echo "warning: no engine container or pod found; recording the log only" >&2
fi

# engine_sh runs a shell script in the engine.
engine_sh() {
  if [ -n "$pod" ]; then
    kubectl "${kube[@]}" exec "$pod" ${kube_container:+-c "$kube_container"} -- sh -c "$1"
  else
    docker exec "$engine" sh -c "$1"
  fi
}

# engine_logs prints the engine's log since an RFC 3339 time.
engine_logs() {
  if [ -n "$pod" ]; then
    kubectl "${kube[@]}" logs "$pod" ${kube_container:+-c "$kube_container"} --since-time "$1"
  else
    docker logs --since "$1" "$engine"
  fi
}

# One line of engine metrics: memory in bytes (cgroup total, anonymous, page
# cache) and the disk backing /var/lib/dagger in bytes. A privileged engine
# pod sees the host's cgroup root at /sys/fs/cgroup, so the engine's own
# cgroup is the one /proc/1/cgroup names (less the /init leaf the engine
# moves itself into).
engine_metrics() {
  engine_sh '
    cg=$(sed -n "s/^0:://p" /proc/1/cgroup)
    d=/sys/fs/cgroup${cg%/init}
    [ -f "$d/memory.current" ] || d=/sys/fs/cgroup
    cur=$(cat "$d/memory.current")
    anon=$(sed -n "s/^anon //p" "$d/memory.stat")
    file=$(sed -n "s/^file //p" "$d/memory.stat")
    set -- $(df -B1 /var/lib/dagger | tail -1)
    printf "%s\t%s\t%s\t%s\t%s\t%s\n" "$cur" "$anon" "$file" "$2" "$3" "$4"
  ' 2>/dev/null
}

cache_stat() {
  # The value is the last line on stdout; progress goes to stderr.
  (cd / && DAGGER_PROGRESS=plain dagger -m core api call engine local-cache entry-set "$1" \
    2>/dev/null | tail -1)
}

gib() { awk -v b="${1:-0}" 'BEGIN { if (b == "") print "?"; else printf "%.2f GiB", b / 1073741824 }'; }

snapshot() {
  local label=$1
  [ -n "$engine" ] || return 0
  local m bytes entries
  m=$(engine_metrics)
  bytes=$(cache_stat disk-space-bytes | awk '{ printf "%.0f", $1 }')
  entries=$(cache_stat entry-count)
  IFS=$'\t' read -r mcur manon mfile dsize dused davail <<<"$m"
  printf '%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n' "$label" "$bytes" "$entries" \
    "$mcur" "$manon" "$mfile" "$dsize" "$dused" "$davail" >>"$out/snapshots.tsv"
}

printf 'when\tcache_bytes\tcache_entries\tmem_bytes\tmem_anon\tmem_file\tdisk_size\tdisk_used\tdisk_avail\n' >"$out/snapshots.tsv"
printf 'elapsed_s\tmem_bytes\tmem_anon\tmem_file\tdisk_size\tdisk_used\tdisk_avail\n' >"$out/samples.tsv"

if [ -n "$engine" ]; then
  min_free=$(cd / && DAGGER_PROGRESS=plain dagger -m core api call engine local-cache min-free-space 2>/dev/null | tail -1)
  max_used=$(cd / && DAGGER_PROGRESS=plain dagger -m core api call engine local-cache max-used-space 2>/dev/null | tail -1)
fi

snapshot before
start_iso=$(date -u +%Y-%m-%dT%H:%M:%SZ)
start=$(date +%s)

sampler=""
if [ -n "$engine" ]; then
  (
    while :; do
      m=$(engine_metrics) && printf '%s\t%s\n' "$(($(date +%s) - start))" "$m" >>"$out/samples.tsv"
      sleep "$interval"
    done
  ) &
  sampler=$!
fi
trap '[ -n "$sampler" ] && kill "$sampler" 2>/dev/null' EXIT

echo "profiling: dagger $* (log: $out/dagger.log)" >&2
printf '%q ' dagger "$@" >"$out/command"
echo >>"$out/command"
# The environment variable, unlike --progress, works with every subcommand.
DAGGER_PROGRESS=plain dagger "$@" >"$out/dagger.log" 2>&1
status=$?
end=$(date +%s)

[ -n "$sampler" ] && kill "$sampler" 2>/dev/null
wait "$sampler" 2>/dev/null
sampler=""
snapshot after

if [ -n "$engine" ]; then
  engine_logs "$start_iso" >"$out/engine.log" 2>&1
fi

{
  echo "command:   $(cat "$out/command")"
  echo "exit:      $status"
  echo "wall:      $((end - start))s"
  echo "log dir:   $out"
  if [ -n "$engine" ]; then
    echo "engine:    $engine"
    echo "gc policy: min-free $(gib "$(printf '%.0f' "$min_free")"), max-used $(gib "$(printf '%.0f' "$max_used")")"
    awk -F'\t' '
      function g(b) { return sprintf("%.2f GiB", b / 1073741824) }
      NR == 1 { next }
      { c[$1] = $2; e[$1] = $3; m[$1] = $4; a[$1] = $5; u[$1] = $8; f[$1] = $9 }
      END {
        printf "cache:     %s -> %s (%+.2f GiB), %d -> %d entries (%+d)\n", g(c["before"]), g(c["after"]), (c["after"] - c["before"]) / 1073741824, e["before"], e["after"], e["after"] - e["before"]
        printf "disk used: %s -> %s, avail %s -> %s\n", g(u["before"]), g(u["after"]), g(f["before"]), g(f["after"])
        printf "engine mem: %s -> %s (anon %s -> %s)\n", g(m["before"]), g(m["after"]), g(a["before"]), g(a["after"])
      }' "$out/snapshots.tsv"
    awk -F'\t' '
      function g(b) { return sprintf("%.2f GiB", b / 1073741824) }
      NR == 1 { next }
      { n++; if ($2 > pm) pm = $2; if ($3 > pa) pa = $3; if (min == "" || $7 < min) min = $7 }
      END { if (n) printf "peak:      mem %s (anon %s), lowest disk avail %s, %d samples\n", g(pm), g(pa), g(min), n }
    ' "$out/samples.tsv"
    pruned=$(grep -c 'msg="dagql pruned result"' "$out/engine.log")
    reclaimed=$(grep 'msg="dagql pruned result"' "$out/engine.log" |
      sed -n 's/.*reclaimedBytes=\([0-9]*\).*/\1/p' | awk '{ s += $1 } END { printf "%.0f", s }')
    echo "gc:        $pruned results pruned, $(gib "$reclaimed") reclaimed during the run"
  fi
} | tee "$out/summary.txt" >&2

exit "$status"
