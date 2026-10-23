{{/*
The password Secret, filled in-cluster by mittwald secret-generator. Skipped
when auth.existingSecret names a Secret the consuming chart owns.

Hex-encoded so a password is safe to embed verbatim in a redis:// or
redis+sentinel:// connection string: one containing @ / : ? or & would
silently corrupt the URI.
Takes (dict "v" $settings "ctx" $).
*/}}
{{- define "valkeyInstance.secret" -}}
{{- $v := .v -}}
apiVersion: v1
kind: Secret
metadata:
  name: {{ $v.auth.secretName }}
  namespace: {{ .ctx.Release.Namespace }}
  labels:
    {{- include "valkeyInstance.labels" . | nindent 4 }}
  annotations:
    secret-generator.v1.mittwald.de/autogenerate: {{ $v.auth.passwordKey }}{{ if $v.sentinel }},{{ $v.auth.sentinelPasswordKey }}{{ end }}
    secret-generator.v1.mittwald.de/encoding: "hex"
    secret-generator.v1.mittwald.de/length: "32"
type: Opaque
{{- end }}
