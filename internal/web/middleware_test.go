package web

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/vinRosso/hogwatch/internal/auth/session"
)

func mustPrefixes(t *testing.T, cidrs ...string) []netip.Prefix {
	t.Helper()
	p, err := parsePrefixes(cidrs)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// targets are POST routes of three kinds: public (login), user-level stub,
// admin-level stub. CSRF and content checks run before all of them.
var csrfTargets = []struct{ path, role string }{
	{"/api/v1/auth/login", "anon"},
	{"/api/v1/setup", "anon"},
	{"/api/v1/admin/users", "admin"},
}

func TestCSRF_Rejections(t *testing.T) {
	e := newEnv(t)
	cases := []struct {
		name   string
		header map[string]string
		bare   bool
	}{
		{"no csrf header", map[string]string{"Content-Type": "application/json"}, true},
		{"wrong csrf value", map[string]string{csrfHeader: "0", "Content-Type": "application/json"}, true},
		{"text/plain", map[string]string{csrfHeader: "1", "Content-Type": "text/plain"}, true},
		{"form encoded", map[string]string{csrfHeader: "1", "Content-Type": "application/x-www-form-urlencoded"}, true},
		{"no content type", map[string]string{csrfHeader: "1"}, true},
		{"cross-site fetch metadata", map[string]string{csrfHeader: "1", "Content-Type": "application/json", "Sec-Fetch-Site": "cross-site"}, true},
		{"foreign origin", map[string]string{csrfHeader: "1", "Content-Type": "application/json", "Origin": "https://evil.example"}, true},
	}
	for _, tgt := range csrfTargets {
		for _, c := range cases {
			w := e.do(req{method: "POST", path: tgt.path, role: tgt.role, body: `{"username":"x","password":"y"}`, header: c.header, bare: c.bare})
			if w.Code != http.StatusForbidden {
				t.Errorf("%s %s: status %d", tgt.path, c.name, w.Code)
			}
		}
	}
	var n int
	_ = e.db.QueryRow(`SELECT count(*) FROM users WHERE username = 'x'`).Scan(&n)
	if n != 0 {
		t.Fatal("a rejected request wrote a row")
	}
}

func TestCSRF_AllowedPaths(t *testing.T) {
	e := newEnv(t)
	// A GET needs nothing; a POST with the header and JSON type reaches the handler.
	if w := e.do(req{method: "GET", path: "/api/v1/setup/status", bare: true}); w.Code == http.StatusForbidden {
		t.Fatalf("GET blocked: %d", w.Code)
	}
	if w := e.do(req{method: "POST", path: "/api/v1/admin/users", role: "admin", body: `{}`, header: map[string]string{"Content-Type": "application/json; charset=utf-8"}}); w.Code == http.StatusForbidden || w.Code == http.StatusUnsupportedMediaType {
		t.Fatalf("json with charset: %d", w.Code)
	}
	// A body-less POST needs only the header.
	if w := e.do(req{method: "POST", path: "/api/v1/auth/logout", role: "plain"}); w.Code == http.StatusForbidden {
		t.Fatalf("body-less POST blocked: %d", w.Code)
	}
}

func TestBodyLimit(t *testing.T) {
	e := newEnv(t)
	big := `{"username":"` + strings.Repeat("a", 1<<20) + `"}`
	w := e.do(req{method: "POST", path: "/api/v1/auth/login", body: big})
	if w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("1 MiB: %d", w.Code)
	}
	// Unknown length (chunked) is cut by the reader.
	hr := httptest.NewRequest("POST", "/api/v1/auth/login", strings.NewReader(big))
	hr.ContentLength = -1
	hr.Header.Set(csrfHeader, "1")
	hr.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, hr)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("chunked: %d", rec.Code)
	}
}

func TestProblem_Shape(t *testing.T) {
	e := newEnv(t)
	w := e.do(req{method: "GET", path: "/api/v1/me"})
	var p map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &p); err != nil {
		t.Fatal(err)
	}
	if w.Code != 401 || w.Header().Get("Content-Type") != "application/problem+json" ||
		p["type"] != "about:blank" || p["title"] != "Unauthorized" || p["status"] != float64(401) {
		t.Fatalf("%d %v", w.Code, p)
	}
	// A panic becomes a bare 500: no detail, no panic text.
	a := &server{log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	rec := httptest.NewRecorder()
	a.recoverer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic("secret /host/path") })).
		ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	if rec.Code != 500 || strings.Contains(rec.Body.String(), "secret") || strings.Contains(rec.Body.String(), "detail") {
		t.Fatalf("panic response %d %q", rec.Code, rec.Body.String())
	}
}

