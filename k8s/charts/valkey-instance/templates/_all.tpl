{{/*
Every resource of one Valkey instance. The consuming chart's only template:

  {{- include "valkeyInstance.all" . }}

See the chart README for the values under .Values.valkeyInstance.
*/}}
{{- define "valkeyInstance.all" -}}
{{- $v := include "valkeyInstance.settings" . | fromYaml -}}
{{- $args := dict "v" $v "ctx" . -}}
{{- if not $v.auth.existingSecret }}
---
{{ include "valkeyInstance.secret" $args }}
{{- end }}
---
{{ include "valkeyInstance.configMap" $args }}
---
{{ include "valkeyInstance.services" $args }}
---
{{ include "valkeyInstance.statefulSet" $args }}
---
{{ include "valkeyInstance.networkPolicy" $args }}
{{- if $v.sentinel }}
---
{{ include "valkeyInstance.podDisruptionBudget" $args }}
{{- end }}
{{- if $v.metrics }}
---
{{ include "valkeyInstance.podMonitor" $args }}
{{- end }}
{{ end -}}
