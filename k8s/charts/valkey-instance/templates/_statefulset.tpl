{{/*
The workload, a StatefulSet in both topologies.

sentinel: one primary + N-1 replicas, with a Sentinel co-located in every pod
(quorum is a majority). Each pod keeps its AOF on a strict-local volume and
only schedules onto nodes with a Longhorn disk: with exactly as many such
nodes as pods, an evicted pod waits for its node to come back instead of
moving, so its volume detaches and the drain is never blocked on its replica.

standalone: one in-memory pod. A StatefulSet rather than a Deployment only so
both topologies share one template; with no volume it behaves like a
Recreate Deployment.

No Replace sync option: most fields roll in place with the preStop failover
below. volumeClaimTemplates, selector and serviceName are immutable; see the
chart README for changing them.
Takes (dict "v" $settings "ctx" $).
*/}}
{{- define "valkeyInstance.statefulSet" -}}
{{- $v := .v -}}
apiVersion: apps/v1
kind: StatefulSet
metadata:
  name: {{ $v.name }}
  namespace: {{ .ctx.Release.Namespace }}
  labels:
    {{- include "valkeyInstance.labels" . | nindent 4 }}
spec:
  serviceName: {{ $v.name }}{{ if $v.sentinel }}-headless{{ end }}
  replicas: {{ $v.replicas }}
  # OrderedReady so <name>-0 (the bootstrap primary) is Ready before the
  # replicas start and discover it.
  podManagementPolicy: OrderedReady
  selector:
    matchLabels:
      {{- include "valkeyInstance.selectorLabels" $v | nindent 6 }}
  updateStrategy:
    type: RollingUpdate
  template:
    metadata:
      labels:
        {{- include "valkeyInstance.labels" . | nindent 8 }}
      annotations:
        checksum/config: {{ include "valkeyInstance.configData" $v | sha256sum }}
    spec:
      automountServiceAccountToken: false
      enableServiceLinks: false
      securityContext:
        runAsNonRoot: true
        runAsUser: 1000
        runAsGroup: 1000
        fsGroup: 1000
        seccompProfile:
          type: RuntimeDefault
      {{- if $v.sentinel }}
      # Room for the preStop failover below plus Valkey's own shutdown.
      terminationGracePeriodSeconds: 60
      {{- end }}
      {{- with $v.nodeSelector }}
      nodeSelector:
        {{- toYaml . | nindent 8 }}
      {{- end }}
      {{- if $v.sentinel }}
      # One pod per node, so a single node loss never takes more than one
      # replica (and never breaks Sentinel quorum).
      affinity:
        podAntiAffinity:
          requiredDuringSchedulingIgnoredDuringExecution:
            - labelSelector:
                matchLabels:
                  {{- include "valkeyInstance.selectorLabels" $v | nindent 18 }}
              topologyKey: kubernetes.io/hostname
      {{- end }}
      containers:
        - name: valkey
          image: {{ $v.image }}
          imagePullPolicy: IfNotPresent
          securityContext:
            {{- include "valkeyInstance.containerSecurityContext" . | nindent 12 }}
          ports:
            - containerPort: 6379
              name: valkey
              protocol: TCP
          env:
            - name: POD_NAMESPACE
              valueFrom:
                fieldRef:
                  fieldPath: metadata.namespace
            {{- include "valkeyInstance.passwordEnv" $v | nindent 12 }}
            # maxmemory is the memory request, in bytes: below the limit, so
            # eviction starts before the kernel kills the container.
            - name: VALKEY_MAXMEMORY
              valueFrom:
                resourceFieldRef:
                  containerName: valkey
                  resource: requests.memory
                  divisor: "1"
          command:
            - sh
            - /etc/valkey/valkey-entrypoint.sh
          volumeMounts:
            - name: config
              mountPath: /etc/valkey
              readOnly: true
            - name: runtime
              mountPath: /run/valkey
            {{- if $v.sentinel }}
            - name: data
              mountPath: /data
            {{- end }}
          {{- if $v.sentinel }}
          # If this pod is the primary, hand the role to a replica through
          # Sentinel before Valkey stops, so clients see a coordinated
          # switchover instead of waiting out down-after-milliseconds. Gives up
          # after 25s (e.g. no healthy replica) and shuts down normally.
          lifecycle:
            preStop:
              exec:
                command:
                  - sh
                  - -c
                  - |
                    role() { REDISCLI_AUTH="$VALKEY_PASSWORD" valkey-cli role | head -1; }
                    [ "$(role)" = master ] || exit 0
                    REDISCLI_AUTH="$SENTINEL_PASSWORD" valkey-cli -p 26379 sentinel failover {{ $v.masterName }}
                    i=0
                    while [ "$(role)" = master ] && [ $i -lt 25 ]; do sleep 1; i=$((i+1)); done
          {{- end }}
          livenessProbe:
            exec:
              command:
                - sh
                - -c
                - REDISCLI_AUTH="$VALKEY_PASSWORD" valkey-cli ping
            initialDelaySeconds: 30
            periodSeconds: 10
            timeoutSeconds: 5
            failureThreshold: 3
          readinessProbe:
            exec:
              command:
                - sh
                - -c
                - REDISCLI_AUTH="$VALKEY_PASSWORD" valkey-cli ping
            periodSeconds: 5
            timeoutSeconds: 3
            failureThreshold: 30
          resources:
            {{- toYaml $v.resources.valkey | nindent 12 }}
        {{- if $v.sentinel }}
        - name: sentinel
          image: {{ $v.image }}
          imagePullPolicy: IfNotPresent
          securityContext:
            {{- include "valkeyInstance.containerSecurityContext" . | nindent 12 }}
          ports:
            - containerPort: 26379
              name: sentinel
              protocol: TCP
          env:
            - name: POD_NAMESPACE
              valueFrom:
                fieldRef:
                  fieldPath: metadata.namespace
            {{- include "valkeyInstance.passwordEnv" $v | nindent 12 }}
          command:
            - sh
            - /etc/valkey/sentinel-entrypoint.sh
          volumeMounts:
            - name: config
              mountPath: /etc/valkey
              readOnly: true
            - name: sentinel-conf
              mountPath: /sentinel
          # The valkey container's preStop asks this Sentinel to run the
          # failover, and all preStop hooks start at once, so keep Sentinel up
          # until the local Valkey is no longer primary.
          lifecycle:
            preStop:
              exec:
                command:
                  - sh
                  - -c
                  - |
                    i=0
                    while [ "$(REDISCLI_AUTH="$VALKEY_PASSWORD" valkey-cli role | head -1)" = master ] && [ $i -lt 30 ]; do sleep 1; i=$((i+1)); done
          livenessProbe:
            exec:
              command:
                - sh
                - -c
                - REDISCLI_AUTH="$SENTINEL_PASSWORD" valkey-cli -p 26379 ping
            initialDelaySeconds: 30
            periodSeconds: 10
            timeoutSeconds: 5
            failureThreshold: 3
          readinessProbe:
            exec:
              command:
                - sh
                - -c
                - REDISCLI_AUTH="$SENTINEL_PASSWORD" valkey-cli -p 26379 ping
            periodSeconds: 5
            timeoutSeconds: 3
            failureThreshold: 30
          resources:
            {{- toYaml $v.resources.sentinel | nindent 12 }}
        {{- end }}
        {{- if $v.metrics }}
        - name: exporter
          image: {{ $v.exporterImage }}
          imagePullPolicy: IfNotPresent
          securityContext:
            {{- include "valkeyInstance.containerSecurityContext" . | nindent 12 }}
          ports:
            - containerPort: 9121
              name: metrics
              protocol: TCP
          env:
            - name: REDIS_ADDR
              value: redis://localhost:6379
            - name: REDIS_PASSWORD
              valueFrom:
                secretKeyRef:
                  name: {{ $v.auth.secretName }}
                  key: {{ $v.auth.passwordKey }}
          livenessProbe:
            httpGet:
              path: /health
              port: metrics
            periodSeconds: 30
          readinessProbe:
            httpGet:
              path: /health
              port: metrics
            periodSeconds: 10
          resources:
            {{- toYaml $v.resources.exporter | nindent 12 }}
        {{- end }}
      volumes:
        - name: config
          configMap:
            name: {{ $v.name }}-config
        # The runtime config holding the passwords. In memory, never on disk.
        - name: runtime
          emptyDir:
            medium: Memory
            sizeLimit: 1Mi
        {{- if $v.sentinel }}
        # Writable location for Sentinel's self-rewritten config.
        - name: sentinel-conf
          emptyDir:
            medium: Memory
            sizeLimit: 1Mi
        {{- end }}
  {{- if $v.sentinel }}
  volumeClaimTemplates:
    - metadata:
        name: data
        labels:
          recurring-job.longhorn.io/source: "enabled"
          recurring-job-group.longhorn.io/ephemeral: "enabled"
      spec:
        accessModes: ["ReadWriteOnce"]
        storageClassName: {{ $v.persistence.storageClass }}
        resources:
          requests:
            storage: {{ $v.persistence.size }}
  {{- end }}
{{- end }}
