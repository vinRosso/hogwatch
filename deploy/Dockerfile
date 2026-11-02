# syntax=docker/dockerfile:1
# Hogwatch image: one static binary with the web UI embedded. The same image
# runs `serve` (web), `scan` (scanner) and `init` (first-start setup).
#
# Build from the repository root (make image does this):
#   docker buildx build -f deploy/Dockerfile --build-arg VERSION=... --build-arg COMMIT=... .
#
# The tool versions below must equal scripts/versions.env (a test checks it).
# The digests pin the multi-arch index of that tag; update both together.
ARG NODE_VERSION=24.22.1
ARG GO_VERSION=1.27.3

FROM --platform=$BUILDPLATFORM node:${NODE_VERSION}-bookworm-slim@sha256:be8204d4bd327363357f532187b752ff62759c31bedf98e9e18fc08c9cbd605c AS web
WORKDIR /src/web
COPY web/package.json web/package-lock.json web/.npmrc ./
RUN --mount=type=cache,target=/root/.npm npm ci
COPY web/ ./
ARG COMMIT=
RUN HOGWATCH_COMMIT="${COMMIT}" npm run build

FROM --platform=$BUILDPLATFORM golang:${GO_VERSION}-bookworm@sha256:d9b6a1beb9474adc2a1ce842573639b44b2a0dd49b6669f51d3821baf9ec37c9 AS build
ARG TARGETOS
ARG TARGETARCH
ENV GOTOOLCHAIN=local CGO_ENABLED=0
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY . .
COPY --from=web /src/web/dist ./web/dist
ARG VERSION=dev
ARG COMMIT=
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -trimpath \
      -ldflags "-s -w -X main.version=${VERSION} -X main.commit=${COMMIT}" \
      -o /out/hogwatch ./cmd/hogwatch \
 && mkdir -p /out/data

FROM gcr.io/distroless/static-debian12@sha256:d75cdd72874d4790092fcb1b058493ecf6bb5bf2b2b897045b00ff01d91843f2
ARG VERSION=dev
ARG COMMIT=
LABEL org.opencontainers.image.title="Hogwatch" \
      org.opencontainers.image.description="Self-hosted disk usage analyzer" \
      org.opencontainers.image.source="https://github.com/vinRosso/hogwatch" \
      org.opencontainers.image.licenses="AGPL-3.0-or-later" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.revision="${COMMIT}"
COPY --from=build /out/hogwatch /hogwatch
COPY --from=build --chown=10001:10001 /out/data /data
COPY LICENSE /LICENSE
USER 10001:10001
EXPOSE 8080
ENTRYPOINT ["/hogwatch"]
CMD ["serve"]
HEALTHCHECK --interval=30s --timeout=5s --start-period=30s --retries=3 CMD ["/hogwatch", "healthcheck"]
