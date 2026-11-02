package web

import (
	"context"
	"crypto/rand"
	"database/sql"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vinRosso/hogwatch/internal/auth/local"
	"github.com/vinRosso/hogwatch/internal/auth/oidc"
	"github.com/vinRosso/hogwatch/internal/auth/oidc/oidctest"
	"github.com/vinRosso/hogwatch/internal/auth/session"
	"github.com/vinRosso/hogwatch/internal/config"
	"github.com/vinRosso/hogwatch/internal/cube"
	"github.com/vinRosso/hogwatch/internal/db"
	"github.com/vinRosso/hogwatch/internal/gen"
	"github.com/vinRosso/hogwatch/internal/gen/gentest"
	"github.com/vinRosso/hogwatch/internal/repo"
)

var roles = []string{"anon", "plain", "owner", "lister", "admin", "disabled"}

type env struct {
	t        testing.TB
	db       *sql.DB
	dataDir  string
	now      time.Time
	cfg      config.Config
	sessions *session.Store
	repo     *repo.Repo
	limiter  *local.Limiter
	setup    *local.SetupToken
	h        http.Handler
	mux      *http.ServeMux
	table    map[string]Access
	users    map[string]int64
	cookies  map[string]string
	flows    *oidc.Flows
}

type envOpt func(*Deps)

