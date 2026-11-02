package web

import (
	"testing"
	"time"
)

func TestHealthz(t *testing.T) {
	e := newEnv(t)
	w := e.do(req{method: "GET", path: "/healthz"})
	if w.Code != 200 || w.Body.String() != "ok" || w.Header().Get("Content-Type") != "text/plain" {
		t.Fatalf("%d %q", w.Code, w.Body.String())
	}
	_ = e.db.Close()
	w = e.do(req{method: "GET", path: "/healthz"})
	if w.Code != 503 || w.Body.String() != "unavailable" {
		t.Fatalf("db closed: %d %q", w.Code, w.Body.String())
	}
}

func TestReadyz(t *testing.T) {
	e := newEnv(t)
	status := func() int { return e.do(req{method: "GET", path: "/readyz"}).Code }
	if c := status(); c != 200 {
		t.Fatalf("fresh heartbeat: %d", c)
	}
	e.now = e.now.Add(59 * time.Second)
	if c := status(); c != 200 {
		t.Fatalf("59s old: %d", c)
	}
	e.now = e.now.Add(time.Second)
	if c := status(); c != 503 {
		t.Fatalf("60s old: %d", c)
	}
	e.now = e.now.Add(-time.Minute)
	e.exec(`DELETE FROM scanner_status`)
	if c := status(); c != 503 {
		t.Fatalf("no heartbeat row: %d", c)
	}
	e.exec(`INSERT INTO scanner_status (id, heartbeat_at, version) VALUES (1, ?, 'x')`, e.now.Unix())
	if c := status(); c != 200 {
		t.Fatalf("restored: %d", c)
	}
	e.exec(`DELETE FROM goose_db_version WHERE version_id = (SELECT max(version_id) FROM goose_db_version)`)
	if c := status(); c != 503 {
		t.Fatalf("pending migration: %d", c)
	}
}
