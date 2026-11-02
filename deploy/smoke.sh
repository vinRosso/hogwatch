#!/usr/bin/env bash
# Container smoke test: installs Hogwatch the documented way (the real
# compose.yaml, a .env that sets only SCAN_PATH) and checks the running
# containers. Every PASS asserts a property, not just an exit status.
#
# usage: deploy/smoke.sh <image>
#
# Needs Docker with Compose v2, curl and python3 (or python). Linux is the
# target; the checks that depend on how the host creates bind paths are
# skipped elsewhere (Docker Desktop creates a missing path anyway).
#
# SMOKE_SPILL_DIRS (default 150000) sizes the directory used for the
# temp-spill check, SMOKE_FRESH_RUNS (default 5) the number of empty-data
# starts, SMOKE_PORT (default 18090) the published port.
set -euo pipefail

export MSYS_NO_PATHCONV=1 # Git Bash on Windows must not rewrite /hogwatch into a Windows path
IMAGE=${1:?usage: deploy/smoke.sh <image>}
ROOT=$(cd "$(dirname "$0")/.." && pwd)
SPILL_DIRS=${SMOKE_SPILL_DIRS:-150000}
FRESH_RUNS=${SMOKE_FRESH_RUNS:-5}
PORT=${SMOKE_PORT:-18090}
PY=
for c in python3 python; do
	if command -v "$c" >/dev/null 2>&1 && "$c" -c 'import json' >/dev/null 2>&1; then PY=$c; break; fi
done
[ -n "$PY" ] || { echo "smoke: python3 is required" >&2; exit 2; }

WORK=$(mktemp -d)
cd "$WORK"
winpath() { if pwd -W >/dev/null 2>&1; then (cd "$1" && pwd -W); else (cd "$1" && pwd); fi; }

export HOGWATCH_VERSION=smoke HOGWATCH_PORT=$PORT
docker image inspect "$IMAGE" >/dev/null 2>&1 || docker pull "$IMAGE"
docker tag "$IMAGE" ghcr.io/vinrosso/hogwatch:smoke

PASSED=0
pass() { PASSED=$((PASSED + 1)); echo "PASS  $*"; }
fail() { echo "FAIL  $*" >&2; dump; exit 1; }
dump() { (cd "$WORK/proj" 2>/dev/null && docker compose logs --no-color --tail 40 2>&1) || true; }
cleanup() {
	(cd "$WORK/proj" 2>/dev/null && docker compose down -v --remove-orphans >/dev/null 2>&1) || true
	docker run --rm -v "$(winpath "$WORK"):/w" busybox chmod -R a+rwX /w >/dev/null 2>&1 || true
	rm -rf "$WORK" 2>/dev/null || true
}
trap cleanup EXIT

# install makes a fresh project directory holding exactly what the docs tell a
# user to download, with SCAN_PATH set to $1.
install() {
	if [ -d "$WORK/proj" ]; then
		# ./data belongs to 10001 after hogwatch-init, so remove it as root.
		docker run --rm -v "$(winpath "$WORK/proj"):/p" busybox sh -c 'rm -rf /p/* /p/.[!.]*' >/dev/null 2>&1 || true
		rm -rf "$WORK/proj"
	fi
	mkdir -p "$WORK/proj"
	cp "$ROOT/compose.yaml" "$WORK/proj/compose.yaml"
	cp "$ROOT/.env.example" "$WORK/proj/.env"
	sed -i.bak "s|^SCAN_PATH=.*|SCAN_PATH=$1|" "$WORK/proj/.env" && rm -f "$WORK/proj/.env.bak"
	cd "$WORK/proj"
}

health() { docker inspect -f '{{.State.Health.Status}}' "$1" 2>/dev/null || echo missing; }
wait_healthy() { # wait_healthy <seconds> <container>...
	local deadline=$((SECONDS + $1)) c
	shift
	for c in "$@"; do
		until [ "$(health "$c")" = healthy ]; do
			[ $SECONDS -lt $deadline ] || return 1
			sleep 2
		done
	done
}

