#!/usr/bin/env bash
# Fails when any source uses Svelte's raw-HTML tag. File names are untrusted bytes
# (security invariant 8): they are only ever rendered as text.
set -euo pipefail
cd "$(dirname "$0")/.."

probe=$(mktemp -d)
trap 'rm -rf "$probe"' EXIT
printf '%s\n' '{@html x}' > "$probe/probe.svelte"
# Negative control: the same grep must catch a file that does use it.
if ! grep -rnE '\{@html' "$probe" >/dev/null; then
  echo "no-raw-html: negative control did not fire" >&2
  exit 1
fi

if grep -rnE '\{@html' src; then
  echo "no-raw-html: {@html} is forbidden in web/src" >&2
  exit 1
fi
echo "no-raw-html: ok"
