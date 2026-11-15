# syntax=docker/dockerfile:1@sha256:4edf897a3ffa55b89f906fc8cc78afdb3f1834cc9c7083565e611a8a7d5fe99e
# One image, every wake word trainer workload: the collector
# (`wake-word-trainer serve`), the nightly trainer and publisher, and the seed
# Job. Holds three Python environments:
#   /opt/venv        the okay_glados training pipeline (TensorFlow, Python 3.10)
#   /opt/venv-piper  Piper TTS for step 2 (piper-sample-generator pins numpy<2)
#   /opt/service     this directory's package, with whisper.cpp
# Released as ghcr.io/teekennedy/k8s-homelab:wake-word-trainer-<version> by any
# conventional commit that touches this directory; see ../../README.md.

# The pipeline, from micro-wake-word's public GitHub mirror. Bump the commit
# to pick up pipeline changes. For a local build against a working tree:
#   docker buildx build --build-context pipeline=../micro-wake-word .
FROM scratch AS pipeline
ADD https://github.com/teekennedy/micro-wake-word.git#ebdfcdbbb6d669e7cd5767eda6cd4fb7cde36700 /

FROM ghcr.io/astral-sh/uv:0.13.5-trixie-slim@sha256:95611d89b7324e0517fe7257bb00979cf1f68461e5fb2b2d027d039da054bc07

# kahrendt/piper-sample-generator, patched by okay_glados/patches/.
ARG PIPER_GENERATOR_COMMIT=b5b36e266576693ea47db129fbfe6bbc06164762
ARG PIPER_MODEL_URL=https://github.com/rhasspy/piper-sample-generator/releases/download/v2.0.0/en_US-libritts_r-medium.pt

ENV UV_LINK_MODE=copy \
    UV_COMPILE_BYTECODE=1 \
    UV_PYTHON_INSTALL_DIR=/opt/python \
    UV_PYTHON_PREFERENCE=only-managed \
    PYTHONUNBUFFERED=1

RUN set -eux; \
    apt-get update; \
    apt-get install -y --no-install-recommends ca-certificates curl ffmpeg git; \
    rm -rf /var/lib/apt/lists/*; \
    uv python install 3.10 3.12

COPY --from=pipeline / /opt/micro-wake-word

# Versions match the environments the published model was trained with.
RUN set -eux; \
    uv venv --python 3.10 /opt/venv; \
    uv pip install --no-cache --python /opt/venv /opt/micro-wake-word \
      'tensorflow==2.21.0' 'numpy==2.2.6' 'datasets==3.6.0' 'matplotlib==3.10.9' \
      'git+https://github.com/whatsnowplaying/audio-metadata@d4ebb238e6a401bb1a5aaaac60c9e2b3cb30929f'; \
    uv venv --python 3.10 /opt/venv-piper; \
    # uv prefers --extra-index-url: the CPU-only torch index wins over PyPI,
    # whose Linux torch pulls in ~3 GB of CUDA libraries.
    uv pip install --no-cache --python /opt/venv-piper \
      --index-url https://pypi.org/simple --extra-index-url https://download.pytorch.org/whl/cpu \
      'torch==2.14.0+cpu' 'torchaudio==2.11.0+cpu' 'piper-phonemize-cross==1.2.1' \
      'webrtcvad-wheels==2.0.14' 'numpy==1.26.4'; \
    git clone --quiet https://github.com/kahrendt/piper-sample-generator /opt/piper-sample-generator; \
    git -C /opt/piper-sample-generator checkout --quiet "$PIPER_GENERATOR_COMMIT"; \
    git -C /opt/piper-sample-generator apply \
      /opt/micro-wake-word/okay_glados/patches/piper-sample-generator.patch; \
    rm -rf /opt/piper-sample-generator/.git; \
    curl -fsSL -o /opt/piper-sample-generator/models/en_US-libritts_r-medium.pt "$PIPER_MODEL_URL"

COPY pyproject.toml uv.lock /src/
COPY wake_word_trainer /src/wake_word_trainer
RUN set -eux; \
    cd /src; \
    UV_PROJECT_ENVIRONMENT=/opt/service uv sync --frozen --no-dev --no-editable \
      --extra whisper --python 3.12 --no-cache; \
    /opt/service/bin/wake-word-trainer --help >/dev/null

ENV PATH=/opt/service/bin:$PATH \
    HOME=/tmp \
    MPLCONFIGDIR=/tmp/matplotlib \
    WWT_PIPELINE_DIR=/opt/micro-wake-word \
    OKAY_GLADOS_TRAIN_PYTHON=/opt/venv/bin/python \
    OKAY_GLADOS_PIPER_PYTHON=/opt/venv-piper/bin/python \
    OKAY_GLADOS_PIPER_DIR=/opt/piper-sample-generator

# Matches the owner of the NFS directories (nix/modules/nfs-mtls).
USER 2400:2400
ENTRYPOINT ["wake-word-trainer"]
CMD ["serve"]

ARG VERSION=dev
ARG REVISION=unknown
LABEL org.opencontainers.image.title="Wake word trainer" \
      org.opencontainers.image.description="Collects, reviews and retrains the Okay GLaDOS microWakeWord model." \
      org.opencontainers.image.source="https://github.com/teekennedy/k8s-homelab" \
      org.opencontainers.image.url="https://github.com/teekennedy/k8s-homelab" \
      org.opencontainers.image.documentation="https://github.com/teekennedy/k8s-homelab/tree/main/k8s/apps/wake-word-trainer" \
      org.opencontainers.image.licenses="MIT" \
      org.opencontainers.image.version="$VERSION" \
      org.opencontainers.image.revision="$REVISION"
