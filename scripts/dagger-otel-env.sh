#!/bin/sh
# Source before running the dagger CLI to export its traces, logs and metrics
# to the OpenTelemetry Collector:
#
#   . scripts/dagger-otel-env.sh [endpoint]
#
# endpoint defaults to the in-cluster collector; local runs use
# https://otel.msng.to. Inside a Woodpecker step the run is also tagged with
# the pipeline it belongs to.

endpoint=${1:-http://otel-collector.monitoring-system.svc:4318}

# Dagger only exports logs and metrics when their per-signal endpoint is set;
# OTEL_EXPORTER_OTLP_ENDPOINT on its own enables traces alone.
export OTEL_EXPORTER_OTLP_PROTOCOL=http/protobuf
export OTEL_EXPORTER_OTLP_TRACES_ENDPOINT="$endpoint/v1/traces"
export OTEL_EXPORTER_OTLP_LOGS_ENDPOINT="$endpoint/v1/logs"
export OTEL_EXPORTER_OTLP_METRICS_ENDPOINT="$endpoint/v1/metrics"

if [ "${CI:-}" = woodpecker ]; then
  ref=${CI_COMMIT_SOURCE_BRANCH:-${CI_COMMIT_TAG:-${CI_COMMIT_BRANCH:-}}}
  attrs="deployment.environment.name=ci"
  attrs="$attrs,cicd.pipeline.name=${CI_WORKFLOW_NAME:-}"
  attrs="$attrs,cicd.pipeline.run.id=${CI_PIPELINE_NUMBER:-}"
  attrs="$attrs,cicd.pipeline.run.url.full=${CI_PIPELINE_URL:-}"
  attrs="$attrs,vcs.ref.head.name=$ref"
  attrs="$attrs,vcs.ref.head.revision=${CI_COMMIT_SHA:-}"
else
  attrs="deployment.environment.name=local"
fi
export OTEL_RESOURCE_ATTRIBUTES="$attrs${OTEL_RESOURCE_ATTRIBUTES:+,$OTEL_RESOURCE_ATTRIBUTES}"
unset endpoint ref attrs
