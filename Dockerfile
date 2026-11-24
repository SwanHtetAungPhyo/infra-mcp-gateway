#

ARG GO_VERSION=1.26
ARG ALPINE_VERSION=3.22
ARG TRIVY_VERSION=0.67.2
ARG NMAP_FORMATTER_VERSION=v3.1.5

FROM aquasec/trivy:${TRIVY_VERSION} AS trivy

FROM golang:${GO_VERSION}-alpine AS builder
ARG NMAP_FORMATTER_VERSION
ENV CGO_ENABLED=0 GOTOOLCHAIN=auto
WORKDIR /src

RUN go install -trimpath -ldflags="-s -w" github.com/vdjagilev/nmap-formatter/v3@${NMAP_FORMATTER_VERSION} \
 && cp "$(go env GOPATH)/bin/nmap-formatter" /nmap-formatter

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
COPY --from=builder /nmap-formatter /usr/local/bin/nmap-formatter
COPY --from=builder /runner /usr/local/bin/runner

ENV HOME=/home/app \
    TRIVY_CACHE_DIR=/home/app/.cache/trivy \
    TRIVY_ALLOWED_ROOTS=/scan

USER 10001:10001
WORKDIR /home/app
EXPOSE 8080

ENTRYPOINT ["/usr/local/bin/runner"]
CMD ["-mode=http", "-addr=:8080"]
