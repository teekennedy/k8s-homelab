#!/usr/bin/env bash

set -euo pipefail

usage() {
  echo "Usage: $0 [-h|--help] [-v|--verbose] namespace cronjob_name"
}

namespace=""
cronjob_name=""

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
    if [ -z "$namespace" ]; then
      namespace=$i
      shift
    elif [ -z "$cronjob_name"]; then
      cronjob_name=$i
      shift
    else
      echo "Unexpected argument $i"
      usage
      exit 1
    fi
    ;;
  esac
done

if [ -z "$namespace" ]; then
  echo "Missing required argument: namespace"
  usage
  exit 1
fi

if [ -z "$cronjob_name" ]; then
  echo "Missing required argument: cronjob_name"
  usage
  exit 1
fi

job="$cronjob_name-manual-run-$(date +%s)"


kubectl -n $namespace create job --from=cronjob.batch/$cronjob_name "$job"

echo "Waiting for pod to exist..."
kubectl -n $namespace wait --for=create --timeout=2m pod -l batch.kubernetes.io/job-name="$job"

echo "Waiting for pod to be ready..."
kubectl -n $namespace wait --for=condition=Ready --timeout=5m pod -l batch.kubernetes.io/job-name="$job"

kubectl -n $namespace logs "job/$job" -f
