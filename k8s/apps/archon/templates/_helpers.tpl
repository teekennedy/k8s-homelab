{{/*
Expand the name of the chart.
*/}}
{{- define "archon.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Create a default fully qualified app name.
We truncate at 63 chars because some Kubernetes name fields are limited to this (by the DNS naming spec).
If release name contains chart name it will be used as a full name.
*/}}
{{- define "archon.fullname" -}}
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
{{- define "archon.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Common labels
*/}}
{{- define "archon.labels" -}}
helm.sh/chart: {{ include "archon.chart" . }}
{{ include "archon.selectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{/*
Selector labels
*/}}
{{- define "archon.selectorLabels" -}}
app.kubernetes.io/name: {{ include "archon.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{/*
Create the name of the service account to use
*/}}
{{- define "archon.serviceAccountName" -}}
{{- if .Values.serviceAccount.create }}
{{- default (include "archon.fullname" .) .Values.serviceAccount.name }}
{{- else }}
{{- default "default" .Values.serviceAccount.name }}
{{- end }}
{{- end }}

{{/*
ConfigMap volume `items` that restore the directory layout the flat ConfigMap
keys had to give up (keys cannot contain "/"). Kept next to the ConfigMap's own
globs in templates/configmap-workflow.yaml so adding a file under
files/workflow/ needs no edit in either place.
*/}}
{{- define "archon.workflowVolumeItems" -}}
{{- range $path, $_ := .Files.Glob "files/workflow/*" }}
- key: {{ base $path }}
  path: {{ base $path }}
{{- end }}
{{- range $path, $_ := .Files.Glob "files/workflow/scripts/*" }}
- key: {{ base $path }}
  path: scripts/{{ base $path }}
{{- end }}
{{- end }}
