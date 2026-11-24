#

ARG GO_VERSION=1.26
ARG ALPINE_VERSION=3.22
ARG TRIVY_VERSION=0.67.2
ARG NMAP_FORMATTER_VERSION=v3.1.5

FROM aquasec/trivy:${TRIVY_VERSION} AS trivy

# nmap-formatter: prebuilt static release binary, checked against its SHA-256.
# (Building it from source pulls in d2, goja and sqlite: slow and memory hungry.)
FROM alpine:${ALPINE_VERSION} AS nmap-formatter
ARG NMAP_FORMATTER_VERSION
ARG TARGETARCH
RUN set -eu; \
    case "${TARGETARCH:-$(uname -m)}" in \
      amd64|x86_64)  asset=linux-amd64-alpine; sum=f1f2d2b8d3a36f4748bb7becde0c9fb55de30b05ec1cf2875c1eb546f159343e ;; \
      arm64|aarch64) asset=linux-arm64;        sum=ebd1ab908c926e12767d69714f6ef0686e2285adaff8f236b8d376f8d58f69b8 ;; \
      *) echo "unsupported arch ${TARGETARCH:-$(uname -m)}" >&2; exit 1 ;; \
    esac; \
    wget -qO /tmp/nf.tgz "https://github.com/vdjagilev/nmap-formatter/releases/download/${NMAP_FORMATTER_VERSION}/nmap-formatter-${asset}.tar.gz"; \
    echo "${sum}  /tmp/nf.tgz" | sha256sum -c -; \
    tar -xzf /tmp/nf.tgz -C /usr/local/bin nmap-formatter; \
    /usr/local/bin/nmap-formatter --version

FROM golang:${GO_VERSION}-alpine AS builder
ENV CGO_ENABLED=0 GOTOOLCHAIN=auto
WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN go build -trimpath -ldflags="-s -w" -o /runner ./cmd

FROM alpine:${ALPINE_VERSION}

RUN apk add --no-cache ca-certificates tzdata nmap nmap-scripts \
 && addgroup -S -g 10001 app \
 && adduser -S -D -u 10001 -G app -h /home/app -s /sbin/nologin app \
 && mkdir -p /home/app/.cache/trivy /scan \
 && chown -R app:app /home/app /scan

COPY --from=trivy /usr/local/bin/trivy /usr/local/bin/trivy
COPY --from=nmap-formatter /usr/local/bin/nmap-formatter /usr/local/bin/nmap-formatter
COPY --from=builder /runner /usr/local/bin/runner

ENV HOME=/home/app \
    TRIVY_CACHE_DIR=/home/app/.cache/trivy \
    TRIVY_ALLOWED_ROOTS=/scan

USER 10001:10001
WORKDIR /home/app
EXPOSE 8080
# Liveness only; /readyz (binaries present, TRIVY_SERVER up) is for k8s readiness.
HEALTHCHECK --interval=30s --timeout=3s CMD wget -qO /dev/null http://127.0.0.1:8080/healthz || exit 1

ENTRYPOINT ["/usr/local/bin/runner"]
CMD ["-mode=http", "-addr=:8080"]
