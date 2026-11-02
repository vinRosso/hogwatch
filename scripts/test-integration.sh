#!/usr/bin/env bash
# Runs the integration tests (build tag "integration") as root in a privileged
# golang container. Usage: bash scripts/test-integration.sh  (or make test-integration)
set -euo pipefail
# Git Bash on Windows rewrites /src-style arguments to docker; harmless on Linux.
export MSYS_NO_PATHCONV=1

cd "$(dirname "$0")/.."
# shellcheck source=versions.env
source scripts/versions.env

exec docker run --rm --privileged \
  -v "$PWD":/src -w /src -e GOFLAGS=-buildvcs=false \
  -v hogwatch-gomod:/go/pkg/mod -v hogwatch-gocache:/root/.cache/go-build \
  "golang:${GO_VERSION}-bookworm" \
  go test -tags integration -count=1 ./test/integration ./internal/scanner/build ./internal/scanner/fsx ./internal/scanner/walk ./internal/scanner/service