func TestHeaders(t *testing.T) {
	e := newEnv(t)
	w := e.do(req{method: "GET", path: "/api/v1/me"})
	h := w.Header()
	if h.Get("X-Content-Type-Options") != "nosniff" || h.Get("Referrer-Policy") != "same-origin" ||
		!strings.Contains(h.Get("Content-Security-Policy"), "frame-ancestors 'none'") || h.Get("Cache-Control") != "no-store" {
		t.Fatalf("headers %v", h)
	}
}

func TestClientIP(t *testing.T) {
	trusted := mustPrefixes(t, "10.0.0.0/8", "fd00::/8")
	mk := func(remote string, xff ...string) *http.Request {
		r := httptest.NewRequest("GET", "/", nil)
		r.RemoteAddr = remote
		for _, v := range xff {
			r.Header.Add("X-Forwarded-For", v)
		}
		return r
	}
	for _, c := range []struct {
		name string
		r    *http.Request
		want string
	}{
		{"direct", mk("203.0.113.9:1"), "203.0.113.9"},
		{"xff ignored from untrusted peer", mk("203.0.113.9:1", "1.2.3.4"), "203.0.113.9"},
		{"rightmost untrusted hop", mk("10.0.0.2:1", "6.6.6.6, 198.51.100.7, 10.0.0.9"), "198.51.100.7"},
		{"spoofed leftmost ignored", mk("10.0.0.2:1", "9.9.9.9", "198.51.100.7"), "198.51.100.7"},
		{"all trusted", mk("10.0.0.2:1", "10.0.0.5"), "10.0.0.2"},
		{"garbled", mk("10.0.0.2:1", "198.51.100.7, nonsense"), "10.0.0.2"},
		{"v4 in v6", mk("[::ffff:203.0.113.9]:1"), "203.0.113.9"},
	} {
		if got := clientIP(c.r, trusted); got.String() != c.want {
			t.Errorf("%s: %v, want %s", c.name, got, c.want)
		}
	}
}

func TestSession_Middleware(t *testing.T) {
	e := newEnv(t)
	cleared := func(w *httptest.ResponseRecorder) bool {
		for _, c := range w.Result().Cookies() {
			if c.Name == session.CookieName && c.MaxAge < 0 {
				return true
			}
		}
		return false
	}
	get := func(path, cookieHeader string) *httptest.ResponseRecorder {
		hr := httptest.NewRequest("GET", path, nil)
		if cookieHeader != "" {
			hr.Header.Set("Cookie", cookieHeader)
		}
		w := httptest.NewRecorder()
		e.h.ServeHTTP(w, hr)
		return w
	}
	valid := e.cookies["plain"]
	name := session.CookieName + "="
	cases := map[string]string{
		"malformed":   name + "not-a-token",
		"empty":       name,
		"wrong len":   name + valid[:40],
		"unknown":     name + strings.Repeat("A", 43),
		"duplicate":   name + valid + "; " + name + valid,
		"disabled":    name + e.cookies["disabled"],
		"huge cookie": name + strings.Repeat("A", 4000),
	}
	for label, ck := range cases {
		w := get("/api/v1/me", ck)
		if w.Code != 401 || !cleared(w) {
			t.Errorf("%s: status %d, cleared %v", label, w.Code, cleared(w))
		}
		// A public route goes on anonymously.
		w = get("/api/v1/setup/status", ck)
		if w.Code == 401 || !cleared(w) {
			t.Errorf("%s on a public route: status %d, cleared %v", label, w.Code, cleared(w))
		}
	}
	if w := get("/api/v1/me", name+valid); w.Code != http.StatusOK || cleared(w) {
		t.Fatalf("valid session: %d cleared %v", w.Code, cleared(w))
	}
	// Revoked.
	if err := e.sessions.Revoke(t.Context(), valid); err != nil {
		t.Fatal(err)
	}
	if w := get("/api/v1/me", name+valid); w.Code != 401 || !cleared(w) {
		t.Fatalf("revoked: %d", w.Code)
	}
	// Idle expiry.
	e.now = e.now.Add(13 * time.Hour)
	if w := get("/api/v1/me", name+e.cookies["owner"]); w.Code != 401 || !cleared(w) {
		t.Fatalf("idle: %d", w.Code)
	}
}

func TestSPA_StillServed(t *testing.T) {
	spa := fstest.MapFS{"index.html": {Data: []byte("<title>app</title>")}}
	e := newEnv(t, func(d *Deps) { d.SPA = spa })
	for _, p := range []string{"/", "/some/client/route"} {
		w := e.do(req{method: "GET", path: p})
		if w.Code != 200 || !strings.Contains(w.Body.String(), "<title>app</title>") {
			t.Errorf("%s: %d", p, w.Code)
		}
	}
	if w := e.do(req{method: "GET", path: "/api/nothing"}); w.Code != 404 {
		t.Errorf("/api/nothing: %d", w.Code)
	}
}
