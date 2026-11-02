// Package db opens the main database (hogwatch.db) and applies
// its embedded goose migrations. Queries live in queries/ and are generated
// into package dbq by sqlc (make gen).
package db

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"time"

	"github.com/pressly/goose/v3"
	_ "modernc.org/sqlite" // registers the "sqlite" database/sql driver
)

// mainDBQuery holds the per-connection settings of the main DB. The driver
// applies every _pragma on each new pooled connection. _txlock=immediate makes
// BeginTx take the write lock up front, so two writers never deadlock on a
// read-to-write lock upgrade, which busy_timeout cannot retry.
const mainDBQuery = "_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)" +
	"&_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)&_txlock=immediate"

// HeartbeatMaxAge is how old the scanner's heartbeat may be before the web's
// /readyz and the scanner container's healthcheck call the scanner stopped.
const HeartbeatMaxAge = 60 * time.Second

//go:embed migrations/*.sql
var migrationsEmbed embed.FS

// FileURI builds a SQLite file: URI for path with raw query q. A relative
// path is made absolute first: rendered as is, its first segment would be read
// as a URI authority (file://data/hogwatch.db). The path is percent-escaped by
// net/url, so a space, '#', '?' or '%' in a directory name cannot truncate the
// path or leak into the query.
func FileURI(path, q string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("db: absolute path of %s: %w", path, err)
	}
	return (&url.URL{Scheme: "file", Path: filepath.ToSlash(abs), RawQuery: q}).String(), nil
}

// fileMode is the mode of hogwatch.db. SQLite gives the -wal and
// -shm files the mode of the database file.
const fileMode = 0o660

// create makes the database file with fileMode when it does not exist yet.
// SQLite would create it 0644, which the root scanner (gid 10001, no
// DAC_OVERRIDE) could not write. An existing file is left alone: a process
// without FOWNER cannot chmod a file someone else owns.
func create(path string) error {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_EXCL, fileMode) //nolint:gosec // the configured data dir, never request input
	switch {
	case errors.Is(err, fs.ErrExist):
		return nil
	case err != nil:
		return err
	}
	// Chmod: the open mode is filtered by the umask.
	if err := f.Chmod(fileMode); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

// Open opens (creating if needed, with mode 0660) the main DB at path and
// pings it.
func Open(ctx context.Context, path string) (*sql.DB, error) {
	uri, err := FileURI(path, mainDBQuery)
	if err != nil {
		return nil, err
	}
	if err := create(path); err != nil {
		return nil, fmt.Errorf("db: open %s: %w", path, err)
	}
	d, err := sql.Open("sqlite", uri)
	if err != nil {
		return nil, fmt.Errorf("db: open %s: %w", path, err)
	}
	if err := d.PingContext(ctx); err != nil {
		_ = d.Close()
		return nil, fmt.Errorf("db: open %s: %w", path, err)
	}
	return d, nil
}

// OpenReadOnly opens an existing main DB without creating it, applying
// migrations or taking a write lock. SQLite would create a missing -wal or
// -shm file even for a read-only connection, and a file made by the
// scanner's root user could not be written by the web later; so when either
// is absent (no process has the database open) it fails instead. It is for
// the healthcheck.
func OpenReadOnly(ctx context.Context, path string) (*sql.DB, error) {
	for _, suffix := range []string{"-wal", "-shm"} {
		if _, err := os.Stat(path + suffix); err != nil {
			return nil, fmt.Errorf("db: open %s: no process has it open: %w", path, err)
		}
	}
	uri, err := FileURI(path, "mode=ro&readonly_shm=1&_pragma=busy_timeout(3000)")
	if err != nil {
		return nil, err
	}
	d, err := sql.Open("sqlite", uri)
	if err != nil {
		return nil, fmt.Errorf("db: open %s: %w", path, err)
	}
	if err := d.PingContext(ctx); err != nil {
		_ = d.Close()
		return nil, fmt.Errorf("db: open %s: %w", path, err)
	}
	return d, nil
}

func newProvider(d *sql.DB) (*goose.Provider, error) {
	fsys, err := fs.Sub(migrationsEmbed, "migrations")
	if err != nil {
		return nil, err
	}
	p, err := goose.NewProvider(goose.DialectSQLite3, d, fsys)
	if err != nil {
		return nil, fmt.Errorf("db: migrations: %w", err)
	}
	return p, nil
}

// Migrate applies every pending migration.
func Migrate(ctx context.Context, d *sql.DB) error {
	p, err := newProvider(d)
	if err != nil {
		return err
	}
	if _, err := p.Up(ctx); err != nil {
		return fmt.Errorf("db: migrate: %w", err)
	}
	return nil
}

// Pending reports whether any migration has not been applied yet.
func Pending(ctx context.Context, d *sql.DB) (bool, error) {
	p, err := newProvider(d)
	if err != nil {
		return false, err
	}
	pending, err := p.HasPending(ctx)
	if err != nil {
		return false, fmt.Errorf("db: pending: %w", err)
	}
	return pending, nil
}

// EnsureMigrated applies pending migrations. Two processes may start together
// and race on the same database; the loser's error is ignored when, once it
// has finished, nothing is pending, because the winner migrated first.
func EnsureMigrated(ctx context.Context, d *sql.DB) error {
	err := Migrate(ctx, d)
	if err == nil {
		return nil
	}
	if pending, perr := Pending(ctx, d); perr == nil && !pending {
		return nil
	}
	return err
}
