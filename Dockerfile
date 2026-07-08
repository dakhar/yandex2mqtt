# syntax=docker/dockerfile:1

# --- build stage ---
FROM golang:1.25-alpine AS build
WORKDIR /src
# Cache modules first.
COPY go.mod go.sum ./
RUN go mod download
# Build a static binary (modernc sqlite is pure Go, so CGO stays off).
COPY . .
ENV CGO_ENABLED=0 GOOS=linux
# VERSION is injected into the binary (defaults to "dev"); pass e.g.
# --build-arg VERSION=$(git describe --tags --always --dirty).
ARG VERSION=dev
RUN go build -trimpath \
    -ldflags="-s -w -X github.com/dakhar/yandex2mqtt/internal/version.value=${VERSION}" \
    -o /out/yandex2mqtt ./cmd/yandex2mqtt

# --- runtime stage ---
FROM alpine:3.20
RUN apk add --no-cache ca-certificates tzdata tini && \
    adduser -D -u 10001 app && \
    mkdir -p /app/data && chown app:app /app/data
WORKDIR /app
COPY --from=build /out/yandex2mqtt /usr/local/bin/yandex2mqtt
USER app
# Device catalog and SQLite DB live in the mounted /app/data volume.
VOLUME ["/app/data"]
# nginx-proxy routes to this port (VIRTUAL_PORT); app listens here behind the proxy.
EXPOSE 80
ENTRYPOINT ["/sbin/tini", "--", "yandex2mqtt"]
