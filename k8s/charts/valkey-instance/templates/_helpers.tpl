{{/*
Normalized settings, as YAML. Every other template reads these instead of
.Values.valkeyInstance, so defaults and validation live in one place:

  {{- $v := include "valkeyInstance.settings" . | fromYaml -}}

Built field by field with `dig` rather than by merging a defaults dict: Sprig's
merge functions skip "empty" source values, so `metrics.enabled: false` would
silently lose to a default of true.
*/}}
{{- define "valkeyInstance.settings" -}}
{{- $u := .Values.valkeyInstance | default dict -}}
{{- $topology := dig "topology" "sentinel" $u -}}
{{- if not (has $topology (list "sentinel" "standalone")) -}}
{{- fail (printf "valkeyInstance.topology must be sentinel or standalone, got %q" $topology) -}}
{{- end -}}
{{- $sentinel := eq $topology "sentinel" -}}
{{- $name := dig "name" "valkey" $u -}}
{{- $replicas := int (dig "replicas" 3 $u) -}}
{{- if and $sentinel (or (lt $replicas 3) (eq (mod $replicas 2) 0)) -}}
{{- fail "valkeyInstance.replicas must be an odd number of at least 3, so Sentinel always has a quorum" -}}
{{- end -}}
{{- if not $sentinel -}}
{{- $replicas = 1 -}}
{{- end -}}
{{- $metrics := dig "metrics" "enabled" true $u -}}
{{- if not (dig "image" "" $u) -}}
{{- fail "valkeyInstance.image is required" -}}
{{- end -}}
{{- if and $metrics (not (dig "exporterImage" "" $u)) -}}
{{- fail "valkeyInstance.exporterImage is required while metrics.enabled is true" -}}
{{- end -}}
{{- $clients := dig "networkPolicy" "clients" list $u -}}
{{- if not $clients -}}
{{- fail "valkeyInstance.networkPolicy.clients must list the NetworkPolicy peers that connect to Valkey; anything not listed is refused" -}}
{{- end -}}
{{- $defaultNodeSelector := dict -}}
{{- if $sentinel -}}
{{- /* Nodes with a Longhorn disk, where a strict-local volume can live. */ -}}
{{- $defaultNodeSelector = dict "node.msng.to/cnpg-storage" "true" -}}
{{- end -}}
name: {{ $name }}
component: {{ dig "component" $name $u }}
topology: {{ $topology }}
sentinel: {{ $sentinel }}
replicas: {{ $replicas }}
masterName: {{ dig "masterName" "mymaster" $u }}
image: {{ $u.image | quote }}
exporterImage: {{ dig "exporterImage" "" $u | quote }}
labelsTemplate: {{ dig "labelsTemplate" "" $u | quote }}
maxmemoryPolicy: {{ dig "maxmemoryPolicy" "allkeys-lru" $u }}
metrics: {{ $metrics }}
auth:
  existingSecret: {{ dig "auth" "existingSecret" "" $u | quote }}
  secretName: {{ dig "auth" "existingSecret" "" $u | default (dig "auth" "secretName" $name $u) | quote }}
  passwordKey: {{ dig "auth" "passwordKey" "valkey-password" $u | quote }}
  sentinelPasswordKey: {{ dig "auth" "sentinelPasswordKey" "sentinel-password" $u | quote }}
persistence:
  storageClass: {{ dig "persistence" "storageClass" "longhorn-strict-local" $u }}
  size: {{ dig "persistence" "size" "1Gi" $u }}
nodeSelector:
  {{- toYaml (dig "nodeSelector" $defaultNodeSelector $u) | nindent 2 }}
resources:
  valkey:
    {{- toYaml (dig "resources" "valkey" (dict "requests" (dict "cpu" "100m" "memory" "256Mi") "limits" (dict "memory" "512Mi")) $u) | nindent 4 }}
  sentinel:
    {{- toYaml (dig "resources" "sentinel" (dict "requests" (dict "cpu" "50m" "memory" "64Mi") "limits" (dict "memory" "128Mi")) $u) | nindent 4 }}
  exporter:
    {{- toYaml (dig "resources" "exporter" (dict "requests" (dict "cpu" "10m" "memory" "32Mi") "limits" (dict "memory" "64Mi")) $u) | nindent 4 }}
networkPolicy:
  clients:
    {{- toYaml $clients | nindent 4 }}
{{- end }}

{{/*
Pod selector labels. Fixed rather than derived from the consuming chart: a
StatefulSet's selector is immutable, and the consumer's own selector labels
would also put these pods behind its other Services.
Takes the settings dict.
*/}}
{{- define "valkeyInstance.selectorLabels" -}}
app.kubernetes.io/name: valkey
app.kubernetes.io/component: {{ .component }}
{{- end }}

{{/*
Labels for every resource: the consuming chart's labelsTemplate, if any, with
the selector labels taking precedence over its name and component.
Takes (dict "v" $settings "ctx" $).
*/}}
{{- define "valkeyInstance.labels" -}}
{{- $labels := dict -}}
{{- with .v.labelsTemplate -}}
{{- $labels = include . $.ctx | fromYaml -}}
{{- end -}}
{{- $labels = mustMergeOverwrite $labels (include "valkeyInstance.selectorLabels" .v | fromYaml) -}}
{{- toYaml $labels -}}
{{- end }}

{{/*
Env vars carrying the passwords. valkey-cli reads REDISCLI_AUTH, so probes and
hooks set it per command instead of passing -a, which `ps` would show.
Takes the settings dict.
*/}}
{{- define "valkeyInstance.passwordEnv" -}}
- name: VALKEY_PASSWORD
  valueFrom:
    secretKeyRef:
      name: {{ .auth.secretName }}
      key: {{ .auth.passwordKey }}
{{- if .sentinel }}
- name: SENTINEL_PASSWORD
  valueFrom:
    secretKeyRef:
      name: {{ .auth.secretName }}
      key: {{ .auth.sentinelPasswordKey }}
{{- end }}
{{- end }}

{{/*
Container security context shared by every container.
*/}}
{{- define "valkeyInstance.containerSecurityContext" -}}
allowPrivilegeEscalation: false
readOnlyRootFilesystem: true
capabilities:
  drop:
    - ALL
{{- end }}
