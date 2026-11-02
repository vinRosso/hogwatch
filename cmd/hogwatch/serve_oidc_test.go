package main

import (
	"context"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vinRosso/hogwatch/internal/auth/oidc/oidctest"
)

type served struct {
	base string
	log  *syncBuf
}

// serveOIDC starts serve against the mock issuer on a free local port and
// stops it with the test. The client secret reaches serve only as a file.
func serveOIDC(t *testing.T, iss *oidctest.Issuer) served {
	t.Helper()
	cleanEnv(t)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	base := "http://" + addr
	secret := filepath.Join(t.TempDir(), "client-secret")
	if err := os.WriteFile(secret, []byte(iss.ClientSecret+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOGWATCH_DATA_DIR", t.TempDir())
	t.Setenv("HOGWATCH_LISTEN", addr)
	t.Setenv("HOGWATCH_BASE_URL", base)
	t.Setenv("HOGWATCH_AUTH_OIDC_ISSUER", iss.URL)
	t.Setenv("HOGWATCH_AUTH_OIDC_CLIENT_ID", iss.ClientID)
	t.Setenv("HOGWATCH_AUTH_OIDC_CLIENT_SECRET_FILE", secret)
	iss.Register(base + "/api/v1/auth/oidc/callback")

	out := &syncBuf{}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan int, 1)
	go func() { done <- run(ctx, []string{"serve"}, out, out) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Error("serve did not stop")
		}
	})
	waitFor(t, func() bool { return strings.Contains(out.String(), "listening") }, "serve to listen")
	return served{base: base, log: out}
}

func waitFor(t *testing.T, ok func() bool, what string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// get is a browser without a cookie jar: a jar drops the Secure cookies
// serve sets over plain http, so the tests carry them by hand.
func get(t *testing.T, target string, cookies ...*http.Cookie) *reply {
	t.Helper()
	c := &http.Client{
		Timeout:       10 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, target, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, ck := range cookies {
		req.AddCookie(&http.Cookie{Name: ck.Name, Value: ck.Value}) //nolint:gosec // a request cookie has no Secure/HttpOnly attributes
	}
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	return &reply{StatusCode: resp.StatusCode, Header: resp.Header, cookies: resp.Cookies()}
}

// reply is what the tests read of a response; the body is already closed.
type reply struct {
	StatusCode int
	Header     http.Header
	cookies    []*http.Cookie
}

func cookieOf(resp *reply, name string) *http.Cookie {
	for _, c := range resp.cookies {
		if c.Name == name {
			return c
		}
	}
	return nil
}

func TestServe_OIDCLogin(t *testing.T) {
	iss := oidctest.New(t)
	s := serveOIDC(t, iss)
	iss.Next(map[string]any{
		"sub": "serve-sub", "email": "serve@example.com", "email_verified": true,
		"preferred_username": "serveuser",
	})

	start := get(t, s.base+"/api/v1/auth/oidc/start")
	if start.StatusCode != http.StatusFound {
		t.Fatalf("start = %d, want 302", start.StatusCode)
	}
	authURL := start.Header.Get("Location")
	flow := cookieOf(start, "__Host-hogwatch-oidc")
	if flow == nil {
		t.Fatal("no flow cookie")
	}
	au, err := url.Parse(authURL)
	if err != nil {
		t.Fatal(err)
	}
	state := au.Query().Get("state")

	back := iss.Authorize(t, authURL)
	bu, err := url.Parse(back)
	if err != nil {
		t.Fatal(err)
	}
	code := bu.Query().Get("code")
	if code == "" || state == "" {
		t.Fatalf("missing code or state: %q", back)
	}

	cb := get(t, back, flow)
	if cb.StatusCode != http.StatusFound || cb.Header.Get("Location") != "/" {
		t.Fatalf("callback = %d -> %q, want 302 -> /", cb.StatusCode, cb.Header.Get("Location"))
	}
	sess := cookieOf(cb, "__Host-hogwatch")
	if sess == nil {
		t.Fatal("no session cookie after callback")
	}
	if me := get(t, s.base+"/api/v1/me", sess); me.StatusCode != http.StatusOK {
		t.Fatalf("GET /me = %d, want 200", me.StatusCode)
	}

	logs := s.log.String()
	if !strings.Contains(logs, "oidc enabled") {
		t.Errorf("log lacks the oidc enabled line:\n%s", logs)
	}
	for name, secret := range map[string]string{
		"client secret": iss.ClientSecret, "code": code, "state": state, "session": sess.Value,
	} {
		if strings.Contains(logs, secret) {
			t.Errorf("log leaks the %s", name)
		}
	}
}

func TestServe_OIDCIssuerDownStarts(t *testing.T) {
	iss := oidctest.New(t)
	iss.Down(true)
	s := serveOIDC(t, iss)

	if got := get(t, s.base+"/api/v1/auth/oidc/start").StatusCode; got != http.StatusServiceUnavailable {
		t.Fatalf("start with the issuer down = %d, want 503", got)
	}
	iss.Down(false)
	if got := get(t, s.base+"/api/v1/auth/oidc/start").StatusCode; got != http.StatusFound {
		t.Fatalf("start after the issuer is back = %d, want 302", got)
	}
}
