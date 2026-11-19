{{/*
Expand the name of the chart.
*/}}
{{- define "hermes-agent.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Create a default fully qualified app name.
We truncate at 63 chars because some Kubernetes name fields are limited to this (by the DNS naming spec).
If release name contains chart name it will be used as a full name.
*/}}
{{- define "hermes-agent.fullname" -}}
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
{{- define "hermes-agent.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Common labels
*/}}
{{- define "hermes-agent.labels" -}}
helm.sh/chart: {{ include "hermes-agent.chart" . }}
{{ include "hermes-agent.selectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{/*
Selector labels
*/}}
{{- define "hermes-agent.selectorLabels" -}}
app.kubernetes.io/name: {{ include "hermes-agent.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{/*
Release-wide labels select every workload in the chart. Select one workload
with these, never with selectorLabels alone.
*/}}
{{- define "hermes-agent.gatewaySelectorLabels" -}}
{{ include "hermes-agent.selectorLabels" . }}
app.kubernetes.io/component: gateway
{{- end }}

{{- define "hermes-agent.sandboxSelectorLabels" -}}
{{ include "hermes-agent.selectorLabels" . }}
app.kubernetes.io/component: sandbox
{{- end }}

{{- define "hermes-agent.relaySelectorLabels" -}}
{{ include "hermes-agent.selectorLabels" . }}
app.kubernetes.io/component: relay
{{- end }}

{{/* The iOS relay's StatefulSet, Service, Secret and route. */}}
{{- define "hermes-agent.relayName" -}}
{{ include "hermes-agent.fullname" . }}-relay
{{- end }}

{{/* The iOS connector's state: its relay credential and the phone's sensors. */}}
{{- define "hermes-agent.connectorHome" -}}
/opt/data/hermes-mobile
{{- end }}

{{- define "hermes-agent.image" -}}
{{ .Values.image.repository }}:{{ .Values.image.tag | default (printf "v%s" .Chart.AppVersion) }}
{{- end }}

{{/* The sandbox StatefulSet and its headless Service. */}}
{{- define "hermes-agent.sandboxName" -}}
{{ include "hermes-agent.fullname" . }}-sandbox
{{- end }}

{{/* The sandbox's home claim, from its StatefulSet's volumeClaimTemplates. */}}
{{- define "hermes-agent.sandboxHomeClaim" -}}
home-{{ include "hermes-agent.sandboxName" . }}-0
{{- end }}

{{- define "hermes-agent.sandboxHost" -}}
{{ include "hermes-agent.sandboxName" . }}.{{ .Release.Namespace }}.svc.cluster.local
{{- end }}

{{/*
The managed config.yaml: every key here is pinned, and wins over the
config.yaml on the data volume. `config` is merged last.
*/}}
{{- define "hermes-agent.managedConfig" -}}
{{- $aux := dict "provider" .Values.model.provider "model" .Values.model.auxiliary -}}
{{- $base := dict
  "model" (dict "provider" .Values.model.provider "default" .Values.model.default)
  "auxiliary" (dict "compression" $aux "approval" $aux "title_generation" $aux "vision" $aux)
  "timezone" .Values.timezone
  "approvals" (dict "mode" .Values.approvals.mode)
  "terminal" (dict
    "backend" "ssh"
    "ssh_host" (include "hermes-agent.sandboxHost" .)
    "ssh_port" (int .Values.sandbox.port)
    "ssh_user" "agent"
    "ssh_key" "/opt/data/.ssh/id_rsa"
    "cwd" "."
    "persistent_shell" true)
  "dashboard" (dict
    "public_url" (printf "https://%s" .Values.host)
    "trusted_proxies" (list .Values.networkPolicy.podCidr)
    "oauth" (dict
      "provider" "self-hosted"
      "self_hosted" (dict "issuer" .Values.oidc.issuer "client_id" .Values.oidc.clientId)))
-}}
{{- if .Values.connector.enabled }}
{{- $_ := set $base "mcp_servers" (dict "hermes_mobile" (dict
  "command" "/opt/hermes-mobile/connector/bin/hermes-mobile-mcp"
  "args" (list)
  "env" (dict "HERMES_MOBILE_CONNECTOR_HOME" (include "hermes-agent.connectorHome" .)))) }}
{{- end }}
{{- toYaml (mustMergeOverwrite $base (deepCopy .Values.config)) }}
{{- end }}

{{/* DNS, for every pod with an egress policy. */}}
{{- define "hermes-agent.egressDNS" -}}
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
{{- define "hermes-agent.egressInternet" -}}
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
    - port: 80
      protocol: TCP
{{- end }}