// newEnv builds a migrated database with one root, one generation (scan 1,
// dirs a, a/b, d) and the users of the role matrix, each with a session.
func newEnv(t testing.TB, opts ...envOpt) *env {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	d, err := db.Open(ctx, filepath.Join(dir, "hogwatch.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	if err := db.Migrate(ctx, d); err != nil {
		t.Fatal(err)
	}
	e := &env{
		t: t, db: d, dataDir: dir, now: time.Unix(1_700_000_000, 0),
		users: map[string]int64{}, cookies: map[string]string{},
	}
	e.cfg = config.Config{
		Session: config.Session{IdleTimeout: 12 * time.Hour, AbsoluteTimeout: 168 * time.Hour, OIDCAdminTimeout: 12 * time.Hour},
		Auth:    config.Auth{Local: config.AuthLocal{Enabled: true, MinPasswordLen: 8}},
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	now := func() time.Time { return e.now }
	e.sessions = session.New(d, e.cfg.Session, now)
	e.repo = repo.New(d, dir, log)
	t.Cleanup(func() { _ = e.repo.Close() })
	e.limiter = local.NewLimiter(now)
	e.setup = local.NewSetupToken(now, nil)

	e.exec(`INSERT INTO scan_roots (id, name, container_path, host_path) VALUES (1, 'r', '/mnt/r', '/srv/r')`)
	cell := func(c cube.Category) cube.Cell { return cube.Cell{Category: uint8(c)} }
	file := gentest.BuildWith(t, dir, 1, 1, []gentest.Dir{
		{Rel: "a", Files: 1, BytesAlloc: 10, Cells: map[cube.Cell][2]int64{cell(cube.Video): {10, 1}},
			Exts: []gen.TopExt{{Ext: "bin", Bytes: 20, Files: 1}}},
		{Rel: "a/b", Files: 2, BytesAlloc: 20, Cells: map[cube.Cell][2]int64{cell(cube.Image): {20, 2}},
			Top:    []gentest.TopFile{{Kind: "largest", Rank: 1, Name: "big.bin", Size: 20, Alloc: 20, Mtime: 5}},
			Errors: []gen.ErrorRow{{Name: []byte("e"), Op: "open", Errno: 13}}},
		{Rel: "d", Files: 3, BytesAlloc: 30, Cells: map[cube.Cell][2]int64{cell(cube.Document): {30, 3}}},
	}, gentest.BuildOpts{TopAnchors: []string{"", "a"}})
	e.exec(`INSERT INTO scans (id, root_id, file, status, trigger, started_at, finished_at) VALUES (1, 1, ?, 'done', 'manual', 1, 2)`, filepath.Base(file))
	e.exec(`UPDATE scan_roots SET current_scan_id = 1`)
	e.exec(`INSERT INTO scanner_status (id, heartbeat_at, version) VALUES (1, ?, 'test')`, e.now.Unix())

	for _, role := range roles[1:] {
		res, err := d.Exec(`INSERT INTO users (username, created_at, can_list_files, admin_local) VALUES (?, 1, ?, ?)`,
			role, role == "lister", role == "admin")
		if err != nil {
			t.Fatal(err)
		}
		id, _ := res.LastInsertId()
		e.users[role] = id
		tok, _, err := e.sessions.Create(ctx, id, false, "192.0.2.1", "test")
		if err != nil {
			t.Fatal(err)
		}
		e.cookies[role] = tok
	}
	e.exec(`UPDATE users SET disabled = 1 WHERE username = 'disabled'`)
	// The admin can sign in, as the last-admin guard requires of a counted admin.
	e.exec(`UPDATE users SET password_hash = ? WHERE username = ?`, "$argon2id$fixture", "admin")
	for role, rel := range map[string]string{"owner": "a", "lister": "d"} {
		e.exec(`INSERT INTO user_paths (user_id, root_id, rel_path, created_at) VALUES (?, 1, CAST(? AS BLOB), 1)`, e.users[role], rel)
	}

	deps := Deps{
		DB: d, Cfg: e.cfg, Sessions: e.sessions, Repo: e.repo, Limiter: e.limiter, Setup: e.setup,
		Log: log, Now: now,
	}
	for _, o := range opts {
		o(&deps)
	}
	e.cfg = deps.Cfg
	e.flows = deps.Flows
	e.h, e.mux, e.table, err = build(deps)
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func (e *env) exec(q string, args ...any) {
	e.t.Helper()
	if _, err := e.db.Exec(q, args...); err != nil {
		e.t.Fatalf("%s: %v", q, err)
	}
}

// req is a request description.
type req struct {
	method, path, role, body string
	header                   map[string]string
	bare                     bool   // add neither CSRF header nor content type
	remote                   string // RemoteAddr, "host:port"; default is the httptest one
}

func (e *env) do(r req) *httptest.ResponseRecorder {
	e.t.Helper()
	hr := httptest.NewRequest(r.method, r.path, strings.NewReader(r.body))
	if r.remote != "" {
		hr.RemoteAddr = r.remote
	}
	if r.body == "" {
		hr.Body = http.NoBody
		hr.ContentLength = 0
	}
	if !r.bare && stateChanging(r.method) {
		hr.Header.Set(csrfHeader, csrfValue)
		if r.body != "" {
			hr.Header.Set("Content-Type", "application/json")
		}
	}
	if tok, ok := e.cookies[r.role]; ok {
		hr.AddCookie(&http.Cookie{Name: session.CookieName, Value: tok}) //nolint:gosec // a request cookie, not a response one
	}
	for k, v := range r.header {
		hr.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	e.h.ServeHTTP(w, hr)
	return w
}

// withOIDC enables OIDC against a mock issuer. edit tweaks the config first.
func withOIDC(iss *oidctest.Issuer, edit ...func(*config.Config)) envOpt {
	return func(d *Deps) {
		d.Cfg.BaseURL = "https://hog.example"
		d.Cfg.Auth.OIDC = config.AuthOIDC{
			Issuer: iss.URL, ClientID: iss.ClientID, ClientSecret: iss.ClientSecret,
			GroupsClaim: "groups", AdminGroup: "admins",
		}
		for _, f := range edit {
			f(&d.Cfg)
		}
		iss.Register(d.Cfg.OIDCRedirectURL())
		d.OIDC = oidc.New(d.Cfg.Auth.OIDC, d.Cfg.OIDCRedirectURL(), &http.Client{Timeout: 5 * time.Second})
		d.Flows = oidc.NewFlows(d.Now, rand.Reader)
	}
}
