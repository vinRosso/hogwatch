package web

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/vinRosso/hogwatch/internal/auth/local"
	"github.com/vinRosso/hogwatch/internal/auth/session"
	"github.com/vinRosso/hogwatch/internal/config"
)

// addUser inserts a user with a local password and returns its id.
func (e *env) addUser(name, password string, admin, disabled bool) int64 {
	e.t.Helper()
	var hash any
	if password != "" {
		h, err := local.Hash(password)
		if err != nil {
			e.t.Fatal(err)
		}
		hash = h
	}
	res, err := e.db.Exec(`INSERT INTO users (username, created_at, password_hash, admin_local, disabled) VALUES (?, 1, ?, ?, ?)`,
		name, hash, admin, disabled)
	if err != nil {
		e.t.Fatal(err)
	}
	id, _ := res.LastInsertId()
	return id
}

func login(e *env, user, pw string) *httptest.ResponseRecorder {
	b, _ := json.Marshal(map[string]string{"username": user, "password": pw})
	return e.do(req{method: "POST", path: "/api/v1/auth/login", body: string(b)})
}

func cookieOf(w *httptest.ResponseRecorder) *http.Cookie {
	for _, c := range w.Result().Cookies() {
		if c.Name == session.CookieName {
			return c
		}
	}
	return nil
}

func TestLogin_Success(t *testing.T) {
	e := newEnv(t)
	id := e.addUser("carol", "correct horse", false, false)
	w := login(e, " Carol ", "correct horse")
	if w.Code != 200 {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	c := cookieOf(w)
	if c == nil || c.MaxAge < 0 || !c.HttpOnly || !c.Secure {
		t.Fatalf("cookie %+v", c)
	}
	var me map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &me)
	if me["username"] != "carol" || me["is_admin"] != false {
		t.Fatalf("me %v", me)
	}
	var last int64
	_ = e.db.QueryRow(`SELECT last_login_at FROM users WHERE id = ?`, id).Scan(&last)
	if last != e.now.Unix() {
		t.Fatalf("last_login_at %d", last)
	}
	var n int
	_ = e.db.QueryRow(`SELECT count(*) FROM audit_log WHERE action = 'auth.login' AND actor_id = ?`, id).Scan(&n)
	if n != 1 {
		t.Fatalf("audit rows %d", n)
	}
	// The cookie works.
	hr := httptest.NewRequest("GET", "/api/v1/me", nil)
	hr.AddCookie(c)
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, hr)
	if rec.Code != 200 {
		t.Fatalf("/me with the new cookie: %d", rec.Code)
	}
}

func TestLogin_IdenticalFailures(t *testing.T) {
	e := newEnv(t, func(d *Deps) { d.Cfg.Auth.Local.Enabled = true })
	e.addUser("real", "right password", false, false)
	e.addUser("off", "right password", false, true)
	e.addUser("oidconly", "", false, false)
	before := local.DummyVerifies.Load()
	var first string
	for i, c := range [][2]string{
		{"nobody", "whatever1"}, {"real", "wrong password"}, {"off", "right password"}, {"oidconly", "whatever1"},
	} {
		w := login(e, c[0], c[1])
		if w.Code != 401 || cookieOf(w) != nil {
			t.Fatalf("case %d: %d", i, w.Code)
		}
		if i == 0 {
			first = w.Body.String()
		} else if w.Body.String() != first {
			t.Fatalf("case %d body %q differs from %q", i, w.Body, first)
		}
	}
	// The unknown, disabled and OIDC-only users each cost a dummy hash.
	if n := local.DummyVerifies.Load() - before; n != 3 {
		t.Fatalf("dummy verifies %d, want 3", n)
	}
}

