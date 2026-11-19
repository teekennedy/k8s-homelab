# syntax=docker/dockerfile:1@sha256:4edf897a3ffa55b89f906fc8cc78afdb3f1834cc9c7083565e611a8a7d5fe99e
# Hermes iOS, server side: the relay, the public endpoint the iPhone app and
# the connector both talk to, and the connector itself. Upstream publishes no
# image, so this builds both from a pinned commit with upstream's lockfiles.
# Released as ghcr.io/teekennedy/k8s-homelab:hermes-relay-<version> by any
# conventional commit that touches this directory; see ../../README.md,
# "iOS app".
#
# Everything lives under /opt/hermes-mobile, which runs from any mount point
# at that same path: the Hermes gateway pod mounts it as an image volume and
# runs the connector, and Hermes its MCP server, on the Hermes image.
#   python/     uv's standalone CPython, shared by both venvs
#   relay/      the relay's venv (this image's entrypoint)
#   connector/  the connector's venv: hermes-mobile, hermes-mobile-mcp
#   skills/     upstream's hermes-ios skill, for Hermes' skills directory

# Bump by hand: a new commit here is a new release.
FROM scratch AS source
ADD https://github.com/dylan-buck/Hermes-iOS.git#359e6e37c149592cb107f4bbb96d9e4957cb211f /

FROM ghcr.io/astral-sh/uv:0.13.5-trixie-slim@sha256:95611d89b7324e0517fe7257bb00979cf1f68461e5fb2b2d027d039da054bc07 AS build

ENV UV_LINK_MODE=copy \
    UV_NO_CACHE=1 \
    UV_PYTHON_INSTALL_DIR=/opt/hermes-mobile/python \
    UV_PYTHON_PREFERENCE=only-managed

COPY --from=source /relay /src/relay
COPY --from=source /connector /src/connector
COPY --from=source /skills /opt/hermes-mobile/skills
RUN set -eux; \
    cd /src/relay; \
    UV_PROJECT_ENVIRONMENT=/opt/hermes-mobile/relay uv sync --frozen --no-dev \
      --no-editable --python 3.12 --compile-bytecode; \
    cd /src/connector; \
    UV_PROJECT_ENVIRONMENT=/opt/hermes-mobile/connector uv sync --frozen --no-dev \
      --no-editable --python 3.12 --compile-bytecode

FROM debian:trixie-slim@sha256:a72b4433de1cf7d6bae0c4cee74d0547f58601a643b3375f070c51600d63b6b2

RUN set -eux; \
    apt-get update; \
    apt-get install -y --no-install-recommends ca-certificates; \
    rm -rf /var/lib/apt/lists/*

COPY --from=build /opt/hermes-mobile /opt/hermes-mobile

ENV PATH=/opt/hermes-mobile/relay/bin:$PATH \
    PYTHONUNBUFFERED=1 \
    HOME=/tmp

RUN set -eux; \
    export PYTHONDONTWRITEBYTECODE=1; \
    python -c 'import app.main'; \
    /opt/hermes-mobile/connector/bin/hermes-mobile --help >/dev/null; \
    /opt/hermes-mobile/connector/bin/python -c 'import hermes_mobile_connector.mcp_server'

USER 10002:10002
WORKDIR /tmp
EXPOSE 8000
# One process: rate limits and connector sockets live in its memory.
# --ws-max-size is upstream's: sensor and attachment frames run to 50 MB.
ENTRYPOINT ["uvicorn", "app.main:app", "--host", "0.0.0.0", "--port", "8000", "--ws-max-size", "52428800"]

ARG VERSION=dev
ARG REVISION=unknown
LABEL org.opencontainers.image.title="Hermes iOS relay" \
      org.opencontainers.image.description="Hermes iOS relay and connector: pairing, auth and jobs for the Hermes iPhone app, and the bridge that runs them on Hermes." \
      org.opencontainers.image.source="https://github.com/teekennedy/k8s-homelab" \
      org.opencontainers.image.url="https://github.com/dylan-buck/Hermes-iOS" \
      org.opencontainers.image.documentation="https://github.com/teekennedy/k8s-homelab/tree/main/k8s/apps/hermes-agent" \
      org.opencontainers.image.licenses="MIT" \
      org.opencontainers.image.version="$VERSION" \
      org.opencontainers.image.revision="$REVISION"
