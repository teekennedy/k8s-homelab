{{/*
Cross-check Authelia's OIDC client list against oidcClientSecrets.clients.

Every confidential client reads its secret out of the aggregate Secret that the
sync job maintains, so the two lists have to stay in lockstep: a client with no
configured plaintext source would leave Authelia pointing at a key that never
gets written, and a configured source with no client would hash a plaintext
nobody consumes. Failing at render time turns both into an obvious CI error
instead of a broken sync or a silently dead integration.
*/}}
{{- define "authSystem.validateOidcClientSources" -}}
{{- $prefix := printf "/secrets/%s/" .Values.oidcClientSecrets.targetSecret -}}
{{- $configured := .Values.oidcClientSecrets.clients | default dict -}}
{{- $referenced := dict -}}
{{- range $client := .Values.authelia.configMap.identity_providers.oidc.clients -}}
  {{- $path := "" -}}
  {{- if $client.client_secret -}}
    {{- $path = $client.client_secret.path | default "" -}}
  {{- end -}}
  {{- if hasPrefix $prefix $path -}}
    {{- $key := trimPrefix $prefix $path -}}
    {{- $_ := set $referenced $key $client.client_id -}}
    {{- if not (hasKey $configured $key) -}}
      {{- fail (printf "OIDC client %q reads its secret from %s but oidcClientSecrets.clients has no %q entry; add `%s: <namespace>/<secret-name>` so the sync job knows where to find the plaintext" $client.client_id $path $key $key) -}}
    {{- end -}}
  {{- end -}}
{{- end -}}
{{- range $key, $source := $configured -}}
  {{- if not (hasKey $referenced $key) -}}
    {{- fail (printf "oidcClientSecrets.clients has an entry for %q (%s) but no Authelia OIDC client reads %s%s; remove the entry or add the client" $key $source $prefix $key) -}}
  {{- end -}}
{{- end -}}
{{- end -}}

{{/*
CLIENT_SOURCES value for the sync job: `<client_id>=<namespace>/<secret>` pairs.
Comma-joined so the rendered Job stays readable in `kubectl describe`.
*/}}
{{- define "authSystem.oidcClientSources" -}}
{{- $entries := list -}}
{{- range $key, $source := .Values.oidcClientSecrets.clients -}}
  {{- $entries = append $entries (printf "%s=%s" $key $source) -}}
{{- end -}}
{{- join "," $entries -}}
{{- end -}}
