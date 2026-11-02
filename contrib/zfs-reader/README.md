# hogwatch-zfs: the ZFS reader

A small container that reads quota, used and available space from ZFS and writes it to `zfs.tsv` every five minutes. Hogwatch's scanner then shows those numbers next to the folder sizes.

- `Dockerfile` builds the image for a Debian or Ubuntu host: `docker build --build-arg BASE=debian:13 -t hogwatch-zfs .`. The released image uses `--build-arg BASE=debian:13 --build-arg BACKPORTS=trixie` (the newest ZFS); build with another base if you want a different ZFS version.
- `write-zfs-tsv.sh` does the work and is also used by the systemd timer in `../systemd`.
- `zfs-probe.sh` checks, before you trust the sidecar, that a container with `/dev/zfs` and no capabilities can read but not change a pool (`Dockerfile.probe` builds its image).

Settings (environment): `ZFS_INTERVAL` seconds between refreshes (default 300), `ZFS_OUT` output file (default `/out/zfs.tsv`).
