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

{{/*
NetworkPolicy egress rules shared by every policy in this chart. Verified on
this cluster's netpol controller (k3s/kube-router): a portless egress rule is
enforced as allow-nothing once the same policy has rules that do name ports,
so every rule here names its ports.
*/}}
{{- define "openhands.egressDNS" -}}
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

{{/*
The forge. Its name resolves to a MetalLB VIP, but traffic raised inside the
cluster is DNAT'd to the ingress controller's pod before egress policy is
evaluated — so the destination has to be Traefik, on its own websecure
container port rather than the 443 the Service publishes. The ipBlock stays for
a genuine off-cluster LAN address.
*/}}
{{- define "openhands.egressForge" -}}
- to:
    - namespaceSelector:
        matchLabels:
          kubernetes.io/metadata.name: {{ .Values.traefikNamespace }}
      podSelector:
        matchLabels:
          app.kubernetes.io/name: traefik
  ports:
    - port: websecure
      protocol: TCP
- to:
    - ipBlock:
        cidr: {{ .Values.networkPolicy.lanCidr }}
  ports:
    - port: 443
      protocol: TCP
{{- end }}

{{/*
HTTPS to the internet: the model API and package registries. RFC1918 is
excluded, so any rule naming the LAN is the only route to it.
*/}}
{{- define "openhands.egressInternet" -}}
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

{{/*
Forge and CI environment for anything the agent runs in: the gitconfig's
credential helper reads FORGEJO_USERNAME/FORGEJO_TOKEN, and the
forgejo-iterate skill reads the rest.
*/}}
{{- define "openhands.forgeEnv" -}}
- name: GIT_CONFIG_GLOBAL
  value: {{ .Values.forge.gitConfigPath | quote }}
- name: FORGEJO_URL
  value: {{ .Values.forge.url | quote }}
- name: FORGEJO_OWNER
  value: {{ .Values.forge.owner | quote }}
- name: FORGEJO_REPO
  value: {{ .Values.forge.repo | quote }}
- name: FORGEJO_USERNAME
  valueFrom:
    secretKeyRef:
      name: {{ .Values.forge.secretName }}
      key: username
- name: FORGEJO_TOKEN
  valueFrom:
    secretKeyRef:
      name: {{ .Values.forge.secretName }}
      key: token
- name: WOODPECKER_URL
  value: {{ .Values.ci.url | quote }}
- name: WOODPECKER_TOKEN
  valueFrom:
    secretKeyRef:
      name: {{ .Values.ci.secretName }}
      key: token
      # Provisioned by a Job that may not have run yet on a cold bootstrap.
      # Optional because only the log-reading half of the skill needs it.
      optional: true
{{- end }}

{{/*
Seeds skills into the agent's home. They land in two directories because two
different readers look for them, and neither reads the other's path:
  ~/.agents/skills   the agent server's own user-skill search path, so the
                     skill is listed in the UI and reaches the agent context.
  ~/.claude/skills   read natively by the Claude Code CLI an ACP session spawns.
Copied rather than mounted: a ConfigMap mounted at either path would make that
directory root-owned and read-only, and the agent writes its own state beside
the skills in both. The glob skips the ConfigMap volume's own ..data and
..<timestamp> entries; -L resolves the symlink each remaining entry actually is.
*/}}
{{- define "openhands.seedSkillsScript" -}}
set -eu
mkdir -p "$HOME/.agents/skills" "$HOME/.claude/skills"
for src in /opt/openhands-skills/*/; do
  [ -d "$src" ] || continue
  cp -rLf "$src" "$HOME/.agents/skills/"
  cp -rLf "$src" "$HOME/.claude/skills/"
done
{{- end }}
