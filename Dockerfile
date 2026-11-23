# syntax=docker/dockerfile:1@sha256:572cebbc2b15e6f84c077742952674889cb63cd5e9b80abdffcbe587b6f19183
# Pure Go, CGO off: cross-compiled on the builder platform, no emulation.
FROM --platform=$BUILDPLATFORM docker.io/library/golang:1.27.4-trixie@sha256:b54b2639de8c1bfe538ec5ffc45f29b1932235ac5f6d63c059853646677112d9 AS build
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
FROM docker.io/library/debian:trixie-slim@sha256:a72b4433de1cf7d6bae0c4cee74d0547f58601a643b3375f070c51600d63b6b2
RUN apt-get update \
    && apt-get install -y --no-install-recommends iproute2 \
    && rm -rf /var/lib/apt/lists/*
COPY --from=build /out/lanatt /usr/local/bin/lanatt
ENTRYPOINT ["/usr/local/bin/lanatt"]
CMD ["daemon", "-config", "/etc/lanatt/config.json"]
