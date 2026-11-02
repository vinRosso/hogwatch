#!/usr/bin/env bash
# Fails when a non-merge commit in BASE..HEAD lacks a Signed-off-by trailer
# matching its author. Usage: bash scripts/check-dco.sh BASE HEAD
set -euo pipefail

[[ $# -eq 2 ]] || { echo "usage: bash scripts/check-dco.sh BASE HEAD" >&2; exit 2; }
base=$1 head=$2

bad=0
for c in $(git rev-list --no-merges "$base..$head"); do
  author=$(git log -1 --format='%an <%ae>' "$c")
  if ! git log -1 --format='%(trailers:key=Signed-off-by,valueonly)' "$c" | grep -qxF "$author"; then
    echo "missing or mismatched Signed-off-by: $(git rev-parse --short "$c") ($author)"
    bad=1
  fi
done
exit $bad
