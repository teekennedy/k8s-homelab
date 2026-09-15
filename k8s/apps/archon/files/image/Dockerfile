# Sandbox image for the Archon `homelab-sandbox-pr` workflow. Built and pushed
# out of band; see README.md, "The sandbox image" for the build command and why.
#
# Deliberately lightweight: the agent verifies with helm/yamllint locally and
# lets the Woodpecker PR check — which has the full Dagger/Nix toolchain — be
# the real gate.

FROM debian:trixie-slim

# renovate: datasource=github-releases depName=kubernetes/kubernetes
ARG KUBECTL_VERSION=v1.34.3
# renovate: datasource=github-releases depName=helm/helm
ARG HELM_VERSION=v3.19.0
# renovate: datasource=github-releases depName=mikefarah/yq
ARG YQ_VERSION=v4.48.1

SHELL ["/bin/bash", "-o", "pipefail", "-c"]

RUN apt-get update && apt-get install --no-install-recommends -y \
      ca-certificates \
      curl \
      git \
      jq \
      less \
      openssh-client \
      python3 \
      ripgrep \
      yamllint \
    && rm -rf /var/lib/apt/lists/*

# kubectl — the read-only cluster oracle described in the agent prompt.
RUN arch="$(dpkg --print-architecture)" \
    && curl -fsSLo /usr/local/bin/kubectl \
       "https://dl.k8s.io/release/${KUBECTL_VERSION}/bin/linux/${arch}/kubectl" \
    && curl -fsSLo /tmp/kubectl.sha256 \
       "https://dl.k8s.io/release/${KUBECTL_VERSION}/bin/linux/${arch}/kubectl.sha256" \
    && echo "$(cat /tmp/kubectl.sha256)  /usr/local/bin/kubectl" | sha256sum --check \
    && rm -f /tmp/kubectl.sha256 \
    && chmod 0755 /usr/local/bin/kubectl

# helm + yq — what the agent uses to check its own work before CI does.
RUN arch="$(dpkg --print-architecture)" \
    && curl -fsSL "https://get.helm.sh/helm-${HELM_VERSION}-linux-${arch}.tar.gz" \
       | tar -xz -C /tmp "linux-${arch}/helm" \
    && install -m 0755 "/tmp/linux-${arch}/helm" /usr/local/bin/helm \
    && rm -rf "/tmp/linux-${arch}" \
    && curl -fsSLo /usr/local/bin/yq \
       "https://github.com/mikefarah/yq/releases/download/${YQ_VERSION}/yq_linux_${arch}" \
    && chmod 0755 /usr/local/bin/yq

# Claude Code, via Anthropic's native installer. Installed as the agent user so
# it lands somewhere the pod can execute from with a read-only root filesystem.
RUN useradd --create-home --uid 1000 --shell /bin/bash agent
USER agent
ENV HOME=/home/agent
RUN curl -fsSL https://claude.ai/install.sh | bash \
    && test -x "$HOME/.local/bin/claude"

# The pod sets HOME=/work/home (root filesystem is read-only), so the binary
# has to be reachable by PATH rather than under $HOME.
USER root
RUN install -m 0755 /home/agent/.local/bin/claude /usr/local/bin/claude \
    && /usr/local/bin/claude --version

USER agent
WORKDIR /work
# No ENTRYPOINT/CMD: the Sandbox pod template supplies
# /opt/agent/entrypoint.sh from the chart's workflow ConfigMap, so the agent's
# behaviour is versioned with the chart rather than baked into the image.
