# ZFS

A ZFS dataset's quota, free space and snapshot space are not visible from a plain folder scan. The optional reader reads them with `zfs list` and writes `zfs.tsv`, which the scanner shows next to your roots.

## Sidecar (default)

Add to `.env`:

```bash
COMPOSE_PROFILES=zfs
ZFS_READER_TAG=debian13
```

Pick the tag that matches your host's ZFS userland: `ubuntu24`, `debian12`, `debian13` or `proxmox8`. The image gets `/dev/zfs` and nothing else. All tags are amd64 and arm64 except `proxmox8`, which is amd64 only because Proxmox publishes no arm64 packages. Each tag is also published as `X.Y.Z-<tag>` (for example `0.1.0-debian13`) to pin an exact release. To build your own, see `contrib/zfs-reader` in the repository.

The reader runs as an unprivileged user. If `/dev/zfs` is not accessible to it on your host, run the container as root; it still only runs `zfs list`. At start it prints a `WARNING` if `zfs allow` delegates rights to `everyone` or `user root`.

A failed `zfs list` keeps the previous `zfs.tsv`; the container reports unhealthy when the file gets old, so stale numbers never go unnoticed.

## systemd timer (fallback)

If the sidecar cannot reach `/dev/zfs`, use the units in `contrib/systemd` instead: a oneshot service and a five-minute timer writing the same file into `./zfs`.
