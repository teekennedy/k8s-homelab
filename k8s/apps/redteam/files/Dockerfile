# Session image for red-team pods: Claude Code + kubectl + recon tools.
# Build and push, then set `image` in values.yaml.
#   docker build -t <registry>/redteam-session:<tag> -f files/Dockerfile files/
FROM debian:bookworm-slim

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
