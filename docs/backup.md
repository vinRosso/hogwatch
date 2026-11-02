# Backup

Everything lives in `./data` (or `DATA_PATH` in `.env`).

- `hogwatch.db` holds users, roots, settings, jobs and the scan history. **This is what you back up.**
- `scans/` holds one database per scan. It can be rebuilt by scanning again, but history is in `hogwatch.db`.

Stop the stack, copy the file, start it again:

```bash
docker compose stop hogwatch-web hogwatch-scanner
cp data/hogwatch.db* /backup/
docker compose start hogwatch-web hogwatch-scanner
```

If the stack was not stopped cleanly, copy the `-wal` and `-shm` files with it.

Keep `./data` on a local filesystem; SQLite does not work reliably over NFS.
