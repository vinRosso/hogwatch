package main

import (
	"context"
	"crypto/rand"
	"database/sql"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	webassets "github.com/vinRosso/hogwatch/web"

	"github.com/vinRosso/hogwatch/internal/auth/local"
	"github.com/vinRosso/hogwatch/internal/auth/oidc"
	"github.com/vinRosso/hogwatch/internal/auth/session"
	"github.com/vinRosso/hogwatch/internal/config"
	"github.com/vinRosso/hogwatch/internal/db"
	"github.com/vinRosso/hogwatch/internal/db/dbq"
	"github.com/vinRosso/hogwatch/internal/repo"
	"github.com/vinRosso/hogwatch/internal/web"
)

func serve(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fl := flag.NewFlagSet("serve", flag.ContinueOnError)
	fl.SetOutput(stderr)
	if err := fl.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}

	cfg, err := config.Load(config.FromOS())
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "hogwatch serve: %v\n", err)
		return 1
	}
	log := config.NewLogger(cfg.Log, stdout)
	log.Info("config loaded", "cfg", cfg)

	mainDB, err := db.Open(ctx, filepath.Join(cfg.DataDir, "hogwatch.db"))
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "hogwatch serve: %v\n", err)
		return 1
	}
	defer func() { _ = mainDB.Close() }()
	if err := db.EnsureMigrated(ctx, mainDB); err != nil {
		_, _ = fmt.Fprintf(stderr, "hogwatch serve: %v\n", err)
		return 1
	}
	log.Info("migrations applied")

	sub, err := fs.Sub(webassets.Dist, "dist")
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "hogwatch serve: %v\n", err)
		return 1
	}
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	now := time.Now
	sessions := session.New(mainDB, cfg.Session, now)
	repository := repo.New(mainDB, cfg.DataDir, log)
	defer func() { _ = repository.Close() }()
	setup := local.NewSetupToken(now, nil)
	deps := web.Deps{
		DB: mainDB, Cfg: cfg, Sessions: sessions, Repo: repository, Limiter: local.NewLimiter(now),
		Setup: setup, SPA: sub, Log: log, Now: now,
	}
	// Discovery is lazy, so an unreachable identity provider does not stop serve.
	if cfg.Auth.OIDC.Issuer != "" {
		deps.OIDC = oidc.New(cfg.Auth.OIDC, cfg.OIDCRedirectURL(), &http.Client{Timeout: 10 * time.Second})
		deps.Flows = oidc.NewFlows(now, rand.Reader)
		log.Info("oidc enabled", "issuer", cfg.Auth.OIDC.Issuer)
	}
	handler, err := web.New(deps)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "hogwatch serve: %v\n", err)
		return 1
	}
	if !cfg.Auth.Local.Enabled && len(cfg.Auth.Local.BreakGlassAdmins) == 0 {
		log.Warn("local login is off and no break-glass admin is configured; only OIDC can log in")
	}
	mctx, stopMaintain := context.WithCancel(ctx)
	maintained := make(chan struct{})
	go func() {
		defer close(maintained)
		maintain(mctx, mainDB, cfg, log, sessions, repository, setup, deps.Flows, now)
	}()
	// Registered after the repo and database closers, so it runs first.
	defer func() { stopMaintain(); <-maintained }()

	ln, err := net.Listen("tcp", cfg.Listen)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "hogwatch serve: %v\n", err)
		return 1
	}
	log.Info("listening", "addr", ln.Addr().String())
	if err := web.Serve(ctx, ln, handler); err != nil {
		_, _ = fmt.Fprintf(stderr, "hogwatch serve: %v\n", err)
		return 1
	}
	return 0
}

// maintain runs once at start and then every minute: it keeps the setup token
// alive while no admin exists, drops it once one does, deletes expired
// sessions, drops expired OIDC flows and closes generations that are no longer served.
func maintain(ctx context.Context, d *sql.DB, cfg config.Config, log *slog.Logger,
	sessions *session.Store, repository *repo.Repo, setup *local.SetupToken, flows *oidc.Flows, now func() time.Time) {
	q := dbq.New(d)
	logged := false
	tick := func() {
		n, err := q.CountEnabledAdmins(ctx)
		switch {
		case err != nil:
			log.Error("count admins", "err", err)
		case n > 0:
			setup.Invalidate()
		default:
			tok, exp := setup.Current()
			if !logged || tok == "" || !exp.After(now()) {
				if logged || tok == "" {
					tok = setup.Rotate()
				}
				logged = true
				log.Warn("setup required: open the URL to create the first admin",
					"url", strings.TrimRight(cfg.BaseURL, "/")+"/setup#token="+tok, "valid_for", local.SetupTokenTTL.String())
			}
		}
		if _, err := sessions.DeleteExpired(ctx); err != nil {
			log.Error("delete expired sessions", "err", err)
		}
		if flows != nil {
			flows.Sweep()
		}
		repository.Sweep(ctx)
	}
	tick()
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			tick()
		}
	}
}
