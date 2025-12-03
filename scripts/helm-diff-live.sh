#!/usr/bin/env bash

set -euo pipefail

usage() {
  echo "Usage: $0 [-h|--help] [-v|--verbose] [-d|--difftool <name>] path/to/chart"
}

difftools=(
  "delta"
  "colordiff"
  "diff"
)

autodetect_difftool() {
  git_difftool="$(git config --get interactive.difffilter 2>/dev/null)"
  if [ -n "$git_difftool" ]; then
    # If git difftool is set, use it as the preferred diff tool
    echo "$git_difftool"
    return 0
  fi

  for tool in "${difftools[@]}"; do
    if command -v "$tool" &>/dev/null; then
      echo "$tool"
      return 0
    fi
  done
  echo "No suitable diff tool found. Please install one of the following: ${difftools[*]}" >&2
  usage
  exit 1
}

chart_path=""
difftool=""

for i in "$@"; do
  case $i in
  -h | --help)
    usage
    exit 0
    ;;
  -v | --verbose)
    set -x
    shift
    ;;
  -d | --difftool)
    set -x
    shift
    ;;
  --)
    shift
    break
    ;;
  -*)
    echo "Unknown option $i"
    usage
    exit 1
    ;;
  *)
    if [ -z "$chart_path" ]; then
      chart_path=$i
      shift
    else
      echo "Unexpected argument $i"
      usage
      exit 1
    fi
    ;;
  esac
done

if [ -z "$chart_path" ]; then
  echo "Missing required argument chart_path"
  usage
  exit 1
fi

if [ -z "$difftool" ]; then
  difftool="$(autodetect_difftool)"
fi

namespace="$(basename "$chart_path")"
release="$(basename "$chart_path")"
output_dir="$(mktemp -d)"
trap 'rm -r "$output_dir"' EXIT

get_last_modified() {
  local path="$1"
  find "$path" -type f -exec stat '{}' --printf="%Y\n" \; |
    sort -n -r |
    head -n 1
}

last_modified="$(get_last_modified "$chart_path")"
last_successfully_modified=0

# Make sure dependencies are up to date
helm dependency update "$chart_path"

if helm template -n "$namespace" "$release" "$chart_path" | tee "$output_dir/$last_modified.yaml" | bat -l yaml; then
  last_successfully_modified="$last_modified"
fi

while sleep 1; do
  modified="$(get_last_modified "$chart_path")"
  if [[ $modified -gt $last_modified ]]; then
    if helm template -n "$namespace" "$release" "$chart_path" >"$output_dir/$modified.yaml"; then
      # Safely shell-quote the filenames
      quoted_files=$(printf ' %q' "$output_dir/$last_successfully_modified.yaml" "$output_dir/$modified.yaml")

      # Let the shell re-parse the difftool command line with the quoted filenames
      eval "$difftool$quoted_files" || :
      last_successfully_modified="$modified"
    fi
    last_modified="$modified"
  fi
done
