# ZFS

A ZFS dataset's quota, free space and snapshot space are not visible from a plain folder scan. The optional reader reads them with `zfs list` and writes `zfs.tsv`, which the scanner shows next to your roots.

## Sidecar (default)

Add to `.env`:

```bash
COMPOSE_PROFILES=zfs
```

There is one image, `vinrosso/zfs-reader`, for amd64 and arm64. It carries the newest OpenZFS userland (from Debian trixie-backports), which talks to older kernel modules too: it only runs `zfs list` with long-standing properties. It gets `/dev/zfs` and nothing else.

If `zfs list` fails inside the container on your host (check `docker compose logs hogwatch-zfs`), use the systemd timer below, which runs your host's own `zfs`, and open an issue with your `zfs version` output.

The reader runs as an unprivileged user. If `/dev/zfs` is not accessible to it on your host, run the container as root; it still only runs `zfs list`. At start it prints a `WARNING` if `zfs allow` delegates rights to `everyone` or `user root`.

A failed `zfs list` keeps the previous `zfs.tsv`; the container reports unhealthy when the file gets old, so stale numbers never go unnoticed.

## systemd timer (fallback)

If the sidecar cannot reach `/dev/zfs`, use the units in `contrib/systemd` instead: a oneshot service and a five-minute timer writing the same file into `./zfs`.
