{{/*
Default-deny for the Valkey pods. Ingress from networkPolicy.clients (and, in
a replication set, the peers) on the Valkey and Sentinel ports, and from
Prometheus on the exporter port. Egress only where a replication set needs
it: DNS, and the peers.

Every egress rule names its ports: on this cluster's netpol controller
(k3s/kube-router), a portless rule in a policy whose other rules name ports
is enforced as allow-nothing, not allow-all.
Takes (dict "v" $settings "ctx" $).
*/}}
{{- define "valkeyInstance.networkPolicy" -}}
{{- $v := .v -}}
apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: {{ $v.name }}
  namespace: {{ .ctx.Release.Namespace }}
  labels:
    {{- include "valkeyInstance.labels" . | nindent 4 }}
spec:
  podSelector:
    matchLabels:
      {{- include "valkeyInstance.selectorLabels" $v | nindent 6 }}
  policyTypes:
    - Ingress
    - Egress
  ingress:
    - from:
        {{- toYaml $v.networkPolicy.clients | nindent 8 }}
        {{- if $v.sentinel }}
        - podSelector:
            matchLabels:
              {{- include "valkeyInstance.selectorLabels" $v | nindent 14 }}
        {{- end }}
      ports:
        - port: valkey
          protocol: TCP
        {{- if $v.sentinel }}
        - port: sentinel
          protocol: TCP
        {{- end }}
    {{- if $v.metrics }}
    - from:
        - namespaceSelector:
            matchLabels:
              kubernetes.io/metadata.name: monitoring-system
          podSelector:
            matchLabels:
              app.kubernetes.io/name: prometheus
      ports:
        - port: metrics
          protocol: TCP
    {{- end }}
  {{- if $v.sentinel }}
  egress:
    - to:
        - namespaceSelector:
            matchLabels:
              kubernetes.io/metadata.name: kube-system
      ports:
        - port: 53
          protocol: UDP
        - port: 53
          protocol: TCP
    # Peers are reached by their headless-Service names, which resolve
    # straight to pod IPs, so a pod selector matches them.
    - to:
        - podSelector:
            matchLabels:
              {{- include "valkeyInstance.selectorLabels" $v | nindent 14 }}
      ports:
        - port: valkey
          protocol: TCP
        - port: sentinel
          protocol: TCP
  {{- else }}
  # A standalone store calls nothing.
  egress: []
  {{- end }}
{{- end }}
