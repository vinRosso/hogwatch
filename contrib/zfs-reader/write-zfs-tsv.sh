#!/bin/sh
# Writes the ZFS numbers Hogwatch shows (quotas, used, available, snapshot
# space) to one tab-separated file, atomically: the old file stays in place
# unless a complete new one is ready. Shared by the sidecar container and the
# systemd timer.
#
# usage: write-zfs-tsv.sh <out-file>
#
# Columns: name mountpoint used avail quota refquota usedbysnapshots. Dataset
# lines come first, then snapshot lines (the reader skips those).
set -eu

out=${1:?usage: write-zfs-tsv.sh <out-file>}
cols=name,mountpoint,used,avail,quota,refquota,usedbysnapshots

# The temp file sits beside the target so the final mv is a same-filesystem rename.
tmp=$(mktemp "$(dirname "$out")/.zfs.tsv.XXXXXX")
trap 'rm -f "$tmp"' EXIT
trap 'exit 1' HUP INT TERM

zfs list -Hp -o "$cols" -t filesystem >"$tmp"
if [ ! -s "$tmp" ]; then
	echo "write-zfs-tsv: zfs list returned no datasets; keeping the previous $out" >&2
	exit 1
fi
zfs list -Hp -o "$cols" -t snapshot >>"$tmp"

# A tab or newline inside a name would give a line the reader rejects; keep the
# previous file rather than replace it with one that cannot be read.
if ! awk -F '\t' 'NF != 7 { bad = 1 } END { exit bad }' "$tmp"; then
	echo "write-zfs-tsv: a line does not have 7 fields; keeping the previous $out" >&2
	exit 1
fi

chmod 0644 "$tmp"
mv "$tmp" "$out"
trap - EXIT
