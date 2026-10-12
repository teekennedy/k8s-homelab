# Session image for red-team pods: Claude Code + kubectl + recon tools.
# Build and push, then set `image` in values.yaml.
#   docker build -t <registry>/redteam-session:<tag> -f files/Dockerfile files/
FROM debian:bookworm-slim@sha256:7c7b2c966bc9ee8cedfeef67e0e279108992c77681fa595db4a9d65c06ccc587


RUN apt-get update && apt-get install -y --no-install-recommends \
      ca-certificates curl wget git jq tmux \
      nmap netcat-openbsd dnsutils iputils-ping python3 python3-pip python3-venv \
      openssl coreutils \
    && rm -rf /var/lib/apt/lists/*

# kubectl (for the read-only "oracle" view)
RUN curl -fsSLo /usr/local/bin/kubectl \
      "https://dl.k8s.io/release/$(curl -fsSL https://dl.k8s.io/release/stable.txt)/bin/linux/amd64/kubectl" \
    && chmod +x /usr/local/bin/kubectl

# Node.js + Claude Code CLI
RUN curl -fsSL https://deb.nodesource.com/setup_22.x | bash - \
    && apt-get install -y --no-install-recommends nodejs \
    && npm install -g @anthropic-ai/claude-code \
    && rm -rf /var/lib/apt/lists/*

# Run as a non-root user (the pods set runAsUser: 1000). /work and HOME are owned by
# it so Claude (~/.claude), git, and kubectl caches are writable.
RUN useradd -m -u 1000 -d /home/agent agent \
    && mkdir -p /work && chown agent:agent /work
USER 1000
ENV HOME=/home/agent
WORKDIR /work
# ANTHROPIC_API_KEY (or OAuth) is injected from the redteam-anthropic Secret.

ARG VERSION=dev
ARG REVISION=unknown
LABEL org.opencontainers.image.title="Red-team session" \
      org.opencontainers.image.description="Claude Code, kubectl, and reconnaissance tools for homelab red-team sessions." \
      org.opencontainers.image.source="https://github.com/teekennedy/k8s-homelab" \
      org.opencontainers.image.url="https://github.com/teekennedy/k8s-homelab" \
      org.opencontainers.image.documentation="https://github.com/teekennedy/k8s-homelab/tree/main/k8s/apps/redteam" \
      org.opencontainers.image.licenses="MIT" \
      org.opencontainers.image.version="$VERSION" \
      org.opencontainers.image.revision="$REVISION"
