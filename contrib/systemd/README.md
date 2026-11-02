# ZFS numbers without the sidecar container

If the `hogwatch-zfs` container cannot read `/dev/zfs` on your host, a systemd timer can write the same `zfs.tsv` file.

1. Install the script and the units (the path in `ReadWritePaths` and `ExecStart` must be your compose project's `zfs` folder):

   ```bash
   sudo install -m 0755 contrib/zfs-reader/write-zfs-tsv.sh /usr/local/bin/
   sudo install -m 0644 contrib/systemd/hogwatch-zfs.service contrib/systemd/hogwatch-zfs.timer /etc/systemd/system/
   sudoedit /etc/systemd/system/hogwatch-zfs.service
   sudo systemctl daemon-reload
   sudo systemctl enable --now hogwatch-zfs.timer
   ```

2. Leave `COMPOSE_PROFILES=zfs` unset in `.env`, so the container does not start.
3. Check: `systemctl list-timers hogwatch-zfs.timer` and `cat <your zfs folder>/zfs.tsv`.

If `zfs list` needs more privileges than user 10001 has, set `User=root` in the service.
