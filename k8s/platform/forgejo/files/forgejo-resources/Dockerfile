FROM golang:1.27-alpine AS builder
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY *.go ./
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /forgejo-resources .

FROM scratch

ARG VERSION=dev
ARG REVISION=unknown
LABEL org.opencontainers.image.title="Forgejo resources" \
      org.opencontainers.image.description="Reconcile Forgejo users, organizations, teams, access tokens, and repos." \
      org.opencontainers.image.source="https://github.com/teekennedy/k8s-homelab" \
      org.opencontainers.image.url="https://github.com/teekennedy/k8s-homelab" \
      org.opencontainers.image.documentation="https://github.com/teekennedy/k8s-homelab/tree/main/k8s/platform/forgejo" \
      org.opencontainers.image.licenses="MIT" \
      org.opencontainers.image.version="$VERSION" \
      org.opencontainers.image.revision="$REVISION"
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=builder /forgejo-resources /forgejo-resources
USER 65534:65534
WORKDIR /config
ENTRYPOINT ["/forgejo-resources"]
