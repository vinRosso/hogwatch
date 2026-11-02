# Install

## Requirements

- Docker 25 or newer with the Compose plugin
- Linux kernel 5.12 or newer (the scanner uses `openat2`)
- amd64 or arm64

## Five commands

```bash
mkdir hogwatch && cd hogwatch
curl -fsSLO https://github.com/vinRosso/hogwatch/releases/latest/download/compose.yaml
curl -fsSL  https://github.com/vinRosso/hogwatch/releases/latest/download/env.example -o .env
# edit .env and set SCAN_PATH to the folder you want to analyze
docker compose up -d
docker compose logs hogwatch-web
```

The log prints a one-time setup link. If it starts with `/setup`, put `http://localhost:8080` (or your `HOGWATCH_BASE_URL`) in front of it; on a headless server use an SSH tunnel to port 8080. Open it in a browser, choose a name and password for the first admin and you are in.
If you missed it, create the admin from the command line instead:

```bash
docker compose exec hogwatch-web /hogwatch user create --username admin --admin
```

`SCAN_PATH` is the only required setting. Compose refuses to start when it is missing or the folder does not exist.

## First scan

1. Sign in, open **Settings → Mounts** and choose **New root**.
2. Pick the mounted folder from the list, give the root a name and save.
3. Start a scan from **Settings → Scans**. The overview fills in when it finishes.

![The Mounts page with two roots](/images/admin-mounts.png)

## Optional settings

Put these in `.env` (all are optional):

| Setting | Meaning |
| --- | --- |
| `HOGWATCH_PORT` | Host port, default `8080`. The port is bound to `127.0.0.1` only. |
| `HOGWATCH_BASE_URL` | The public address, for example `https://disk.example.com`. Needed behind a reverse proxy and for sign-in links. |
| `COMPOSE_PROFILES=zfs` | Start the [ZFS sidecar](/zfs). |
| `HOGWATCH_VERSION` | Pin an image version instead of `latest`. |
| `DATA_PATH` | Where the database lives, default `./data`. Use an absolute path to keep it elsewhere. |
| `ZFS_PATH` | Where the ZFS reader writes, default `./zfs`. |

Both images (`hogwatch` and `zfs-reader`) are published on Docker Hub (`vinrosso/hogwatch` and `vinrosso/zfs-reader`). `hogwatch` has the tags `latest`, `X`, `X.Y` and `X.Y.Z`.

### More folders

Add a `compose.override.yaml` next to `compose.yaml`:

```yaml
services:
  hogwatch-scanner:
    volumes:
      - {type: bind, source: /mnt/other, target: /host/other, read_only: true, bind: {propagation: rslave, create_host_path: false}}
```

Keep every scan folder read-only. The scanner refuses a writable one.

### Reverse proxy and HTTPS

The session cookie is `Secure`, so signing in over plain HTTP on a LAN address looks like a login loop. Put Hogwatch behind a TLS proxy (Caddy, nginx, Traefik) and set `HOGWATCH_BASE_URL` to the public `https://` address. `http://localhost` works without TLS.

A proxy on the Docker host reaches the container through Docker's bridge, so Hogwatch sees the bridge gateway as the client, not the proxy. Set `HOGWATCH_TRUSTED_PROXIES` to that address, which you can read with the command below (usually `172.x.0.1`). Without it every visitor shares one address and the login limiter treats them as one person. A proxy that runs in Docker itself should join the same network and publish no port; set `HOGWATCH_TRUSTED_PROXIES` to its address.

```bash
docker network inspect hogwatch_default --format '{{(index .IPAM.Config 0).Gateway}}'
```

### Upgrade

```bash
docker compose pull && docker compose up -d
```

Migrations run on start. Back up first, see [Backup](/backup).
