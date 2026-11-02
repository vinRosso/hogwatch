package main

import (
	"bytes"
	"context"
	"database/sql"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vinRosso/hogwatch/internal/auth/local"
	"github.com/vinRosso/hogwatch/internal/db"
)

func userEnv(t *testing.T) string {
	t.Helper()
	cleanEnv(t)
	dir := t.TempDir()
	t.Setenv("HOGWATCH_DATA_DIR", dir)
	return dir
}

func withStdin(t *testing.T, s string) {
	t.Helper()
	old := stdin
	stdin = strings.NewReader(s)
	t.Cleanup(func() { stdin = old })
}

func queryRow(t *testing.T, dir, q string, dest ...any) {
	t.Helper()
	d, err := db.Open(context.Background(), filepath.Join(dir, "hogwatch.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = d.Close() }()
	if err := d.QueryRow(q).Scan(dest...); err != nil {
		t.Fatal(err)
	}
}

func TestUserCreate_Admin(t *testing.T) {
	dir := userEnv(t)
	withStdin(t, "a long password\n")
	code, out, errs := runCmd("user", "create", "--username", "Boss", "--admin", "--password-stdin", "--email", "b@example.com")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errs)
	}
	if strings.Contains(out, "a long password") {
		t.Fatalf("stdout echoes the supplied password: %q", out)
	}
	var admin int
	var hash sql.NullString
	queryRow(t, dir, `SELECT admin_local, password_hash FROM users WHERE username = 'boss'`, &admin, &hash)
	if ok, err := local.Verify(hash.String, "a long password"); admin != 1 || !ok || err != nil {
		t.Fatalf("admin=%d verify=%v %v", admin, ok, err)
	}
	var n int
	var actor sql.NullInt64
	queryRow(t, dir, `SELECT count(*), max(actor_id) FROM audit_log WHERE action = 'user.created_cli' AND target = 'boss'`, &n, &actor)
	if n != 1 || actor.Valid {
		t.Fatalf("audit rows %d actor %v", n, actor)
	}
}

func TestUserCreate_Generated(t *testing.T) {
	dir := userEnv(t)
	code, out, errs := runCmd("user", "create", "--username", "gen")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errs)
	}
	m := regexp.MustCompile(`password: (\S{20})\n`).FindStringSubmatch(out)
	if m == nil {
		t.Fatalf("no password in %q", out)
	}
	var hash string
	queryRow(t, dir, `SELECT password_hash FROM users WHERE username = 'gen'`, &hash)
	if ok, _ := local.Verify(hash, m[1]); !ok {
		t.Fatal("printed password does not verify")
	}
}

func TestUserCreate_Duplicate(t *testing.T) {
	userEnv(t)
	if code, _, errs := runCmd("user", "create", "--username", "dup"); code != 0 {
		t.Fatal(errs)
	}
	code, _, errs := runCmd("user", "create", "--username", "DUP")
	if code != 1 || !strings.Contains(errs, "already exists") {
		t.Fatalf("exit %d: %s", code, errs)
	}
}

func TestUserCreate_Short(t *testing.T) {
	dir := userEnv(t)
	withStdin(t, "short\n")
	code, _, errs := runCmd("user", "create", "--username", "u", "--password-stdin")
	if code != 1 || !strings.Contains(errs, "8 to 1024") {
		t.Fatalf("exit %d: %s", code, errs)
	}
	if _, err := os.Stat(filepath.Join(dir, "hogwatch.db")); err == nil {
		t.Fatal("a rejected password still opened the database")
	}
}

func TestUserCreate_Usage(t *testing.T) {
	userEnv(t)
	for _, args := range [][]string{{}, {"frob"}, {"create"}, {"create", "--username", "bad name!"}} {
		if code, _, _ := runCmd(append([]string{"user"}, args...)...); code != 2 {
			t.Errorf("%v: exit %d, want 2", args, code)
		}
	}
}

type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func TestServe_LogsSetupURL(t *testing.T) {
	userEnv(t)
	t.Setenv("HOGWATCH_LISTEN", "127.0.0.1:0")
	t.Setenv("HOGWATCH_BASE_URL", "http://hogwatch.test")
	ctx, cancel := context.WithCancel(context.Background())
	var out syncBuf
	done := make(chan int, 1)
	go func() { done <- run(ctx, []string{"serve"}, &out, io.Discard) }()
	defer func() {
		cancel()
		select {
		case <-done:
		case <-time.After(15 * time.Second):
			t.Error("serve did not stop")
		}
	}()

	addrRE := regexp.MustCompile(`127\.0\.0\.1:([1-9][0-9]*)`)
	setupRE := regexp.MustCompile(`url=http://hogwatch\.test/setup(\s|$)`)
	var port string
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		s := out.String()
		if m := addrRE.FindStringSubmatch(s); m != nil && setupRE.MatchString(s) {
			port = m[1]
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if port == "" {
		t.Fatalf("no setup line in log: %s", out.String())
	}
	if n := len(setupRE.FindAllString(out.String(), -1)); n != 1 {
		t.Fatalf("%d setup lines", n)
	}

	base := "http://127.0.0.1:" + port
	post := func(path, body string) (int, []*http.Cookie) {
		r, _ := http.NewRequest("POST", base+path, strings.NewReader(body))
		r.Header.Set("X-Hogwatch-CSRF", "1")
		r.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		return resp.StatusCode, resp.Cookies()
	}
	status, cookies := post("/api/v1/setup", `{"username":"boss","password":"long enough pw"}`)
	if status != 200 {
		t.Fatalf("setup: %d", status)
	}
	var cookie *http.Cookie
	for _, c := range cookies {
		cookie = c
	}
	if cookie == nil {
		t.Fatal("no cookie")
	}
	// The __Host- cookie is Secure; a plain-HTTP client still sends it when set by hand.
	r, _ := http.NewRequest("GET", base+"/api/v1/me", nil)
	r.AddCookie(&http.Cookie{Name: cookie.Name, Value: cookie.Value}) //nolint:gosec // a request cookie, not a response one
	me, err := http.DefaultClient.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	_ = me.Body.Close()
	if me.StatusCode != 200 {
		t.Fatalf("/me: %d", me.StatusCode)
	}
}
