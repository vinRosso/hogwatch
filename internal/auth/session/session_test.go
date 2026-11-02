package session

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vinRosso/hogwatch/internal/config"
	"github.com/vinRosso/hogwatch/internal/db"
)

var cfg = config.Session{IdleTimeout: 12 * time.Hour, AbsoluteTimeout: 7 * 24 * time.Hour, OIDCAdminTimeout: 12 * time.Hour}

type fixture struct {
	t   *testing.T
	db  *sql.DB
	st  *Store
	now time.Time
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	ctx := context.Background()
	d, err := db.Open(ctx, filepath.Join(t.TempDir(), "hogwatch.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	if err := db.Migrate(ctx, d); err != nil {
		t.Fatal(err)
	}
	f := &fixture{t: t, db: d, now: time.Unix(1_700_000_000, 0)}
	f.st = New(d, cfg, func() time.Time { return f.now })
	return f
}

func (f *fixture) user(name string, disabled int) int64 {
	f.t.Helper()
	res, err := f.db.Exec(`INSERT INTO users (username, created_at, disabled) VALUES (?, 1, ?)`, name, disabled)
	if err != nil {
		f.t.Fatal(err)
	}
	id, _ := res.LastInsertId()
	return id
}

func (f *fixture) lastSeen(token string) int64 {
	f.t.Helper()
	raw, _ := base64.RawURLEncoding.DecodeString(token)
	sum := sha256.Sum256(raw)
	var v int64
	if err := f.db.QueryRow(`SELECT last_seen_at FROM sessions WHERE id_hash = ?`, sum[:]).Scan(&v); err != nil {
		f.t.Fatal(err)
	}
	return v
}

func (f *fixture) count(where string, args ...any) int {
	f.t.Helper()
	var n int
	if err := f.db.QueryRow(`SELECT count(*) FROM sessions `+where, args...).Scan(&n); err != nil {
		f.t.Fatal(err)
	}
	return n
}

func TestStore_CreateLookup(t *testing.T) {
	f := newFixture(t)
	uid := f.user("alice", 0)
	tok, exp, err := f.st.Create(context.Background(), uid, false, "10.0.0.1", "ua")
	if err != nil {
		t.Fatal(err)
	}
	if len(tok) != 43 {
		t.Fatalf("token length %d", len(tok))
	}
	if !exp.Equal(f.now.Add(7 * 24 * time.Hour)) {
		t.Fatalf("expiry %v", exp)
	}
	raw, _ := base64.RawURLEncoding.DecodeString(tok)
	sum := sha256.Sum256(raw)
	if f.count(`WHERE id_hash = ?`, sum[:]) != 1 || f.count(`WHERE id_hash = ?`, raw) != 0 {
		t.Fatal("DB must hold sha256(token), never the token")
	}
	u, err := f.st.Lookup(context.Background(), tok)
	if err != nil || u.ID != uid || u.Username != "alice" || u.IsAdmin() {
		t.Fatalf("Lookup = %+v, %v", u, err)
	}
}

func TestStore_IdleExpiry(t *testing.T) {
	f := newFixture(t)
	tok, _, _ := f.st.Create(context.Background(), f.user("a", 0), false, "", "")
	f.now = f.now.Add(12*time.Hour - time.Second)
	if _, err := f.st.Lookup(context.Background(), tok); err != nil {
		t.Fatalf("just before idle: %v", err)
	}
	// The lookup touched last_seen, so idle restarts from there.
	f.now = f.now.Add(12 * time.Hour)
	if _, err := f.st.Lookup(context.Background(), tok); err != ErrNoSession {
		t.Fatalf("at idle: %v", err)
	}
	if f.count("") != 0 {
		t.Fatal("expired row kept")
	}
}

func TestStore_AbsoluteExpiry(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	tok, exp, _ := f.st.Create(ctx, f.user("a", 0), false, "", "")
	oidc, oexp, _ := f.st.Create(ctx, f.user("b", 0), true, "", "")
	if !oexp.Equal(f.now.Add(12 * time.Hour)) {
		t.Fatalf("oidc expiry %v", oexp)
	}
	// Keep both active so only the absolute limit can end them.
	for f.now.Add(6 * time.Hour).Before(exp) {
		f.now = f.now.Add(6 * time.Hour)
		if f.now.Before(oexp) {
			if _, err := f.st.Lookup(ctx, oidc); err != nil {
				t.Fatalf("oidc session ended early: %v", err)
			}
		}
		if _, err := f.st.Lookup(ctx, tok); err != nil {
			t.Fatalf("session ended early at %v: %v", f.now, err)
		}
	}
	if _, err := f.st.Lookup(ctx, oidc); err != ErrNoSession {
		t.Fatalf("oidc session past 12h: %v", err)
	}
	f.now = exp
	if _, err := f.st.Lookup(ctx, tok); err != ErrNoSession {
		t.Fatalf("at expires_at: %v", err)
	}
}

func TestStore_TouchThrottle(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	tok, _, _ := f.st.Create(ctx, f.user("a", 0), false, "", "")
	start := f.now.Unix()
	f.now = f.now.Add(30 * time.Second)
	if _, err := f.st.Lookup(ctx, tok); err != nil {
		t.Fatal(err)
	}
	if got := f.lastSeen(tok); got != start {
		t.Fatalf("written after 30s: %d", got)
	}
	f.now = f.now.Add(31 * time.Second)
	if _, err := f.st.Lookup(ctx, tok); err != nil {
		t.Fatal(err)
	}
	if got := f.lastSeen(tok); got != f.now.Unix() {
		t.Fatalf("not written after 61s: %d", got)
	}
}

func TestStore_DisabledUser(t *testing.T) {
	f := newFixture(t)
	uid := f.user("a", 0)
	tok, _, _ := f.st.Create(context.Background(), uid, false, "", "")
	if _, err := f.db.Exec(`UPDATE users SET disabled = 1 WHERE id = ?`, uid); err != nil {
		t.Fatal(err)
	}
	if _, err := f.st.Lookup(context.Background(), tok); err != ErrNoSession {
		t.Fatalf("disabled: %v", err)
	}
	if f.count("") != 0 {
		t.Fatal("row kept")
	}
}

func TestStore_RevokeUser(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	a, b := f.user("a", 0), f.user("b", 0)
	t1, _, _ := f.st.Create(ctx, a, false, "", "")
	t2, _, _ := f.st.Create(ctx, a, false, "", "")
	t3, _, _ := f.st.Create(ctx, b, false, "", "")
	if err := f.st.RevokeUser(ctx, a); err != nil {
		t.Fatal(err)
	}
	for _, tok := range []string{t1, t2} {
		if _, err := f.st.Lookup(ctx, tok); err != ErrNoSession {
			t.Fatalf("revoked session alive: %v", err)
		}
	}
	if _, err := f.st.Lookup(ctx, t3); err != nil {
		t.Fatalf("other user's session: %v", err)
	}
	if err := f.st.Revoke(ctx, t3); err != nil {
		t.Fatal(err)
	}
	if _, err := f.st.Lookup(ctx, t3); err != ErrNoSession {
		t.Fatal("Revoke left the session")
	}
}

func TestStore_Malformed(t *testing.T) {
	f := newFixture(t)
	for _, tok := range []string{"", strings.Repeat("A", 42), strings.Repeat("A", 44), "!" + strings.Repeat("A", 42), strings.Repeat("A", 1024)} {
		if _, err := f.st.Lookup(context.Background(), tok); err != ErrNoSession {
			t.Errorf("Lookup(%d bytes) = %v", len(tok), err)
		}
	}
	// A well-formed token that names no session.
	if _, err := f.st.Lookup(context.Background(), strings.Repeat("A", 43)); err != ErrNoSession {
		t.Errorf("unknown token: %v", err)
	}
}

func TestStore_DeleteExpired(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	_, _, _ = f.st.Create(ctx, f.user("a", 0), false, "", "")
	f.now = f.now.Add(13 * time.Hour)
	_, _, _ = f.st.Create(ctx, f.user("b", 0), false, "", "")
	n, err := f.st.DeleteExpired(ctx)
	if err != nil || n != 1 || f.count("") != 1 {
		t.Fatalf("DeleteExpired = %d, %v; %d rows left", n, err, f.count(""))
	}
}

func TestTokenFrom_DuplicateCookie(t *testing.T) {
	req := func(vals ...string) *http.Request {
		r := httptest.NewRequest("GET", "/", nil)
		for _, v := range vals {
			r.AddCookie(&http.Cookie{Name: CookieName, Value: v}) //nolint:gosec // a request cookie, not a response one
		}
		return r
	}
	if tok, ok := TokenFrom(req("abc")); !ok || tok != "abc" {
		t.Fatalf("single cookie: %q %v", tok, ok)
	}
	if _, ok := TokenFrom(req("abc", "def")); ok {
		t.Fatal("two cookies accepted")
	}
	if _, ok := TokenFrom(req()); ok {
		t.Fatal("no cookie accepted")
	}
}

func TestCookie_Attributes(t *testing.T) {
	w := httptest.NewRecorder()
	SetCookie(w, "tok", time.Unix(2_000_000_000, 0))
	c := w.Result().Cookies()[0]
	if c.Name != "__Host-hogwatch" || c.Path != "/" || c.Domain != "" || !c.Secure || !c.HttpOnly || c.SameSite != http.SameSiteLaxMode {
		t.Fatalf("cookie %+v", c)
	}
	w = httptest.NewRecorder()
	ClearCookie(w)
	if c := w.Result().Cookies()[0]; c.MaxAge >= 0 || c.Value != "" {
		t.Fatalf("clear cookie %+v", c)
	}
}

func (f *fixture) setAdminOIDC(id int64, v int) {
	f.t.Helper()
	if _, err := f.db.Exec(`UPDATE users SET admin_oidc = ? WHERE id = ?`, v, id); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fixture) setNotice(id int64, v string) {
	f.t.Helper()
	if _, err := f.db.Exec(`UPDATE users SET notice = ? WHERE id = ?`, v, id); err != nil {
		f.t.Fatal(err)
	}
}

func TestStore_OIDCAdminCap(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	uid := f.user("o", 0)
	f.setAdminOIDC(uid, 1)
	tok, exp, err := f.st.Create(ctx, uid, true, "", "")
	if err != nil || !exp.Equal(f.now.Add(12*time.Hour)) {
		t.Fatalf("Create: %v, expiry %v", err, exp)
	}
	u, err := f.st.Lookup(ctx, tok)
	if err != nil || !u.IsAdmin() || !u.OIDCAdminSession || !u.AdminOIDC {
		t.Fatalf("Lookup = %+v, %v", u, err)
	}
	// Active every hour, still gone at 12h: activity never extends the cap.
	for h := 1; h < 12; h++ {
		f.now = f.now.Add(time.Hour)
		if u, err := f.st.Lookup(ctx, tok); err != nil || !u.IsAdmin() {
			t.Fatalf("hour %d: %+v, %v", h, u, err)
		}
	}
	f.now = f.now.Add(time.Hour)
	if _, err := f.st.Lookup(ctx, tok); err != ErrNoSession {
		t.Fatalf("at 12h: %v", err)
	}
}

// A password session of a user whose admin right comes from OIDC is a plain
// session: admin_oidc alone grants nothing.
func TestStore_AdminOIDCNeedsAnOIDCSession(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	uid := f.user("o", 0)
	f.setAdminOIDC(uid, 1)
	tok, _, _ := f.st.Create(ctx, uid, false, "", "")
	u, err := f.st.Lookup(ctx, tok)
	if err != nil || u.IsAdmin() || !u.AdminOIDC || u.OIDCAdminSession {
		t.Fatalf("password session: %+v, %v", u, err)
	}
}

// A session flagged oidc_admin outlives a cleared admin_oidc (unlink, a
// manual edit): the flag alone must not keep the rights.
func TestStore_StaleOIDCAdminFlag(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	uid := f.user("o", 0)
	f.setAdminOIDC(uid, 1)
	tok, _, _ := f.st.Create(ctx, uid, true, "", "")
	f.setAdminOIDC(uid, 0)
	u, err := f.st.Lookup(ctx, tok)
	if err != nil || u.IsAdmin() {
		t.Fatalf("stale flag kept admin: %+v, %v", u, err)
	}
}

func TestStore_Notice(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	uid := f.user("n", 0)
	tok, _, _ := f.st.Create(ctx, uid, false, "", "")
	if u, _ := f.st.Lookup(ctx, tok); u.Notice != nil {
		t.Fatalf("notice = %v, want nil", *u.Notice)
	}
	f.setNotice(uid, "oidc_linked")
	u, err := f.st.Lookup(ctx, tok)
	if err != nil || u.Notice == nil || *u.Notice != "oidc_linked" {
		t.Fatalf("notice = %v, %v", u.Notice, err)
	}
}
