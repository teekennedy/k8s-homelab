{{/*
Expand the name of the chart.
*/}}
{{- define "gauntlet.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Create a default fully qualified app name.
We truncate at 63 chars because some Kubernetes name fields are limited to this (by the DNS naming spec).
If release name contains chart name it will be used as a full name.
*/}}
{{- define "gauntlet.fullname" -}}
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

{{/*
Create chart name and version as used by the chart label.
*/}}
{{- define "gauntlet.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Common labels
*/}}
{{- define "gauntlet.labels" -}}
helm.sh/chart: {{ include "gauntlet.chart" . }}
{{ include "gauntlet.selectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{/*
Selector labels
*/}}
{{- define "gauntlet.selectorLabels" -}}
app.kubernetes.io/name: {{ include "gauntlet.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}


{{/*
Selector labels for one component. This chart runs several workloads, so each
selector must include the component, or one Service would match every pod.
Call with (dict "ctx" $ "component" "name").
*/}}
{{- define "gauntlet.componentSelectorLabels" -}}
{{ include "gauntlet.selectorLabels" .ctx }}
app.kubernetes.io/component: {{ .component }}
{{- end }}

{{/*
Common labels for one component.
*/}}
{{- define "gauntlet.componentLabels" -}}
{{ include "gauntlet.labels" .ctx }}
app.kubernetes.io/component: {{ .component }}
{{- end }}
