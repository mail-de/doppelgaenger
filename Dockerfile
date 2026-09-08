# syntax=docker/dockerfile:1.7

ARG GO_IMAGE=golang:1.27.1-alpine3.23
ARG CERTS_IMAGE=alpine:3.23
ARG RUNTIME_IMAGE=scratch

FROM --platform=$BUILDPLATFORM ${GO_IMAGE} AS builder

ARG TARGETOS=linux
ARG TARGETARCH
ARG VERSION=dev

WORKDIR /src

ENV CGO_ENABLED=0 \
    GOEXPERIMENT=runtimesecret \
    GOFLAGS=-mod=vendor

COPY go.mod go.sum ./
COPY vendor/ ./vendor/
COPY main.go ./
COPY internal/ ./internal/

RUN --mount=type=cache,target=/root/.cache/go-build \
    set -eux; \
    target_os="${TARGETOS:-linux}"; \
    target_arch="${TARGETARCH:-$(go env GOARCH)}"; \
    GOOS="$target_os" GOARCH="$target_arch" go build \
        -trimpath \
        -buildvcs=false \
        -ldflags="-s -w -buildid= -X main.version=${VERSION}" \
        -o /out/doppelgaenger \
        .

FROM --platform=$BUILDPLATFORM ${CERTS_IMAGE} AS runtime-files

RUN set -eux; \
    apk add --no-cache ca-certificates tzdata; \
    addgroup -S -g 10001 doppelgaenger; \
    adduser -S -D -H -u 10001 -G doppelgaenger -s /sbin/nologin doppelgaenger; \
    mkdir -p /app /etc/doppelgaenger /tmp; \
    chown 10001:10001 /app /etc/doppelgaenger; \
    chmod 0755 /app /etc/doppelgaenger; \
    chmod 1777 /tmp

FROM ${RUNTIME_IMAGE}

ARG VERSION=dev
ARG REVISION=unknown
ARG BUILD_DATE=unknown
ARG SOURCE=https://github.com/mail-de/doppelgaenger

LABEL org.opencontainers.image.title="doppelgaenger" \
      org.opencontainers.image.description="Backend-neutral shadow proxy for HTTP, gRPC, and Milter traffic" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.revision="${REVISION}" \
      org.opencontainers.image.created="${BUILD_DATE}" \
      org.opencontainers.image.source="${SOURCE}" \
      org.opencontainers.image.authors="Christian Rößner <c.roessner@team.mail.de>" \
      org.opencontainers.image.licenses="MIT"

COPY --from=runtime-files /etc/passwd /etc/passwd
COPY --from=runtime-files /etc/group /etc/group
COPY --from=runtime-files /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=runtime-files /usr/share/zoneinfo/UTC /usr/share/zoneinfo/UTC
COPY --from=runtime-files --chown=10001:10001 /app /app
COPY --from=runtime-files --chown=10001:10001 /etc/doppelgaenger /etc/doppelgaenger
COPY --from=runtime-files /tmp /tmp
COPY --chmod=0444 LICENSE /app/LICENSE
COPY --from=builder --chmod=0555 /out/doppelgaenger /usr/local/bin/doppelgaenger

ENV SSL_CERT_FILE=/etc/ssl/certs/ca-certificates.crt \
    TMPDIR=/tmp \
    TZ=UTC

WORKDIR /app
VOLUME ["/etc/doppelgaenger", "/tmp"]

USER 10001:10001

EXPOSE 8080 8443 9444 9464 9999

ENTRYPOINT ["/usr/local/bin/doppelgaenger"]
CMD ["--config", "/etc/doppelgaenger/config.yaml"]