func TestLogin_RateLimited(t *testing.T) {
	e := newEnv(t)
	e.addUser("alice", "right password", false, false)
	for i := range 5 {
		if w := login(e, "ghost", "x"); w.Code != 401 {
			t.Fatalf("attempt %d: %d", i, w.Code)
		}
	}
	w := login(e, "ghost", "x")
	if w.Code != 429 || w.Header().Get("Retry-After") != "1" {
		t.Fatalf("6th: %d retry-after %q", w.Code, w.Header().Get("Retry-After"))
	}
	// The same address is locked for every username.
	if w := login(e, "alice", "right password"); w.Code != 429 {
		t.Fatalf("same IP, other user: %d", w.Code)
	}
	// Failures as Alice lock alice from another address too.
	e2 := newEnv(t)
	e2.addUser("alice", "right password", false, false)
	for i := range 5 {
		hr := httptest.NewRequest("POST", "/api/v1/auth/login", strings.NewReader(`{"username":"Alice","password":"bad"}`))
		hr.RemoteAddr = fmt.Sprintf("198.51.100.%d:1", i+1)
		hr.Header.Set(csrfHeader, "1")
		hr.Header.Set("Content-Type", "application/json")
		e2.h.ServeHTTP(httptest.NewRecorder(), hr)
	}
	hr := httptest.NewRequest("POST", "/api/v1/auth/login", strings.NewReader(`{"username":"alice","password":"right password"}`))
	hr.RemoteAddr = "203.0.113.50:1"
	hr.Header.Set(csrfHeader, "1")
	hr.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	e2.h.ServeHTTP(rec, hr)
	if rec.Code != 429 {
		t.Fatalf("alice from a fresh address: %d", rec.Code)
	}
}

func TestLogin_BreakGlass(t *testing.T) {
	e := newEnv(t, func(d *Deps) {
		d.Cfg.Auth.Local = config.AuthLocal{Enabled: false, MinPasswordLen: 8, BreakGlassAdmins: []string{"root"}}
	})
	e.addUser("root", "right password", true, false)
	e.addUser("other", "right password", true, false)
	e.addUser("plainjoe", "right password", false, false)
	if w := login(e, "root", "right password"); w.Code != 200 {
		t.Fatalf("break-glass admin: %d", w.Code)
	}
	a, b := login(e, "other", "right password"), login(e, "plainjoe", "right password")
	if a.Code != 401 || b.Code != 401 || a.Body.String() != b.Body.String() {
		t.Fatalf("unlisted: %d %d", a.Code, b.Code)
	}
}

func TestLogout(t *testing.T) {
	e := newEnv(t)
	w := e.do(req{method: "POST", path: "/api/v1/auth/logout", role: "plain"})
	if w.Code != 204 {
		t.Fatalf("status %d", w.Code)
	}
	if c := cookieOf(w); c == nil || c.MaxAge >= 0 {
		t.Fatalf("cookie not cleared: %+v", c)
	}
	if w := e.do(req{method: "GET", path: "/api/v1/me", role: "plain"}); w.Code != 401 {
		t.Fatalf("session survived logout: %d", w.Code)
	}
}

func setupBody(user, pw string) string {
	b, _ := json.Marshal(map[string]string{"username": user, "password": pw})
	return string(b)
}

// clearAdmins leaves the database with no enabled admin, as on first start.
func (e *env) clearAdmins() {
	e.t.Helper()
	e.exec(`DELETE FROM users WHERE admin_local = 1 OR admin_oidc = 1`)
}

func TestSetup_Flow(t *testing.T) {
	e := newEnv(t)
	e.clearAdmins()
	status := func() bool {
		w := e.do(req{method: "GET", path: "/api/v1/setup/status"})
		var s struct{ Needed bool }
		_ = json.Unmarshal(w.Body.Bytes(), &s)
		return s.Needed
	}
	if !status() {
		t.Fatal("setup not needed with no admin")
	}
	w := e.do(req{method: "POST", path: "/api/v1/setup", body: setupBody("Boss", "long enough pw")})
	if w.Code != 200 || cookieOf(w) == nil {
		t.Fatalf("setup: %d %s", w.Code, w.Body)
	}
	var admin, audited int
	_ = e.db.QueryRow(`SELECT count(*) FROM users WHERE username = 'boss' AND admin_local = 1`).Scan(&admin)
	_ = e.db.QueryRow(`SELECT count(*) FROM audit_log WHERE action = 'setup.admin_created'`).Scan(&audited)
	if admin != 1 || audited != 1 {
		t.Fatalf("admin rows %d, audit rows %d", admin, audited)
	}
	if status() {
		t.Fatal("setup still needed")
	}
	if w := e.do(req{method: "POST", path: "/api/v1/setup", body: setupBody("two", "long enough pw")}); w.Code != 404 {
		t.Fatalf("second setup: %d", w.Code)
	}
}

