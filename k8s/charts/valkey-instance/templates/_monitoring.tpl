{{/*
Scrapes the redis_exporter sidecar in every Valkey pod. Alerts live in
k8s/platform/monitoring-system/templates/prometheus-rule-valkey.yaml.
Takes (dict "v" $settings "ctx" $).
*/}}
{{- define "valkeyInstance.podMonitor" -}}
{{- $v := .v -}}
apiVersion: monitoring.coreos.com/v1
kind: PodMonitor
metadata:
  name: {{ $v.name }}
  namespace: {{ .ctx.Release.Namespace }}
  labels:
    {{- include "valkeyInstance.labels" . | nindent 4 }}
spec:
  selector:
    matchLabels:
      {{- include "valkeyInstance.selectorLabels" $v | nindent 6 }}
  podMetricsEndpoints:
    - port: metrics
      interval: 30s
{{- end }}

{{/*
At most one replication-set pod down at a time from evictions, so draining one
node never costs Sentinel its quorum.
Takes (dict "v" $settings "ctx" $).
*/}}
{{- define "valkeyInstance.podDisruptionBudget" -}}
{{- $v := .v -}}
apiVersion: policy/v1
kind: PodDisruptionBudget
metadata:
  name: {{ $v.name }}
  namespace: {{ .ctx.Release.Namespace }}
  labels:
    {{- include "valkeyInstance.labels" . | nindent 4 }}
spec:
  maxUnavailable: 1
  selector:
    matchLabels:
      {{- include "valkeyInstance.selectorLabels" $v | nindent 6 }}
{{- end }}
