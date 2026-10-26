{{- define "restic.labels" -}}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
app.kubernetes.io/name: restic
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{/*
Selects one component's pods for one repository. Takes a dict with
`root`, `component` and `repo`.
*/}}
{{- define "restic.selectorLabels" -}}
app.kubernetes.io/name: restic
app.kubernetes.io/instance: {{ .root.Release.Name }}
{{ include "restic.componentLabels" . }}
{{- end }}

{{- define "restic.componentLabels" -}}
app.kubernetes.io/component: {{ .component }}
restic.msng.to/repository: {{ .repo }}
{{- end }}

{{- define "restic.podSecurityContext" -}}
runAsNonRoot: true
runAsUser: 65534
runAsGroup: 65534
fsGroup: 65534
seccompProfile:
  type: RuntimeDefault
{{- end }}

{{- define "restic.containerSecurityContext" -}}
allowPrivilegeEscalation: false
readOnlyRootFilesystem: true
capabilities:
  drop: ["ALL"]
{{- end }}

{{/*
Environment and volumes for reaching one repository. Takes a dict with
`root`, `repo` and `url`.
*/}}
{{- define "restic.repoEnv" -}}
- name: RESTIC_REPOSITORY
  value: {{ .url | quote }}
- name: RESTIC_PASSWORD_FILE
  value: /run/secrets/restic/password
- name: RESTIC_CACHE_DIR
  value: /cache
- name: AWS_ACCESS_KEY_ID
  valueFrom:
    secretKeyRef:
      name: {{ .root.Values.secretName }}
      key: AWS_ACCESS_KEY_ID
- name: AWS_SECRET_ACCESS_KEY
  valueFrom:
    secretKeyRef:
      name: {{ .root.Values.secretName }}
      key: AWS_SECRET_ACCESS_KEY
{{- end }}

{{- define "restic.repoVolumeMounts" -}}
- name: password
  mountPath: /run/secrets/restic
  readOnly: true
- name: cache
  mountPath: /cache
{{- end }}

{{- define "restic.repoVolumes" -}}
- name: password
  secret:
    secretName: {{ .root.Values.secretName }}
    defaultMode: 0440
    items:
      - key: {{ .repo }}
        path: password
- name: cache
  emptyDir: {}
{{- end }}
