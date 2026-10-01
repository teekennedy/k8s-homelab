{{/*
Expand the name of the chart.
*/}}
{{- define "openhands.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Create a default fully qualified app name.
We truncate at 63 chars because some Kubernetes name fields are limited to this (by the DNS naming spec).
If release name contains chart name it will be used as a full name.
*/}}
{{- define "openhands.fullname" -}}
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
{{- define "openhands.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Common labels
*/}}
{{- define "openhands.labels" -}}
helm.sh/chart: {{ include "openhands.chart" . }}
{{ include "openhands.selectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{/*
Selector labels
*/}}
{{- define "openhands.selectorLabels" -}}
app.kubernetes.io/name: {{ include "openhands.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{/*
Selector labels for the canvas pod alone. The labels above identify the whole
release, so every sibling workload in it carries them too; anything that means
"the entry point" — its Service, its NetworkPolicy — has to select on this.
*/}}
{{- define "openhands.canvasSelectorLabels" -}}
{{ include "openhands.selectorLabels" . }}
app.kubernetes.io/component: canvas
{{- end }}

{{/*
Create the name of the service account to use
*/}}
{{- define "openhands.serviceAccountName" -}}
{{- if .Values.serviceAccount.create }}
{{- default (include "openhands.fullname" .) .Values.serviceAccount.name }}
{{- else }}
{{- default "default" .Values.serviceAccount.name }}
{{- end }}
{{- end }}

{{/*
ConfigMap volume `items` restoring the directory layout the flat ConfigMap keys
had to give up. Kept next to the ConfigMap's own glob in
templates/configmap-skills.yaml so adding a file under files/skills/ needs no
edit in either place.
*/}}
{{- define "openhands.skillVolumeItems" -}}
{{- range $path, $_ := .Files.Glob "files/skills/*/*" }}
- key: {{ printf "%s.%s" (base (dir $path)) (base $path) }}
  path: {{ printf "%s/%s" (base (dir $path)) (base $path) }}
{{- end }}
{{- end }}

{{/*
Selector labels for the app server pod. See canvasSelectorLabels for why every
workload needs a component of its own.
*/}}
{{- define "openhands.appServerSelectorLabels" -}}
{{ include "openhands.selectorLabels" . }}
app.kubernetes.io/component: app-server
{{- end }}

{{/*
Labels on every sandbox pod the app server creates, whatever its spec. The
per-spec NetworkPolicy adds openhands.msng.to/sandbox-spec to these.
*/}}
{{- define "openhands.runtimeSelectorLabels" -}}
{{ include "openhands.selectorLabels" . }}
app.kubernetes.io/component: runtime
{{- end }}

{{/*
ConfigMap volume `items` for the app server's source, restoring the package
directory that flat ConfigMap keys cannot express. Mirrors the key scheme in
templates/app-server.yaml.
*/}}
{{- define "openhands.appServerSourceItems" -}}
- key: pyproject.toml
  path: pyproject.toml
- key: uv.lock
  path: uv.lock
{{- range $path, $_ := .Files.Glob "files/app-server/app_server/*.py" }}
- key: {{ printf "app_server.%s" (base $path) }}
  path: {{ printf "app_server/%s" (base $path) }}
{{- end }}
{{- end }}
