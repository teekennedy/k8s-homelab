{{/*
Labels for the resources this chart renders itself. They carry the same
name/instance pair as the seerr-chart dependency's, so the whole release
selects together.
*/}}
{{- define "seerr.ownLabels" -}}
app.kubernetes.io/name: {{ .Chart.Name }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}
