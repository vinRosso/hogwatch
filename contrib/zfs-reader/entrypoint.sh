#!/bin/sh
# Sidecar entrypoint: refresh the zfs.tsv file every ZFS_INTERVAL seconds.
#
#   entrypoint.sh              run until stopped
#   entrypoint.sh healthcheck  exit 0 when the file is fresh enough
#
# ZFS_OUT (default /out/zfs.tsv), ZFS_INTERVAL (default 300) and ZFS_ONCE=1
# (one cycle, then exit) are the only settings.
set -u

dir=$(dirname "$0")
out=${ZFS_OUT:-/out/zfs.tsv}
interval=${ZFS_INTERVAL:-300}

if [ "${1:-}" = healthcheck ]; then
	# A file not rewritten for three intervals means the sidecar is stuck or
	# every cycle fails; -mmin works in whole minutes, so round up.
	mins=$(((3 * interval + 59) / 60))
	[ -n "$(find "$out" -mmin "-$mins" 2>/dev/null)" ]
	exit $?
fi

# Hogwatch only needs read access. A delegation to everyone or root lets a
# container break-out change the pool, so say so loudly.
warn_delegations() {
	# Every dataset is asked, because a delegation may sit on a child; one
	# warning per distinct delegation, naming the first dataset that shows it.
	for ds in $(zfs list -H -o name -t filesystem 2>/dev/null | cut -f1); do
		zfs allow "$ds" 2>/dev/null | grep -E '^[[:space:]]*(everyone|user root)([[:space:]]|$)' | sed 's/^[[:space:]]*//' |
			while IFS= read -r line; do printf '%s\t%s\n' "$line" "$ds"; done
	done | awk -F '\t' '!seen[$1]++ {
		gsub(/^[ \t]+/, "", $1)
		print "WARNING: ZFS permissions are delegated to everyone or root (" $1 ") on " $2 "; a compromised container with /dev/zfs could use them" > "/dev/stderr"
	}'
}
warn_delegations

trap 'exit 0' TERM INT
while :; do
	# In the background with wait, so a TERM is handled at once even while a
	# hung zfs command is running.
	sh "$dir/write-zfs-tsv.sh" "$out" &
	if ! wait $!; then
		echo "zfs: refresh failed; the previous $out stays in place" >&2
	fi
	[ "${ZFS_ONCE:-}" = 1 ] && exit 0
	sleep "$interval" &
	wait $!
done
