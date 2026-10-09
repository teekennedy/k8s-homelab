# Sandbox image: the upstream agent server plus the two CLIs a sandbox needs to
# run Dagger on the shared engine over `kube-pod://`, and a newer Claude Code
# ACP adapter installed over the one in the image's /acp-node (which its
# /usr/local/bin/claude-agent-acp wrapper runs), so a sandbox installs nothing
# at start. Build and push, then set `sandboxSpecs.image` in values.yaml; see
# README.md, "Sandbox image".
#   docker buildx build --platform linux/amd64 -t ghcr.io/teekennedy/openhands-sandbox:<tag> --push files/sandbox
FROM ghcr.io/openhands/agent-server:1.53.0-python

# renovate: datasource=github-releases depName=kubernetes/kubernetes
ARG KUBECTL_VERSION=v1.36.0
# renovate: datasource=npm depName=@agentclientprotocol/claude-agent-acp
ARG CLAUDE_ADAPTER_VERSION=0.87.0
# Must match the engine (k8s/platform/dagger-engine/values.yaml), so Renovate
# does not track it on its own.
ARG DAGGER_VERSION=0.21.10
ARG TARGETARCH

USER root
RUN set -eux; \
    curl -fsSLo /usr/local/bin/kubectl \
      "https://dl.k8s.io/release/${KUBECTL_VERSION}/bin/linux/${TARGETARCH}/kubectl"; \
    chmod +x /usr/local/bin/kubectl; \
    curl -fsSL https://dl.dagger.io/dagger/install.sh \
      | DAGGER_VERSION="${DAGGER_VERSION}" BIN_DIR=/usr/local/bin sh; \
    PATH=/acp-node/bin:$PATH npm install --global --prefix /acp-node \
      --no-fund --no-audit --loglevel=error \
      "@agentclientprotocol/claude-agent-acp@${CLAUDE_ADAPTER_VERSION}"; \
    kubectl version --client; \
    dagger version; \
    grep -q "\"version\": \"${CLAUDE_ADAPTER_VERSION}\"" \
      /acp-node/lib/node_modules/@agentclientprotocol/claude-agent-acp/package.json
USER 10001
