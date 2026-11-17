# Sandbox image: the upstream agent server plus the CLIs a sandbox needs (kubectl,
# and dagger to run on the shared engine over `kube-pod://`), the Grafana MCP
# server, and a newer Claude Code ACP adapter installed over the one in the
# image's /acp-node (which its /usr/local/bin/claude-agent-acp wrapper runs), so
# a sandbox installs nothing at start. Released as
# ghcr.io/teekennedy/k8s-homelab:openhands-sandbox-<version> by any conventional
# commit that touches this directory; see README.md, "Sandbox image".
FROM ghcr.io/openhands/agent-server:1.62.0-python@sha256:bd35862a12dca128715c77b824b6afc2091e3071709032249c4ec39677afc0c1


# renovate: datasource=github-releases depName=kubernetes/kubernetes
ARG KUBECTL_VERSION=v1.37.2
# renovate: datasource=npm depName=@agentclientprotocol/claude-agent-acp
ARG CLAUDE_ADAPTER_VERSION=0.104.0
# renovate: datasource=github-releases depName=grafana/mcp-grafana
ARG MCP_GRAFANA_VERSION=v2.2.0
# Must match the engine (k8s/platform/dagger-engine/values.yaml), so Renovate
# does not track it on its own.
ARG DAGGER_VERSION=1.0.0-beta.27
ARG TARGETARCH

USER root
RUN set -eux; \
    curl -fsSLo /usr/local/bin/kubectl \
      "https://dl.k8s.io/release/${KUBECTL_VERSION}/bin/linux/${TARGETARCH}/kubectl"; \
    chmod +x /usr/local/bin/kubectl; \
    case "${TARGETARCH}" in amd64) grafana_arch=x86_64 ;; *) grafana_arch="${TARGETARCH}" ;; esac; \
    curl -fsSL "https://github.com/grafana/mcp-grafana/releases/download/${MCP_GRAFANA_VERSION}/mcp-grafana_Linux_${grafana_arch}.tar.gz" \
      | tar -xz -C /usr/local/bin mcp-grafana; \
    curl -fsSL https://dl.dagger.io/dagger/install.sh \
      | DAGGER_VERSION="${DAGGER_VERSION}" BIN_DIR=/usr/local/bin sh; \
    PATH=/acp-node/bin:$PATH npm install --global --prefix /acp-node \
      --no-fund --no-audit --loglevel=error \
      "@agentclientprotocol/claude-agent-acp@${CLAUDE_ADAPTER_VERSION}"; \
    kubectl version --client; \
    dagger version; \
    mcp-grafana --help >/dev/null; \
    grep -q "\"version\": \"${CLAUDE_ADAPTER_VERSION}\"" \
      /acp-node/lib/node_modules/@agentclientprotocol/claude-agent-acp/package.json
USER 10001

ARG VERSION=dev
ARG REVISION=unknown
LABEL org.opencontainers.image.title="OpenHands sandbox" \
      org.opencontainers.image.description="OpenHands agent server with kubectl, Dagger, the Grafana MCP server, and the Claude Code ACP adapter." \
      org.opencontainers.image.source="https://github.com/teekennedy/k8s-homelab" \
      org.opencontainers.image.url="https://github.com/teekennedy/k8s-homelab" \
      org.opencontainers.image.documentation="https://github.com/teekennedy/k8s-homelab/tree/main/k8s/apps/openhands" \
      org.opencontainers.image.licenses="MIT" \
      org.opencontainers.image.version="$VERSION" \
      org.opencontainers.image.revision="$REVISION"
