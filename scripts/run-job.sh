#!/usr/bin/env bash

set -euo pipefail

usage() {
  echo "Usage: $0 [-h|--help] [-v|--verbose] [-n|--namespace NAMESPACE] JOB_NAME"
  echo ""
  echo "Run a Kubernetes job from a CronJob or re-run an existing Job"
  echo ""
  echo "Arguments:"
  echo "  JOB_NAME                Name of the CronJob or Job to run"
  echo ""
  echo "Options:"
  echo "  -n, --namespace NS      Kubernetes namespace (optional, uses current context default)"
  echo "  -v, --verbose           Enable verbose output"
  echo "  -h, --help              Show this help message"
}

namespace=""
job_name=""

while [[ $# -gt 0 ]]; do
  case $1 in
  -h | --help)
    usage
    exit 0
    ;;
  -v | --verbose)
    set -x
    shift
    ;;
  -n | --namespace)
    if [ -z "${2:-}" ]; then
      echo "Error: --namespace requires an argument"
      usage
      exit 1
    fi
    namespace="$2"
    shift 2
    ;;
  -*)
    echo "Unknown option $1"
    usage
    exit 1
    ;;
  *)
    if [ -z "$job_name" ]; then
      job_name="$1"
      shift
    else
      echo "Unexpected argument $1"
      usage
      exit 1
    fi
    ;;
  esac
done

if [ -z "$job_name" ]; then
  echo "Missing required argument: JOB_NAME"
  usage
  exit 1
fi

# Build namespace flag for kubectl commands
ns_flag=""
if [ -n "$namespace" ]; then
  ns_flag="-n $namespace"
fi

# Auto-detect if it's a CronJob or Job
if kubectl $ns_flag get cronjob.batch/$job_name &>/dev/null; then
  echo "Detected CronJob: $job_name"
  resource_type="cronjob"
elif kubectl $ns_flag get job.batch/$job_name &>/dev/null; then
  echo "Detected Job: $job_name"
  resource_type="job"
else
  echo "Error: No CronJob or Job found with name '$job_name'${namespace:+ in namespace '$namespace'}"
  exit 1
fi

job="$job_name-manual-run-$(date +%s)"

if [ "$resource_type" = "cronjob" ]; then
  # Create a job from the cronjob
  kubectl $ns_flag create job --from=cronjob.batch/$job_name "$job"
else
  # Create a new job from the existing job's template
  echo "Creating new job from Job template: $job_name"
  kubectl $ns_flag get job "$job_name" -o json | \
    jq --arg newname "$job" '
      .metadata.name = $newname |
      .spec.template.metadata.name = $newname |
      del(
        .metadata.uid,
        .metadata.resourceVersion,
        .metadata.creationTimestamp,
        .metadata.labels["batch.kubernetes.io/controller-uid"],
        .metadata.labels["controller-uid"],
        .status,
        .spec.selector,
        .spec.template.metadata.labels
      )
    ' | \
    kubectl $ns_flag create -f -
fi

echo "Waiting for pod to exist..."
kubectl $ns_flag wait --for=create --timeout=2m pod -l batch.kubernetes.io/job-name="$job"

echo "Waiting for pod to be ready..."
kubectl $ns_flag wait --for=condition=Ready --timeout=5m pod -l batch.kubernetes.io/job-name="$job"

kubectl $ns_flag logs "job/$job" -f
