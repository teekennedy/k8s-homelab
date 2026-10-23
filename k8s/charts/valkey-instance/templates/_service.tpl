{{/*
Services. A replication set gets a headless Service, giving every pod a stable
DNS name (<name>-N.<name>-headless.<ns>.svc) that Sentinel and replicas track
across reschedules, plus a ClusterIP Service fronting all the Sentinels.
Clients ask any Sentinel for the current primary. A standalone store gets one
ClusterIP Service on the Valkey port.
Takes (dict "v" $settings "ctx" $).
*/}}
{{- define "valkeyInstance.services" -}}
{{- $v := .v -}}
{{- if $v.sentinel -}}
apiVersion: v1
kind: Service
metadata:
  name: {{ $v.name }}-headless
  namespace: {{ .ctx.Release.Namespace }}
  labels:
    {{- include "valkeyInstance.labels" . | nindent 4 }}
spec:
  type: ClusterIP
  clusterIP: None
  # Lets peers resolve each other during bootstrap, before readiness passes.
  publishNotReadyAddresses: true
  ports:
    - port: 6379
      targetPort: valkey
      protocol: TCP
      name: valkey
    - port: 26379
      targetPort: sentinel
      protocol: TCP
      name: sentinel
  selector:
    {{- include "valkeyInstance.selectorLabels" $v | nindent 4 }}
---
apiVersion: v1
kind: Service
metadata:
  name: {{ $v.name }}-sentinel
  namespace: {{ .ctx.Release.Namespace }}
  labels:
    {{- include "valkeyInstance.labels" . | nindent 4 }}
spec:
  type: ClusterIP
  ports:
    - port: 26379
      targetPort: sentinel
      protocol: TCP
      name: sentinel
  selector:
    {{- include "valkeyInstance.selectorLabels" $v | nindent 4 }}
{{- else -}}
apiVersion: v1
kind: Service
metadata:
  name: {{ $v.name }}
  namespace: {{ .ctx.Release.Namespace }}
  labels:
    {{- include "valkeyInstance.labels" . | nindent 4 }}
spec:
  type: ClusterIP
  ports:
    - port: 6379
      targetPort: valkey
      protocol: TCP
      name: valkey
  selector:
    {{- include "valkeyInstance.selectorLabels" $v | nindent 4 }}
{{- end }}
{{- end }}
