# Where the Hermes agent's shell, file and code tools run: a non-root sshd and
# the handful of tools an assistant's scripts reach for. Released as
# ghcr.io/teekennedy/k8s-homelab:hermes-sandbox-<version> by any conventional
# commit that touches this directory; see README.md, "Sandbox image".
FROM debian:trixie-slim@sha256:8aa5a7bf5a1955bddbe7d74fcdd0b91ab30e74362a8b73af672b0481b35b44a0

# bash, tar and python3 are what Hermes' ssh backend itself requires; the rest
# is for the agent. Node, ffmpeg and numpy cover rendering and encoding media
# (headless Chromium captures, video), which it cannot install without root.
RUN set -eux; \
    apt-get update; \
    apt-get install -y --no-install-recommends \
      bash ca-certificates curl ffmpeg git jq less nodejs npm openssh-server \
      procps python3 python3-numpy python3-pip python3-venv ripgrep tar unzip \
      xz-utils; \
    rm -rf /var/lib/apt/lists/*; \
    # Host keys come from a Secret, never from the image.
    rm -f /etc/ssh/ssh_host_*; \
    # sshd insists on its privilege separation directory, even as non-root.
    mkdir -p /run/sshd; \
    useradd --create-home --uid 10001 --shell /bin/bash agent; \
    # useradd locks the password, and sshd without PAM refuses a locked
    # account. '*' is no valid password, but not locked.
    usermod --password '*' agent

# The desktop Hermes' browser tool runs a headed Chromium on, started over SSH
# by the gateway: the binaries tools/bot_desktop/sandbox_host.py requires, as
# in upstream's docker/sandbox-desktop.Dockerfile. python3-pil grabs the
# dashboard's screen thumbnail.
RUN set -eux; \
    apt-get update; \
    apt-get install -y --no-install-recommends \
      chromium dbus-bin dbus-x11 fonts-dejavu-core fonts-liberation \
      python3-pil tigervnc-standalone-server x11-utils x11-xkb-utils \
      x11-xserver-utils xauth xfce4-panel xfce4-settings xfce4-terminal \
      xfdesktop4 xfwm4; \
    rm -rf /var/lib/apt/lists/*; \
    # Hermes only looks for a Playwright-style Chromium.
    mkdir -p /opt/playwright/chromium-debian/chrome-linux; \
    ln -s /usr/lib/chromium/chromium /opt/playwright/chromium-debian/chrome-linux/chrome

COPY sshd_config /etc/ssh/sshd_config

USER 10001
WORKDIR /home/agent
EXPOSE 2222
# -e: log to stderr. No syslog in the container.
ENTRYPOINT ["/usr/sbin/sshd", "-D", "-e", "-f", "/etc/ssh/sshd_config"]
