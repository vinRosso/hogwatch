# Contributing to Hogwatch

## Developer Certificate of Origin

Contributions use the [DCO](https://developercertificate.org). Sign off every commit:

```bash
git commit -s
```

This adds a `Signed-off-by:` trailer with your name and email. CI rejects pull-request commits without it.

## Development setup

- WSL2 Ubuntu (or any Linux) with Docker Desktop's WSL integration enabled for the distro.
- Install the pinned toolchain and check the environment:

```bash
bash scripts/dev-setup.sh
bash scripts/dev-setup.sh --check
```

Tool versions live in `scripts/versions.env` (Go, Node), `tools/go.mod` (lint tools) and `web/package-lock.json` (npm packages).

## Make targets

| Target | What it does |
|---|---|
| `make lint` | golangci-lint, the lint-barrier probes, actionlint, line-ending check |
| `make test` | `go test -race ./...` |
| `make test-integration` | integration tests as root in a privileged Docker container |
| `make build` | builds the SPA, then the `bin/hogwatch` binary |
| `make web` | installs npm packages and builds the SPA into `web/dist` |
| `make check-web` | type-checks the SPA |

On a fresh clone `web/dist` does not exist, so the embed fails to compile. Run `make web-stub` (or `make web`) before any bare `go` command; the make targets do it for you.

## Security invariants

These rules hold everywhere in the code. A change that bends one needs a design discussion first.

1. **The web process never touches the host filesystem.** Only the scanner reads host paths. Lint rules in `.golangci.yml` enforce this; do not weaken them.
2. **Every data query goes through the repository layer with a `Scope` value.** Out-of-scope nodes return 404, the same as nonexistent ones.
3. **Clients reference nodes as `<scan_id>.<dir_id>`, never by filesystem path.** `/resolve` matches name segments in the database only.
4. **The scanner never follows symlinks.** Directories are opened fd-relative with `O_NOFOLLOW`. Listings use `openat2(RESOLVE_BENEATH | RESOLVE_NO_SYMLINKS | RESOLVE_NO_XDEV)` and check (dev, ino) against the scan.
5. **The scanner's privileges stay minimal.** It runs as root with only `CAP_DAC_READ_SEARCH`, no network and read-only mounts, and refuses writable filesystems.
6. **`/dev/zfs` goes only to the optional `hogwatch-zfs` sidecar**, never to the scanner or the web.
7. **OIDC auto-links an existing account only when `email_verified` is true.** After linking, match on (issuer, sub) only.
8. **Filenames are raw bytes.** Escape them in the UI; guard CSV exports against formula injection.

