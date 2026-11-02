package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"

	"github.com/vinRosso/hogwatch/internal/audit"
	"github.com/vinRosso/hogwatch/internal/auth/local"
	"github.com/vinRosso/hogwatch/internal/config"
	"github.com/vinRosso/hogwatch/internal/db"
	"github.com/vinRosso/hogwatch/internal/db/dbq"
)

// stdin is where --password-stdin reads; tests replace it.
var stdin io.Reader = os.Stdin

func user(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] != "create" {
		_, _ = fmt.Fprintln(stderr, "usage: hogwatch user create --username NAME [--admin] [--email E] [--display-name D] [--can-list-files] [--password-stdin]")
		return 2
	}
	fl := flag.NewFlagSet("user create", flag.ContinueOnError)
	fl.SetOutput(stderr)
	username := fl.String("username", "", "login name (required)")
	admin := fl.Bool("admin", false, "make the user a local admin")
	email := fl.String("email", "", "email address")
	display := fl.String("display-name", "", "display name (default: the username)")
	canList := fl.Bool("can-list-files", false, "allow file listings")
	pwStdin := fl.Bool("password-stdin", false, "read the password from one line of stdin")
	if err := fl.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	name := local.NormalizeUsername(*username)
	if name == "" {
		_, _ = fmt.Fprintln(stderr, "hogwatch user create: --username is required")
		return 2
	}
	if !local.ValidUsername(name) {
		_, _ = fmt.Fprintln(stderr, "hogwatch user create: username must be 1-64 characters of a-z, 0-9, '.', '_' or '-', starting with a letter or digit")
		return 2
	}
	cfg, err := config.Load(config.FromOS())
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "hogwatch user create: %v\n", err)
		return 1
	}

	generated := false
	var pw string
	if *pwStdin {
		line, err := bufio.NewReader(io.LimitReader(stdin, local.MaxPasswordBytes+2)).ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			_, _ = fmt.Fprintf(stderr, "hogwatch user create: reading password: %v\n", err)
			return 1
		}
		pw = strings.TrimRight(line, "\r\n")
	} else {
		pw, generated = rand.Text()[:20], true
	}
	if len(pw) > local.MaxPasswordBytes || utf8.RuneCountInString(pw) < cfg.Auth.Local.MinPasswordLen {
		_, _ = fmt.Fprintf(stderr, "hogwatch user create: password must be %d to %d characters\n", cfg.Auth.Local.MinPasswordLen, local.MaxPasswordBytes)
		return 1
	}
	hash, err := local.Hash(pw)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "hogwatch user create: %v\n", err)
		return 1
	}

	d, err := db.Open(ctx, filepath.Join(cfg.DataDir, "hogwatch.db"))
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "hogwatch user create: %v\n", err)
		return 1
	}
	defer func() { _ = d.Close() }()
	if err := db.EnsureMigrated(ctx, d); err != nil {
		_, _ = fmt.Fprintf(stderr, "hogwatch user create: %v\n", err)
		return 1
	}
	if *display == "" {
		*display = name
	}
	if err := insertUser(ctx, d, dbq.InsertUserParams{
		Username: name, DisplayName: *display,
		Email:        sql.NullString{String: *email, Valid: *email != ""},
		PasswordHash: sql.NullString{String: hash, Valid: true},
		AdminLocal:   b2i(*admin), CanListFiles: b2i(*canList), CreatedAt: time.Now().Unix(),
	}); err != nil {
		var se *sqlite.Error
		if errors.As(err, &se) && se.Code() == sqlite3.SQLITE_CONSTRAINT_UNIQUE {
			_, _ = fmt.Fprintf(stderr, "hogwatch user create: username %q already exists\n", name)
		} else {
			_, _ = fmt.Fprintf(stderr, "hogwatch user create: %v\n", err)
		}
		return 1
	}
	if generated {
		_, _ = fmt.Fprintf(stdout, "created user %s with password: %s\n", name, pw)
	} else {
		_, _ = fmt.Fprintf(stdout, "created user %s\n", name)
	}
	return 0
}

func insertUser(ctx context.Context, d *sql.DB, p dbq.InsertUserParams) error {
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	q := dbq.New(d).WithTx(tx)
	if _, err := q.InsertUser(ctx, p); err != nil {
		return err
	}
	if err := audit.Write(ctx, q, audit.Entry{Action: "user.created_cli", Target: p.Username}); err != nil {
		return err
	}
	return tx.Commit()
}

func b2i(b bool) int64 {
	if b {
		return 1
	}
	return 0
}