# --- request helpers -------------------------------------------------------
COOKIE=
api() { # api METHOD PATH [JSON]; prints the body and leaves the status in $WORK/code
	curl -s -o "$(winpath "$WORK")/body" -w '%{http_code}' -X "$1" -H "Cookie: $COOKIE" -H 'X-Hogwatch-CSRF: 1' \
		-H 'Content-Type: application/json' ${3:+-d "$3"} "http://127.0.0.1:$PORT$2" >"$WORK/code" || true
	cat "$WORK/body"
}
code() { cat "$WORK/code"; }
# json evaluates a Python expression written in this script (never input) on the
# parsed JSON document d read from stdin.
# Anything that goes wrong prints ERR, which the callers' checks then reject.
json() { "$PY" -c 'import json,sys
try:
    d = json.load(sys.stdin)
    print(eval(sys.argv[1]))
except Exception:
    print("ERR")' "$1" | tr -d '\r'; }

# --- fixture ---------------------------------------------------------------
mkdir -p scan/docs scan/spill rw
head -c 1048576 /dev/zero >scan/docs/b.bin
head -c 102400 /dev/zero >scan/docs/a.txt
seq 1 "$SPILL_DIRS" | sed 's|^|scan/spill/d|' | xargs mkdir -p
seq 1 "$SPILL_DIRS" | sed 's|^|scan/spill/d|; s|$|/f|' | xargs touch
SCAN=$(winpath "$WORK/scan")

# --- 1. five fresh starts on an empty ./data (Review Focus 1) --------------
for i in $(seq 1 "$FRESH_RUNS"); do
	install "$(winpath "$WORK/scan")"
	docker compose up -d >/dev/null 2>&1 || fail "fresh start $i: compose up failed"
	wait_healthy 90 hogwatch-web hogwatch-scanner || fail "fresh start $i: web and scanner not both healthy in 90 s"
	docker compose down -v >/dev/null 2>&1
done
pass "five fresh starts on an empty ./data: web and scanner healthy within 90 s each time"

# --- 2. the main stack, inspected -----------------------------------------
install "$SCAN"
docker compose up -d >/dev/null 2>&1 || fail "compose up failed"
wait_healthy 90 hogwatch-web hogwatch-scanner || fail "main stack not healthy"
[ "$(docker inspect -f '{{.State.ExitCode}}' hogwatch-init)" = 0 ] || fail "hogwatch-init did not exit 0"
pass "hogwatch-init exited 0 and the other services are healthy"

insp() { docker inspect -f "$2" "$1"; }
[ "$(insp hogwatch-scanner '{{.HostConfig.CapAdd}} {{.HostConfig.CapDrop}} {{.HostConfig.NetworkMode}} {{.HostConfig.ReadonlyRootfs}} {{.Config.User}} {{.HostConfig.Privileged}} {{.HostConfig.SecurityOpt}}')" \
	= '[CAP_DAC_READ_SEARCH] [ALL] none true 0:10001 false [no-new-privileges:true]' ] || fail "scanner privileges differ from the rules: $(insp hogwatch-scanner '{{.HostConfig.CapAdd}} {{.HostConfig.CapDrop}} {{.HostConfig.NetworkMode}} {{.HostConfig.ReadonlyRootfs}} {{.Config.User}}')"
[ "$(insp hogwatch-scanner '{{range .Mounts}}{{.Destination}}={{.RW}}/{{.Propagation}} {{end}}' | tr ' ' '\n' | grep '^/host/')" = '/host/data=false/rslave' ] || fail "the scan path is not mounted read-only with rslave"
[ -z "$(insp hogwatch-scanner '{{range .HostConfig.Devices}}{{.PathOnHost}}{{end}}')" ] || fail "the scanner has a device"
pass "scanner: caps, network, read-only rootfs, user, security options and the read-only rslave scan mount match the rules"

[ "$(insp hogwatch-web '{{range .Mounts}}{{.Destination}} {{end}}')" = '/data ' ] || fail "the web has mounts other than /data"
[ "$(insp hogwatch-web '{{.HostConfig.CapAdd}} {{.HostConfig.ReadonlyRootfs}}')" = '[] true' ] || fail "web capabilities or rootfs differ"
pass "web: /data is its only mount, no added capabilities, read-only rootfs"

# What the rules mean in practice: an equivalent container cannot write.
docker pull -q busybox >/dev/null 2>&1 || true
WROTE=$(docker run --rm --read-only --cap-drop ALL --cap-add DAC_READ_SEARCH --user 0:10001 --network none \
	-v "$SCAN:/host/data:ro" busybox sh -c 'if touch /host/data/x 2>/dev/null || touch /x 2>/dev/null; then echo WRITE_OK; else echo DENIED; fi' 2>&1) \
	|| fail "the write probe container did not run: $WROTE"
[ "$WROTE" = DENIED ] || fail "a container built like the scanner could write ($WROTE)"
pass "a container built like the scanner cannot write to the scan path or to /"

# --- 3. the whole flow -----------------------------------------------------
PW='smoke test password 1'
MADE=$(printf '%s\n' "$PW" | docker compose exec -T hogwatch-web /hogwatch user create --username admin --admin --password-stdin 2>&1) \
	|| fail "creating the admin failed: $MADE"
LOGIN=$(curl -si -X POST -H 'Content-Type: application/json' -H 'X-Hogwatch-CSRF: 1' \
	-d "{\"username\":\"admin\",\"password\":\"$PW\"}" "http://127.0.0.1:$PORT/api/v1/auth/login")
COOKIE=$(printf '%s' "$LOGIN" | tr -d '\r' | sed -n 's/^[Ss]et-[Cc]ookie: \([^;]*\).*/\1/p' | head -1)
[ -n "$COOKIE" ] || fail "login set no cookie: $LOGIN"
pass "admin created with the CLI and logged in"

MOUNTS=$(api GET /api/v1/admin/available-mounts)
[ "$(printf '%s' "$MOUNTS" | json "[m['container_path'] for m in d['items']]")" = "['/host/data']" ] || fail "available mounts: $MOUNTS"
ROOT_JSON=$(api POST /api/v1/admin/roots '{"name":"smoke","container_path":"/host/data"}')
[ "$(code)" = 201 ] || fail "create root: $(code) $ROOT_JSON"
RID=$(printf '%s' "$ROOT_JSON" | json "d['id']")
api POST /api/v1/scans "{\"root_id\":$RID}" >/dev/null
[ "$(code)" = 202 ] || fail "queue scan: $(code)"
deadline=$((SECONDS + 600))
STATUS=
until [ "$STATUS" = "done" ]; do
	[ $SECONDS -lt $deadline ] || fail "the scan did not finish in 10 minutes (status $STATUS)"
	[ "$STATUS" != failed ] || fail "the scan failed"
	sleep 3
	STATUS=$(api GET "/api/v1/scans?root=$RID" | json "d['items'][0]['status'] if d['items'] else ''")
done
pass "the scan ran to done"

[ "$(curl -s -o /dev/null -w '%{http_code}' "http://127.0.0.1:$PORT/readyz")" = 200 ] || fail "/readyz is not 200 after the scan"
SCAN_ID=$(api GET /api/v1/admin/roots | json "[r for r in d['items'] if r['id']==$RID][0]['current_scan_id']")
CHILD_REF() { api GET "/api/v1/nodes/$1/children?sort=name&limit=200"; }
TOP=$(CHILD_REF "$SCAN_ID.1")
[ "$(code)" = 200 ] || fail "root children: $(code) $TOP"
DOCS_BYTES=$(printf '%s' "$TOP" | json "[c['bytes_alloc'] for c in d['items'] if c['name']=='docs'][0]")
[ "$DOCS_BYTES" -ge 1150976 ] || fail "docs holds $DOCS_BYTES bytes, the fixture has 1,150,976 or more"
pass "/readyz is 200 and the browse API returns the fixture's bytes (docs: $DOCS_BYTES)"

# --- 4. temp spill (Review Focus 2): sorts over a huge directory ----------
SPILL_REF=$(printf '%s' "$TOP" | json "[c['ref'] for c in d['items'] if c['name']=='spill'][0]")
for q in 'sort=size&limit=100' 'sort=size&limit=100&filter=size:lt1m' 'sort=files&limit=100' 'sort=mtime&limit=100'; do
	api GET "/api/v1/nodes/$SPILL_REF/children?$q" >/dev/null
	[ "$(code)" = 200 ] || fail "sorting $SPILL_DIRS subdirectories ($q) answered $(code); the web needs a writable temp directory"
done
api GET "/api/v1/nodes/$SPILL_REF/treemap" >/dev/null
[ "$(code)" = 200 ] || fail "the treemap of $SPILL_DIRS subdirectories answered $(code)"
pass "four sorts and the treemap over a directory with $SPILL_DIRS subdirectories answer 200 on the read-only rootfs"

# --- 5. a writable mount is refused (invariant 5) --------------------------
cat >compose.override.yaml <<EOF
services:
  hogwatch-scanner:
    volumes:
      - {type: bind, source: "$(winpath "$WORK/rw")", target: /host/rw, bind: {propagation: rslave}}
EOF
docker compose up -d >/dev/null 2>&1 || fail "compose up with the override failed"
wait_healthy 90 hogwatch-scanner || fail "scanner with a writable mount not healthy"
LISTED=
for _ in $(seq 1 30); do
	if [ "$(api GET /api/v1/admin/available-mounts | json "[m['readonly'] for m in d['items'] if m['container_path']=='/host/rw']")" = "[False]" ]; then LISTED=1; break; fi
	sleep 2
done
[ -n "$LISTED" ] || fail "the writable /host/rw mount never showed up as a writable available mount"
REFUSED=$(api POST /api/v1/admin/roots '{"name":"rw","container_path":"/host/rw"}')
[ "$(code)" = 422 ] || fail "a root on a writable mount answered $(code), want 422: $REFUSED"
[ "$(api GET /api/v1/admin/roots | json "[r['name'] for r in d['items'] if r['name']=='rw']")" = "[]" ] || fail "the refused root was created anyway"
pass "a writable mount is refused as a root (422) and no root is created"
rm -f compose.override.yaml
docker compose up -d >/dev/null 2>&1
wait_healthy 90 hogwatch-scanner || fail "scanner did not come back"

# --- 6. scanner down: not ready, web still healthy -------------------------
docker compose stop hogwatch-scanner >/dev/null 2>&1
deadline=$((SECONDS + 100))
until [ "$(curl -s -o /dev/null -w '%{http_code}' "http://127.0.0.1:$PORT/readyz")" != 200 ]; do
	[ $SECONDS -lt $deadline ] || fail "/readyz stayed 200 with the scanner stopped"
	sleep 3
done
[ "$(health hogwatch-web)" = healthy ] || fail "the web container went unhealthy with the scanner stopped"
pass "scanner stopped: /readyz fails, the web container stays healthy"
docker compose start hogwatch-scanner >/dev/null 2>&1

# --- 7. wrong setups (Review Focus 4) --------------------------------------
docker compose down -v >/dev/null 2>&1
install "$SCAN"
mkdir data
docker run --rm -v "$(winpath "$PWD/data"):/d" busybox sh -c 'chown 0:0 /d && chmod 755 /d'
docker compose up -d >/dev/null 2>&1 || fail "up on a root-owned ./data failed"
wait_healthy 90 hogwatch-web hogwatch-scanner || fail "a root-owned ./data was not fixed by hogwatch-init"
OWNER=$(docker run --rm -v "$(winpath "$PWD/data"):/d" busybox stat -c '%u:%g %a' /d)
case "$OWNER" in
'10001:10001 2775') pass "a root-owned ./data is handed to 10001:10001 mode 2775 by hogwatch-init" ;;
# Docker Desktop's file sharing drops the setgid bit; on Linux it must stay.
'10001:10001 775') if [ "$(uname -s)" = Linux ]; then fail "./data lost its setgid bit: $OWNER"; else pass "a root-owned ./data is handed to 10001:10001 by hogwatch-init (setgid not kept by this file sharing)"; fi ;;
*) fail "./data is $OWNER" ;;
esac
docker compose down -v >/dev/null 2>&1

if [ "$(uname -s)" = Linux ]; then
	install "$WORK/does-not-exist"
	if UPOUT=$(docker compose up -d 2>&1); then fail "up succeeded with a mistyped SCAN_PATH"; fi
	case "$UPOUT" in *"bind source path does not exist"*) ;; *) fail "up failed, but not because the bind source is missing: $UPOUT" ;; esac
	[ ! -e "$WORK/does-not-exist" ] || fail "a mistyped SCAN_PATH was created as an empty directory"
	pass "a mistyped SCAN_PATH fails and is not created"
	docker compose down -v >/dev/null 2>&1 || true
else
	echo "SKIP  mistyped SCAN_PATH: Docker Desktop creates a missing bind path; this check runs in CI"
fi

echo "smoke: $PASSED checks passed"
