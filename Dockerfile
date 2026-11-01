# syntax=docker/dockerfile:1
# Pure Go, CGO off: cross-compiled on the builder platform, no emulation.
FROM --platform=$BUILDPLATFORM docker.io/library/golang:1.27.3-trixie@sha256:c65ecfe613b539ac74c62a4c3b2c27e8f8a6076384c8f27e8a3c0f3be17cc385 AS build
ARG TARGETOS
ARG TARGETARCH
WORKDIR /src
COPY go.mod ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY cmd ./cmd
COPY internal ./internal
RUN --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags='-s -w' -o /out/lanatt ./cmd/lanatt

# The daemon runs /usr/sbin/ip and /usr/bin/nsenter, so the runtime is not distroless.
# It runs as root by design (ADR-0001): it creates host interfaces.
FROM docker.io/library/debian:trixie-slim@sha256:f6b08b09a05725f9431502d3b5714f5c4576e82c1a84fa1debe0c24a7e0c3ec4
RUN apt-get update \
    && apt-get install -y --no-install-recommends iproute2 \
    && rm -rf /var/lib/apt/lists/*
COPY --from=build /out/lanatt /usr/local/bin/lanatt
ENTRYPOINT ["/usr/local/bin/lanatt"]
CMD ["daemon", "-config", "/etc/lanatt/config.json"]
