#!/usr/bin/env bash
# Proves the invariant-1 lint rules reject what they must: each probe file is
# written into every package the rules cover (see .golangci.yml: internal/web,
# internal/repo, internal/auth, internal/export, internal/schedule, internal/settings),
# linted, and must make golangci-lint fail through the named linter.
# Usage: bash scripts/lint-barrier.sh   (from the repo root; run by make lint)
set -euo pipefail

cd "$(dirname "$0")/.."
# dir:package of the probe's host. internal/auth has no package of its own, so
# the probe lives in one of its subpackages.
targets=(internal/web:web internal/repo:repo internal/auth/local:local internal/export:export internal/schedule:schedule internal/settings:settings)
lint() { go tool -modfile=tools/go.mod golangci-lint run "./${1%%:*}/..."; }
trap 'rm -f internal/web/zz_lintprobe_*.go internal/repo/zz_lintprobe_*.go internal/auth/local/zz_lintprobe_*.go internal/export/zz_lintprobe_*.go internal/schedule/zz_lintprobe_*.go internal/settings/zz_lintprobe_*.go' EXIT

for t in "${targets[@]}"; do
  lint "$t" >/dev/null || { echo "lint barrier: control run failed for ${t%%:*}; fix lint first" >&2; exit 1; }
done

probe() { # name linter source-with-PKG-placeholder
  local t dir pkg file out src
  for t in "${targets[@]}"; do
    dir=${t%%:*} pkg=${t##*:}
    file="$dir/zz_lintprobe_$1.go"
    src=${3//PKG/$pkg}
    printf '%s\n' "$src" >"$file"
    if out=$(lint "$t" 2>&1); then
      echo "lint barrier: $2 probe was not rejected in $dir" >&2; exit 1
    fi
    if ! grep -q "($2)" <<<"$out"; then
      echo "lint barrier: $2 probe failed lint in $dir but not through $2:" >&2; echo "$out" >&2; exit 1
    fi
    rm -f "$file"
  done
}

probe os forbidigo 'package PKG

import "os"

// ProbeOS is exported and its body is on its own line so that golangci-lint
// (uniq-by-line) reports forbidigo rather than another linter.
func ProbeOS() {
	_, _ = os.Open("/")
}'
probe syscall depguard 'package PKG

import _ "syscall"'

echo "lint barrier: 2 probes rejected in ${#targets[@]} packages"
