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
  -v "$PWD":/src -w /src \
  -v hogwatch-gomod:/go/pkg/mod -v hogwatch-gocache:/root/.cache/go-build \
  "golang:${GO_VERSION}-bookworm" \
  go test -buildvcs=false -tags integration -race -count=1 ./...
