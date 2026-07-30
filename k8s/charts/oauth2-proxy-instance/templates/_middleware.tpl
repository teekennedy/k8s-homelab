{{/*
The Traefik middleware pair for a forwardAuth-style instance: oauth2-proxy only
answers the auth subrequest, and Traefik routes the actual traffic (including
request bodies) straight to the app. Reverse-proxy instances — where
oauth2-proxy itself owns the hostname and forwards upstream — need neither of
these; leave `middleware.enabled` false there.

Two details are load-bearing and easy to lose:

  * Traefik DELETES each header named in authResponseHeaders from the incoming
    request before copying the auth response's values in, so a client cannot
    smuggle its own X-Auth-Request-* headers past the middleware. That property
    is what lets an app trust those headers as identity.
  * statusRewrites "401": 302. The errors middleware serves the error page
    service's *body* but keeps the ORIGINAL status code, so oauth2-proxy's 302
    reaches the browser as a 401 carrying a `Found.` link that nothing follows.
    Rewriting 401 -> 302 makes the Location header actually take effect.

The -signin middleware must be listed BEFORE the -forwardauth middleware on the
Ingress so it wraps (and therefore sees the response of) that middleware.

Usage:
  {{- include "oauth2ProxyInstance.middleware" . }}

Values (under .Values.oauth2ProxyInstance.middleware):
  enabled              set false for reverse-proxy instances
  serviceName          required, must equal oauth2-proxy.fullnameOverride
  servicePort          defaults to 80 (oauth2-proxy.service.portNumber)
  authResponseHeaders  required; deliberately has no default, because a header
                       the app needs but nobody listed here fails as "anonymous"
                       rather than as an error
  namePrefix           defaults to .Release.Name; yields <prefix>-forwardauth
                       and <prefix>-signin
*/}}
{{- define "oauth2ProxyInstance.middleware" -}}
{{- $v := .Values.oauth2ProxyInstance | default dict -}}
{{- $mw := $v.middleware | default dict -}}
{{- if $mw.enabled -}}
{{- if not $mw.serviceName -}}
{{- fail "oauth2ProxyInstance.middleware.serviceName is required and must equal oauth2-proxy.fullnameOverride" -}}
{{- end -}}
{{- if not $mw.authResponseHeaders -}}
{{- fail "oauth2ProxyInstance.middleware.authResponseHeaders is required; list every identity header the protected app reads" -}}
{{- end -}}
{{- $name := $mw.namePrefix | default .Release.Name -}}
{{- $port := $mw.servicePort | default 80 -}}
---
# forwardAuth against this chart's dedicated oauth2-proxy.
apiVersion: traefik.io/v1alpha1
kind: Middleware
metadata:
  name: {{ $name }}-forwardauth
  namespace: {{ .Release.Namespace }}
  {{- with $v.labelsTemplate }}
  labels:
    {{- include . $ | nindent 4 }}
  {{- end }}
spec:
  forwardAuth:
    address: http://{{ $mw.serviceName }}.{{ .Release.Namespace }}.svc.cluster.local/oauth2/auth
    trustForwardHeader: true
    authResponseHeaders:
      {{- toYaml $mw.authResponseHeaders | nindent 6 }}
---
# Catches the 401 from the forwardAuth middleware and redirects the browser into
# the Authelia login flow instead of showing a bare error.
apiVersion: traefik.io/v1alpha1
kind: Middleware
metadata:
  name: {{ $name }}-signin
  namespace: {{ .Release.Namespace }}
  {{- with $v.labelsTemplate }}
  labels:
    {{- include . $ | nindent 4 }}
  {{- end }}
spec:
  errors:
    status:
      - "401"
    service:
      name: {{ $mw.serviceName }}
      port: {{ $port }}
    query: "/oauth2/sign_in?rd={url}"
    statusRewrites:
      "401": 302
{{/* Trailing newline is deliberate — see the note in _secret.tpl. */}}
{{ end -}}
{{- end -}}
