{{- define "wake-word-trainer.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{- define "wake-word-trainer.fullname" -}}
{{- if .Values.fullnameOverride }}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- $name := default .Chart.Name .Values.nameOverride }}
{{- if contains $name .Release.Name }}
{{- .Release.Name | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" }}
{{- end }}
{{- end }}
{{- end }}

{{- define "wake-word-trainer.labels" -}}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{ include "wake-word-trainer.selectorLabels" . }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{/*
Release-wide: every workload in the chart carries these. Select one workload
with the per-component helpers below, never with this alone.
*/}}
{{- define "wake-word-trainer.selectorLabels" -}}
app.kubernetes.io/name: {{ include "wake-word-trainer.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{- define "wake-word-trainer.collectorSelectorLabels" -}}
{{ include "wake-word-trainer.selectorLabels" . }}
app.kubernetes.io/component: collector
{{- end }}

{{- define "wake-word-trainer.trainerSelectorLabels" -}}
{{ include "wake-word-trainer.selectorLabels" . }}
app.kubernetes.io/component: trainer
{{- end }}

{{- define "wake-word-trainer.seedSelectorLabels" -}}
{{ include "wake-word-trainer.selectorLabels" . }}
app.kubernetes.io/component: seed
{{- end }}

{{- define "wake-word-trainer.image" -}}
{{ .Values.image.repository }}:{{ .Values.image.tag }}
{{- end }}

{{- define "wake-word-trainer.collectorUrl" -}}
http://{{ include "wake-word-trainer.fullname" . }}.{{ .Release.Namespace }}.svc.cluster.local:{{ .Values.collector.port }}
{{- end }}

{{- define "wake-word-trainer.cronjobName" -}}
{{ include "wake-word-trainer.fullname" . }}-nightly
{{- end }}

{{/* Non-root, read-only, nothing to escalate to. */}}
{{- define "wake-word-trainer.containerSecurityContext" -}}
allowPrivilegeEscalation: false
readOnlyRootFilesystem: true
capabilities:
  drop:
    - ALL
{{- end }}

{{- define "wake-word-trainer.podSecurityContext" -}}
runAsNonRoot: true
runAsUser: {{ .Values.uid }}
runAsGroup: {{ .Values.uid }}
fsGroup: {{ .Values.uid }}
fsGroupChangePolicy: OnRootMismatch
seccompProfile:
  type: RuntimeDefault
{{- end }}

{{/* Where the pipeline keeps its data, for every pod that runs it. */}}
{{- define "wake-word-trainer.pipelineEnv" -}}
- name: OKAY_GLADOS_WORK_DIR
  value: /data/work
- name: WWT_CLIPS_DIR
  value: /data/clips
- name: WWT_RUNS_DIR
  value: /data/runs
- name: HF_HOME
  value: /data/work/hf-cache
{{- if .Values.phoneRecordings.enabled }}
- name: OKAY_GLADOS_RECORDINGS_DIR
  value: /data/clips/phone
{{- else }}
# No phone recordings: point the pipeline at an empty directory rather than
# the image's source tree.
- name: OKAY_GLADOS_RECORDINGS_DIR
  value: /tmp/no-phone-recordings
{{- end }}
{{- end }}

{{/* DNS, for every pod with an egress policy. */}}
{{- define "wake-word-trainer.egressDNS" -}}
- to:
    - namespaceSelector:
        matchLabels:
          kubernetes.io/metadata.name: kube-system
  ports:
    - port: 53
      protocol: UDP
    - port: 53
      protocol: TCP
{{- end }}

{{/* The internet, RFC1918 excluded. */}}
{{- define "wake-word-trainer.egressInternet" -}}
- to:
    - ipBlock:
        cidr: 0.0.0.0/0
        except:
          - 10.0.0.0/8
          - 172.16.0.0/12
          - 192.168.0.0/16
  ports:
    - port: 443
      protocol: TCP
{{- end }}