func TestSetup_Race(t *testing.T) {
	e := newEnv(t)
	e.clearAdmins()
	var wg sync.WaitGroup
	codes := make([]int, 8)
	for i := range codes {
		wg.Add(1)
		go func() {
			defer wg.Done()
			hr := httptest.NewRequest("POST", "/api/v1/setup", strings.NewReader(setupBody(fmt.Sprintf("admin%d", i), "long enough pw")))
			hr.RemoteAddr = fmt.Sprintf("198.51.100.%d:1", i+1)
			hr.Header.Set(csrfHeader, "1")
			hr.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			e.h.ServeHTTP(rec, hr)
			codes[i] = rec.Code
		}()
	}
	wg.Wait()
	var ok int
	for _, c := range codes {
		switch c {
		case 200:
			ok++
		case 404:
		default:
			t.Errorf("unexpected status %d", c)
		}
	}
	var n int
	_ = e.db.QueryRow(`SELECT count(*) FROM users WHERE admin_local = 1`).Scan(&n)
	if ok != 1 || n != 1 {
		t.Fatalf("%d successes, %d admin rows", ok, n)
	}
}

func TestSetup_RateLimited(t *testing.T) {
	e := newEnv(t)
	e.clearAdmins()
	for i := range 5 {
		if w := e.do(req{method: "POST", path: "/api/v1/setup", body: setupBody("boss", "short")}); w.Code != 400 {
			t.Fatalf("attempt %d: %d", i, w.Code)
		}
	}
	if w := e.do(req{method: "POST", path: "/api/v1/setup", body: setupBody("boss", "long enough pw")}); w.Code != 429 {
		t.Fatalf("locked key: %d", w.Code)
	}
}

func TestSetup_Validation(t *testing.T) {
	e := newEnv(t)
	e.clearAdmins()
	for _, c := range [][2]string{{"boss", "short"}, {"bad name!", "long enough pw"}, {"", "long enough pw"}} {
		if w := e.do(req{method: "POST", path: "/api/v1/setup", body: setupBody(c[0], c[1])}); w.Code != 400 {
			t.Errorf("%v: %d", c, w.Code)
		}
	}
	var n int
	_ = e.db.QueryRow(`SELECT count(*) FROM users WHERE admin_local = 1`).Scan(&n)
	if n != 0 {
		t.Fatal("a rejected setup created an admin")
	}
}

func TestMe(t *testing.T) {
	e := newEnv(t)
	w := e.do(req{method: "GET", path: "/api/v1/me", role: "lister"})
	var me map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &me)
	if w.Code != 200 || me["username"] != "lister" || me["can_list_files"] != true || me["is_admin"] != false || me["email"] != nil {
		t.Fatalf("%d %v", w.Code, me)
	}
}

func TestPassword_Change(t *testing.T) {
	e := newEnv(t)
	id := e.addUser("dave", "old password", false, false)
	w1 := login(e, "dave", "old password")
	w2 := login(e, "dave", "old password")
	c1, c2 := cookieOf(w1), cookieOf(w2)
	call := func(c *http.Cookie, cur, next string) *httptest.ResponseRecorder {
		b, _ := json.Marshal(map[string]string{"current": cur, "new": next})
		hr := httptest.NewRequest("POST", "/api/v1/me/password", strings.NewReader(string(b)))
		hr.Header.Set(csrfHeader, "1")
		hr.Header.Set("Content-Type", "application/json")
		hr.AddCookie(c)
		rec := httptest.NewRecorder()
		e.h.ServeHTTP(rec, hr)
		return rec
	}
	if w := call(c1, "wrong", "new password!"); w.Code != 422 {
		t.Fatalf("wrong current: %d", w.Code)
	}
	if w := call(c1, "old password", "short"); w.Code != 400 {
		t.Fatalf("short new: %d", w.Code)
	}
	w := call(c1, "old password", "new password!")
	if w.Code != 204 {
		t.Fatalf("change: %d", w.Code)
	}
	fresh := cookieOf(w)
	if fresh == nil {
		t.Fatal("no fresh cookie")
	}
	get := func(c *http.Cookie) int {
		hr := httptest.NewRequest("GET", "/api/v1/me", nil)
		hr.AddCookie(c)
		rec := httptest.NewRecorder()
		e.h.ServeHTTP(rec, hr)
		return rec.Code
	}
	if get(c1) != 401 || get(c2) != 401 {
		t.Fatal("old sessions survived")
	}
	if get(fresh) != 200 {
		t.Fatal("fresh cookie rejected")
	}
	if w := login(e, "dave", "new password!"); w.Code != 200 {
		t.Fatalf("login with the new password: %d", w.Code)
	}
	var n int
	_ = e.db.QueryRow(`SELECT count(*) FROM audit_log WHERE action = 'auth.password_change' AND actor_id = ?`, id).Scan(&n)
	if n != 1 {
		t.Fatalf("audit rows %d", n)
	}
}
