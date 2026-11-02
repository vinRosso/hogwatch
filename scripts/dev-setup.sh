#!/usr/bin/env bash
# Installs the pinned Go and Node toolchains into ~/.local (no sudo) and checks
# the dev environment. Usage: bash scripts/dev-setup.sh [--check]
set -euo pipefail

cd "$(dirname "$0")/.."
# shellcheck source=versions.env
source scripts/versions.env

OPT="$HOME/.local/opt"
BIN="$HOME/.local/bin"

install_go() {
  local dir="$OPT/go-$GO_VERSION" file="go$GO_VERSION.linux-amd64.tar.gz"
  if [[ -x "$dir/bin/go" ]]; then echo "go $GO_VERSION already installed"; else
    local tmp sum
    tmp=$(mktemp -d)
    sum=$(curl -fsSL "https://go.dev/dl/?mode=json&include=all" | python3 -c '
import json, sys
f = sys.argv[1]
print(next(x["sha256"] for r in json.load(sys.stdin) for x in r["files"] if x["filename"] == f))
' "$file")
    curl -fsSLo "$tmp/$file" "https://go.dev/dl/$file"
    echo "$sum  $tmp/$file" | sha256sum -c --quiet
    mkdir -p "$dir"
    tar -xzf "$tmp/$file" -C "$dir" --strip-components=1
    rm -rf "$tmp"
  fi
  mkdir -p "$BIN"
  ln -sfn "$dir/bin/go" "$BIN/go"
  ln -sfn "$dir/bin/gofmt" "$BIN/gofmt"
}

install_node() {
  local name="node-v$NODE_VERSION-linux-x64"
  local dir="$OPT/$name" file="$name.tar.xz"
  if [[ -x "$dir/bin/node" ]]; then echo "node $NODE_VERSION already installed"; else
    local tmp
    tmp=$(mktemp -d)
    curl -fsSLo "$tmp/$file" "https://nodejs.org/dist/v$NODE_VERSION/$file"
    curl -fsSL "https://nodejs.org/dist/v$NODE_VERSION/SHASUMS256.txt" | grep " $file\$" \
      | sed "s|  $file\$|  $tmp/$file|" | sha256sum -c --quiet
    mkdir -p "$dir"
    tar -xJf "$tmp/$file" -C "$dir" --strip-components=1
    rm -rf "$tmp"
  fi
  mkdir -p "$BIN"
  for t in node npm npx; do ln -sfn "$dir/bin/$t" "$BIN/$t"; done
}

check() {
  local fails=0 p v
  for t in go node npm; do
    p=$(command -v "$t" || true)
    if [[ "$p" != "$BIN/$t" ]]; then
      echo "FAIL $t resolves to '${p:-nothing}', want $BIN/$t (is ~/.local/bin first on PATH?)"
      fails=$((fails + 1)); continue
    fi
    case $t in
      go) v=$(go env GOVERSION); [[ "$v" == "go$GO_VERSION" ]] ;;
      node) v=$(node --version); [[ "$v" == "v$NODE_VERSION" ]] ;;
      npm) v=$(npm --version); true ;;
    esac || { echo "FAIL $t version $v, want ${t^^}_VERSION from versions.env"; fails=$((fails + 1)); continue; }
    echo "ok   $t $v"
  done
  v=$(docker info --format '{{.ServerVersion}}' 2>/dev/null || true)
  if [[ "$v" =~ ^[0-9]+\. ]]; then
    if (( ${v%%.*} >= 25 )); then echo "ok   docker server $v"; else
      echo "FAIL docker server $v, want >= 25 (Docker Desktop WSL integration)"; fails=$((fails + 1)); fi
  else
    echo "FAIL docker server unreachable (enable Docker Desktop WSL integration for this distro)"; fails=$((fails + 1))
  fi
  return $((fails > 0))
}

case "${1:-}" in
  --check) check ;;
  "") install_go; install_node; echo "installed; run: bash scripts/dev-setup.sh --check" ;;
  *) echo "usage: bash scripts/dev-setup.sh [--check]" >&2; exit 2 ;;
esac
