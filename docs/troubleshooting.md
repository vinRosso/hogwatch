# Troubleshooting

## I sign in and land on the login page again

The session cookie is `Secure`. Use HTTPS (a reverse proxy) or `http://localhost`, and set `HOGWATCH_BASE_URL` to the address you use.

## `docker compose up` says SCAN_PATH is not set

Copy `.env.example` to `.env` and set `SCAN_PATH`.

## There are no roots to choose

The scanner lists what it sees under `/host/data`. Check `docker compose logs hogwatch-scanner`, and that `SCAN_PATH` exists and holds the folders you expect.

## The overview says the scanner is not running

`/readyz` fails when the scanner's heartbeat is older than a minute. Check `docker compose ps` and the scanner log.

## "Config issues" lists a writable mount

The scanner refuses a writable scan folder. Mount it read-only.

## ZFS numbers look old

The reader keeps the last good file when `zfs list` fails. Look at `docker compose logs hogwatch-zfs`; the container turns unhealthy when the file is more than three intervals old.

## The setup link is gone

Create the first admin from the shell, see [Install](/install).
