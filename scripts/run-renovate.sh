#!/usr/bin/env bash

job="manual-try-$(date +%s)"

kubectl -n renovate create job --from=cronjob.batch/renovate "$job"

echo "Waiting for pod to exist..."
kubectl -n renovate wait --for=create --timeout=2m pod -l batch.kubernetes.io/job-name="$job"

echo "Waiting for pod to be ready..."
kubectl -n renovate wait --for=condition=Ready --timeout=5m pod -l batch.kubernetes.io/job-name="$job"

kubectl -n renovate logs "job/$job" -f
