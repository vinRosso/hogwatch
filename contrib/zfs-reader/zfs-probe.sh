#!/usr/bin/env bash
# /dev/zfs probe. Runs on the ZFS host, as root.
#
# usage: sudo bash zfs-probe.sh <pool>/<scratch-dataset>
#
# The scratch dataset must be an empty dataset named <pool>/hogwatch-probe
# (any other name is refused), made for this probe. In a
# container that has /dev/zfs but no capabilities, for uid 0 and uid 10001, it
# checks that the read commands work and that "zfs snapshot" and "zfs set" are
# refused. It prints CHECK lines and never cleans up: a leftover snapshot or
# property is reported as LEAK together with the command to remove it.
#
# Exit: 0 all checks pass, 1 a check failed or something leaked, 2 bad usage.

set -u

IMAGE=hogwatch-zfs-probe

usage() {
	cat >&2 <<USAGE
usage: sudo bash $0 <pool>/<scratch-dataset>

  Probes /dev/zfs from a cap-dropped container. The argument must be an
  existing scratch dataset (it contains a "/"). Nothing is changed on it
  unless the container is wrongly allowed to.
USAGE
	exit 2
}

[ "$#" -eq 1 ] || usage
case "$1" in
-* | /* | *@* | *' '*) usage ;;
*/*) ;;
*) usage ;;
esac
DS=$1

# Safety: only a dataset named hogwatch-probe is accepted, so the script can
# never be pointed at a real dataset.
if [ "${DS##*/}" != hogwatch-probe ]; then
	echo "zfs-probe: refusing '$DS'. The last path component must be exactly 'hogwatch-probe'." >&2
	echo "Create a scratch dataset <pool>/hogwatch-probe and pass that." >&2
	exit 2
fi
if [ "$(id -u)" -ne 0 ]; then
	echo "zfs-probe: run as root (sudo bash $0 $DS)." >&2
	exit 2
fi

if ! zfs list -H "$DS" >/dev/null; then
	echo "zfs-probe: cannot list dataset '$DS'. Create it first (zfs create $DS)." >&2
	exit 2
fi
if ! docker image inspect "$IMAGE" >/dev/null 2>&1; then
	echo "zfs-probe: image '$IMAGE' not found. Build it first: docker build -t $IMAGE -f Dockerfile.probe --build-arg BASE=<host distro image> ." >&2
	exit 2
fi

# check_leak prints LEAK lines and returns 1 if a probe leftover exists.
check_leak() {
	local leak=0
	if zfs list -H -t snapshot -o name "$DS@hogwatch-probe" >/dev/null 2>&1; then
		echo "LEAK snapshot $DS@hogwatch-probe exists. Remove it yourself with: zfs destroy '$DS@hogwatch-probe'"
		leak=1
	fi
	if [ -n "$(zfs get -H -s local -o value hogwatch:probe "$DS" 2>/dev/null)" ]; then
		echo "LEAK property hogwatch:probe is set on $DS. Remove it yourself with: zfs inherit hogwatch:probe '$DS'"
		leak=1
	fi
	return "$leak"
}

echo "== host: ls -l /dev/zfs"
ls -l /dev/zfs
echo "== host: zfs version"
zfs version
while read -r pool; do
	echo "== host: zfs allow $pool"
	zfs allow "$pool"
done < <(zpool list -H -o name)

if ! check_leak; then
	echo "zfs-probe: leftovers from an earlier run. Clean them up, then run again." >&2
	exit 1
fi

# Runs inside the container with the dataset as $1. One RES line per command:
# "RES <name> <exit> <detail>". Only line counts are printed on success.
# shellcheck disable=SC2016 # the script runs inside the container, where these expand
INNER='
DS=$1
res() {
	if [ "$2" -eq 0 ]; then
		echo "RES $1 0 lines=$(printf "%s\n" "$3" | grep -c .)"
	else
		echo "RES $1 $2 $(printf "%s\n" "$3" | head -n 1)"
	fi
}
echo "VERSION $(zfs version 2>&1 | tr "\n" " ")"
o=$(zfs list -Hp -o name,mountpoint,used,avail,quota,refquota,usedbysnapshots 2>&1); res list-datasets $? "$o"
o=$(zfs list -Hp -t snapshot -o name,used 2>&1); res list-snapshots $? "$o"
o=$(zfs snapshot "$DS@hogwatch-probe" 2>&1); res snapshot $? "$o"
o=$(zfs set hogwatch:probe=1 "$DS" 2>&1); res set $? "$o"
'

pass=0
fail=0
leaked=0

report() { # uid name PASS|FAIL detail
	echo "CHECK $1 $2 $3 $4"
	if [ "$3" = PASS ]; then pass=$((pass + 1)); else fail=$((fail + 1)); fi
}

for uid in 0 10001; do
	echo "== container: --user $uid"
	out=$(docker run --rm --user "$uid" --cap-drop ALL --security-opt no-new-privileges \
		--network none --read-only --device /dev/zfs "$IMAGE" sh -c "$INNER" sh "$DS" 2>&1)
	drc=$?
	printf '%s\n' "$out" | sed 's/^/   /'

	declare -A seen=()
	while read -r tag name rc detail; do
		[ "$tag" = RES ] || continue
		seen[$name]=1
		case "$name" in
		list-datasets | list-snapshots)
			# Datasets need at least one line. Zero snapshots is a valid host.
			n=${detail#lines=}
			min=0
			[ "$name" = list-datasets ] && min=1
			if [ "$rc" = 0 ] && [ "$n" != "$detail" ] && [ "$n" -ge "$min" ] 2>/dev/null; then
				report "$uid" "$name" PASS "exit=0 count=$n"
			else
				report "$uid" "$name" FAIL "exit=$rc ${detail:-no output}"
			fi
			;;
		snapshot | set)
			if [ "$rc" = 0 ]; then
				report "$uid" "$name" FAIL "succeeded, but it must be refused"
			elif printf '%s\n' "$detail" | grep -qiE 'permission denied|not permitted|insufficient privileges|operation not permitted'; then
				report "$uid" "$name" PASS "refused: $detail"
			else
				report "$uid" "$name" FAIL "unexpected error: ${detail:-exit=$rc}"
			fi
			;;
		esac
	done <<<"$out"
	for name in list-datasets list-snapshots snapshot set; do
		[ -n "${seen[$name]:-}" ] || report "$uid" "$name" FAIL "no result (docker exit=$drc, see output above)"
	done
	unset seen

	if ! check_leak; then
		leaked=1
		echo "zfs-probe: stopping. Clean up the LEAK above, then run again." >&2
		break
	fi
done

echo "SUMMARY pass=$pass fail=$fail leak=$leaked"
if [ "$fail" -eq 0 ] && [ "$leaked" -eq 0 ]; then
	exit 0
fi
exit 1
