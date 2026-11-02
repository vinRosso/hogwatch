# Security

Hogwatch is built so that a bug in the web part cannot read your files.

## Design

- **The web container never touches your files.** Only the scanner reads them, and it writes results to a database.
- **The scanner is minimal.** It runs read-only, without network, with only the capability to read any file (`CAP_DAC_READ_SEARCH`), and it refuses writable mounts.
- **It never follows symlinks.** Folders are opened relative to a handle with `O_NOFOLLOW`; listings use `openat2` with `RESOLVE_BENEATH` and `RESOLVE_NO_SYMLINKS`.
- **Access is scoped.** Every query carries the user's allowed paths. Out-of-scope items answer 404, the same as missing ones.
- **No paths in URLs.** Clients refer to folders by a scan and folder number.
- **`/dev/zfs` only on the sidecar**, never on the scanner or the web.
- **Single sign-on links by verified email only.** An existing account is linked only when the provider reports `email_verified`, and afterwards by issuer and subject.
- **File names are bytes.** They are escaped in the interface, and CSV exports guard against formula injection.

## What admins can see

**Admins can see all file names on the server.** Listings are audited. The footer says so on every page.

## Known behavior

Mounts that appear after the scanner started come up writable and are refused or skipped until the scanner restarts.

## Reporting a problem

Open a private security advisory on the GitHub repository.
