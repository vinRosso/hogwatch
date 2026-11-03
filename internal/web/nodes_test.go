package web

import (
	"encoding/json"
	"net/url"
	"testing"

	"github.com/vinRosso/hogwatch/internal/totals"
)

// Fixture ids: root 1.1, a 1.2 (owner's path), a/b 1.3, d 1.4 (lister's path).

func getJSON(t *testing.T, e *env, role, path string, wantCode int) map[string]any {
	t.Helper()
	w := e.do(req{method: "GET", path: path, role: role})
	if w.Code != wantCode {
		t.Fatalf("GET %s as %s = %d, want %d: %s", path, role, w.Code, wantCode, w.Body)
	}
	var m map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &m); err != nil {
		t.Fatalf("GET %s: body %q: %v", path, w.Body, err)
	}
	return m
}

func itemNames(t *testing.T, m map[string]any) []string {
	t.Helper()
	var out []string
	for _, it := range m["items"].([]any) {
		out = append(out, it.(map[string]any)["name"].(string))
	}
	return out
}

func TestGetNode(t *testing.T) {
	e := newEnv(t)
	n := getJSON(t, e, "owner", "/api/v1/nodes/1.2", 200)
	if n["rel_path"] != "a" || n["parent_ref"] != nil || n["ref"] != "1.2" || n["name"] != "a" {
		t.Errorf("owner node = %v", n)
	}
	n = getJSON(t, e, "admin", "/api/v1/nodes/1.2", 200)
	if n["parent_ref"] != "1.1" {
		t.Errorf("admin node parent_ref = %v, want 1.1", n["parent_ref"])
	}
	n = getJSON(t, e, "owner", "/api/v1/nodes/1.3", 200)
	if n["rel_path"] != "a/b" || n["parent_ref"] != "1.2" {
		t.Errorf("owner a/b = %v", n)
	}
	// Out of scope and nonexistent are the same 404.
	miss := e.do(req{method: "GET", path: "/api/v1/nodes/9.9", role: "plain"})
	for _, c := range []struct{ role, ref string }{{"plain", "1.2"}, {"owner", "1.4"}, {"owner", "1.1"}, {"owner", "9.9"}} {
		w := e.do(req{method: "GET", path: "/api/v1/nodes/" + c.ref, role: c.role})
		if w.Code != 404 || w.Body.String() != miss.Body.String() {
			t.Errorf("%s on %s = %d %q, want the common 404 %q", c.role, c.ref, w.Code, w.Body, miss.Body)
		}
	}
	for _, ref := range []string{"x", "0.1", "1.0", "1.2.3", "01.2"} {
		if w := e.do(req{method: "GET", path: "/api/v1/nodes/" + ref, role: "admin"}); w.Code != 400 {
			t.Errorf("malformed ref %q = %d, want 400", ref, w.Code)
		}
	}
}

func TestResolve_Handler(t *testing.T) {
	e := newEnv(t)
	get := func(role, q string, code int) map[string]any {
		return getJSON(t, e, role, "/api/v1/resolve?"+q, code)
	}
	if m := get("owner", "root=1&path=a/b", 200); m["ref"] != "1.3" {
		t.Errorf("resolve a/b = %v", m)
	}
	if m := get("admin", "root=1&path="+url.QueryEscape("/d/"), 200); m["ref"] != "1.4" {
		t.Errorf("resolve /d/ = %v", m)
	}
	if m := get("admin", "root=1&path=", 200); m["ref"] != "1.1" {
		t.Errorf("resolve root = %v", m)
	}
	for _, p := range []string{"..", "a/../d", `a\q`, "a//b", `a\x4`} {
		get("admin", "root=1&path="+url.QueryEscape(p), 400)
	}
	for _, c := range []struct{ role, q string }{
		{"plain", "root=1&path=a/b"}, {"owner", "root=1&path=d"}, {"owner", "root=1&path="},
		{"owner", "root=2&path=a"}, {"admin", "root=1&path=nope"},
	} {
		get(c.role, c.q, 404)
	}
	// A root without a current scan is a 404 too.
	e.exec(`INSERT INTO scan_roots (id, name, container_path, host_path) VALUES (2, 'x', '/mnt/x', '/srv/x')`)
	get("admin", "root=2&path=", 404)
}

func TestListChildren_Handler(t *testing.T) {
	e := newEnv(t)
	m := getJSON(t, e, "admin", "/api/v1/nodes/1.1/children?sort=name&limit=1", 200)
	if got := itemNames(t, m); len(got) != 1 || got[0] != "a" || m["next_cursor"] == nil {
		t.Fatalf("page 1 = %v next %v", got, m["next_cursor"])
	}
	m2 := getJSON(t, e, "admin", "/api/v1/nodes/1.1/children?sort=name&limit=1&cursor="+url.QueryEscape(m["next_cursor"].(string)), 200)
	if got := itemNames(t, m2); len(got) != 1 || got[0] != "d" || m2["next_cursor"] != nil {
		t.Errorf("page 2 = %v next %v", got, m2["next_cursor"])
	}
	// Under a filter bytes_logical is null; without one it is a number.
	m = getJSON(t, e, "owner", "/api/v1/nodes/1.2/children?filter=cat:image", 200)
	for _, it := range m["items"].([]any) {
		if it.(map[string]any)["bytes_logical"] != nil {
			t.Errorf("filtered item has bytes_logical: %v", it)
		}
	}
	m = getJSON(t, e, "owner", "/api/v1/nodes/1.2/children", 200)
	for _, it := range m["items"].([]any) {
		if it.(map[string]any)["bytes_logical"] == nil {
			t.Errorf("unfiltered item without bytes_logical: %v", it)
		}
	}
	for _, q := range []string{"limit=0", "limit=501", "limit=-1", "sort=x", "filter=cat:x", "filter=cat:video%3Bcat:audio", "filter=cat:video;age:lt1w"} {
		getJSON(t, e, "admin", "/api/v1/nodes/1.1/children?"+q, 400)
	}
	getJSON(t, e, "owner", "/api/v1/nodes/1.2/children?filter=cat:image%3Bage:lt1w%3Bsize:lt1m", 200)
	getJSON(t, e, "plain", "/api/v1/nodes/1.2/children", 404)
	getJSON(t, e, "owner", "/api/v1/nodes/1.1/children", 404)
}

func TestChildren_CursorTampered(t *testing.T) {
	e := newEnv(t)
	good := encodeCursor(childCursor{V: 1, Scan: 1, Dir: 1, Sort: "name", Name: []byte("a")})
	base := "/api/v1/nodes/1.1/children?sort=name&cursor="
	getJSON(t, e, "admin", base+url.QueryEscape(good), 200)
	for name, c := range map[string]string{
		"garbage":    "!!!",
		"truncated":  good[:len(good)-6],
		"empty-json": "e30",
		"version":    encodeCursor(childCursor{V: 2, Scan: 1, Dir: 1, Sort: "name", Name: []byte("a")}),
		"sort":       encodeCursor(childCursor{V: 1, Scan: 1, Dir: 1, Sort: "size", Name: []byte("a")}),
		"filter":     encodeCursor(childCursor{V: 1, Scan: 1, Dir: 1, Sort: "name", Filter: "cat:video", Name: []byte("a")}),
		"node":       encodeCursor(childCursor{V: 1, Scan: 1, Dir: 4, Sort: "name", Name: []byte("a")}),
		"scan":       encodeCursor(childCursor{V: 1, Scan: 2, Dir: 1, Sort: "name", Name: []byte("a")}),
		"long":       encodeCursor(childCursor{V: 1, Scan: 1, Dir: 1, Sort: "name", Name: make([]byte, 600)}),
	} {
		if w := e.do(req{method: "GET", path: base + url.QueryEscape(c), role: "admin"}); w.Code != 400 {
			t.Errorf("%s cursor = %d, want 400", name, w.Code)
		}
	}
	// A cursor for the same node but other sort bounds never reaches outside
	// the node's own children: the owner's page for a stays inside a.
	crafted := encodeCursor(childCursor{V: 1, Scan: 1, Dir: 2, Sort: "name", Name: []byte{}})
	m := getJSON(t, e, "owner", "/api/v1/nodes/1.2/children?sort=name&cursor="+url.QueryEscape(crafted), 200)
	if got := itemNames(t, m); len(got) != 1 || got[0] != "b" {
		t.Errorf("owner page = %v, want exactly [b]", got)
	}
	// Every sort round-trips its cursor.
	for _, sort := range []string{"size", "files", "mtime", "name"} {
		var seen []string
		cur := ""
		for i := 0; i < 5; i++ {
			q := "/api/v1/nodes/1.1/children?limit=1&sort=" + sort
			if cur != "" {
				q += "&cursor=" + url.QueryEscape(cur)
			}
			m := getJSON(t, e, "admin", q, 200)
			seen = append(seen, itemNames(t, m)...)
			if m["next_cursor"] == nil {
				break
			}
			cur = m["next_cursor"].(string)
		}
		if len(seen) != 2 {
			t.Errorf("sort %s paged = %v, want both children of the root once", sort, seen)
		}
	}
}

func TestGetTreemap_Handler(t *testing.T) {
	e := newEnv(t)
	for _, q := range []string{"depth=0", "depth=7", "filter=bogus"} {
		getJSON(t, e, "admin", "/api/v1/nodes/1.1/treemap?"+q, 400)
	}
	m := getJSON(t, e, "admin", "/api/v1/nodes/1.1/treemap?depth=2", 200)
	if m["kind"] != "dir" || m["ref"] != "1.1" || m["children"] == nil {
		t.Errorf("treemap root = %v", m)
	}
	m = getJSON(t, e, "owner", "/api/v1/nodes/1.2/treemap", 200)
	kids := m["children"].([]any)
	if len(kids) == 0 {
		t.Fatalf("owner treemap = %v", m)
	}
	for _, k := range kids {
		if k.(map[string]any)["children"] == nil {
			t.Errorf("a leaf has null children: %v", k)
		}
	}
	getJSON(t, e, "owner", "/api/v1/nodes/1.1/treemap", 404)
}

func TestGetTop_Handler(t *testing.T) {
	e := newEnv(t)
	m := getJSON(t, e, "owner", "/api/v1/nodes/1.2/top?kind=largest", 200)
	items := m["items"].([]any)
	if m["stored"] != true || len(items) != 1 || items[0].(map[string]any)["dir_ref"] != "1.3" || items[0].(map[string]any)["name"] != "big.bin" {
		t.Errorf("top = %v", m)
	}
	m = getJSON(t, e, "owner", "/api/v1/nodes/1.2/top?kind=oldest", 200)
	if m["stored"] != false || len(m["items"].([]any)) != 0 {
		t.Errorf("oldest = %v", m)
	}
	// 1.3 has no list of its own; its file comes from the list of 1.2.
	m = getJSON(t, e, "owner", "/api/v1/nodes/1.3/top?kind=largest", 200)
	items = m["items"].([]any)
	if m["stored"] != true || len(items) != 1 || items[0].(map[string]any)["name"] != "big.bin" {
		t.Errorf("a node without its own list: %v", m)
	}
	m = getJSON(t, e, "admin", "/api/v1/nodes/1.4/top?kind=largest", 200)
	if m["stored"] != false {
		t.Errorf("a node with no file in any list says stored: %v", m)
	}
	// Only the files directly in a node: 1.3 holds big.bin itself, 1.4 has files no list names.
	m = getJSON(t, e, "owner", "/api/v1/nodes/1.3/top?kind=largest&scope=dir", 200)
	items = m["items"].([]any)
	if m["stored"] != true || len(items) != 1 || items[0].(map[string]any)["name"] != "big.bin" {
		t.Errorf("scope=dir at 1.3 = %v", m)
	}
	m = getJSON(t, e, "admin", "/api/v1/nodes/1.4/top?kind=largest&scope=dir", 200)
	if m["stored"] != false {
		t.Errorf("scope=dir at 1.4 should say not stored: %v", m)
	}
	getJSON(t, e, "admin", "/api/v1/nodes/1.4/top?kind=largest&scope=bogus", 400)
	m = getJSON(t, e, "owner", "/api/v1/nodes/1.3/top?kind=largest&scope=both", 200)
	if items = m["items"].([]any); m["stored"] != true || len(items) < 1 || items[0].(map[string]any)["name"] != "big.bin" {
		t.Errorf("scope=both at 1.3 = %v", m)
	}
	if m = getJSON(t, e, "admin", "/api/v1/nodes/1.4/top?kind=largest&scope=both", 200); m["stored"] != false {
		t.Errorf("scope=both at 1.4 should say not stored: %v", m)
	}
	// The admin's own finished, unfiltered listing of 1.4 feeds its top files.
	e.exec(`INSERT INTO jobs (id, kind, root_id, target_node, params, requested_by, status, progress, created_at, expires_at)
		VALUES (7, 'listing', 1, '1.4', '{"mode":"subtree"}', ?, 'done', '{"rows":1,"truncated":false}', 1, ?)`, e.users["admin"], e.now.Unix()+3600)
	e.exec(`INSERT INTO listing_results (job_id, seq, rel_name, type, size, alloc, mtime, uid) VALUES (7, 1, ?, 'file', 5, 8, 9, 0)`, []byte("f.bin"))
	m = getJSON(t, e, "admin", "/api/v1/nodes/1.4/top?kind=largest", 200)
	items = m["items"].([]any)
	if m["stored"] != true || len(items) != 1 || items[0].(map[string]any)["name"] != "f.bin" || items[0].(map[string]any)["dir_ref"] != "1.4" {
		t.Errorf("top from a listing = %v", m)
	}
	getJSON(t, e, "owner", "/api/v1/nodes/1.2/top?kind=x", 400)
	getJSON(t, e, "owner", "/api/v1/nodes/1.2/top", 400)
	getJSON(t, e, "plain", "/api/v1/nodes/1.2/top?kind=largest", 404)
}

func TestListExtensions_Handler(t *testing.T) {
	e := newEnv(t)
	m := getJSON(t, e, "owner", "/api/v1/nodes/1.2/extensions", 200)
	items := m["items"].([]any)
	if len(items) != 1 || items[0].(map[string]any)["ext"] != "bin" || items[0].(map[string]any)["category"] != "other" {
		t.Errorf("extensions = %v", m)
	}
	if m = getJSON(t, e, "owner", "/api/v1/nodes/1.3/extensions", 200); m["items"] == nil || len(m["items"].([]any)) != 0 {
		t.Errorf("no extensions must be [], got %v", m)
	}
	getJSON(t, e, "plain", "/api/v1/nodes/1.2/extensions", 404)
}

func TestListNodeErrors_Handler(t *testing.T) {
	e := newEnv(t)
	m := getJSON(t, e, "owner", "/api/v1/nodes/1.2/errors", 200)
	items := m["items"].([]any)
	if len(items) != 1 || items[0].(map[string]any)["name"] != "b/e" || items[0].(map[string]any)["errno"] != float64(13) || m["next_cursor"] != nil {
		t.Errorf("errors = %v", m)
	}
	getJSON(t, e, "owner", "/api/v1/nodes/1.1/errors", 404)
}

func TestErrors_CursorTampered(t *testing.T) {
	e := newEnv(t)
	good := encodeCursor(errCursor{V: 1, Scan: 1, Dir: 2, RowID: 1})
	base := "/api/v1/nodes/1.2/errors?cursor="
	getJSON(t, e, "owner", base+url.QueryEscape(good), 200)
	for name, c := range map[string]string{
		"garbage":   "%%%",
		"truncated": good[:len(good)-4],
		"version":   encodeCursor(errCursor{V: 2, Scan: 1, Dir: 2, RowID: 1}),
		"node":      encodeCursor(errCursor{V: 1, Scan: 1, Dir: 4, RowID: 1}),
		"scan":      encodeCursor(errCursor{V: 1, Scan: 2, Dir: 2, RowID: 1}),
		"negative":  encodeCursor(errCursor{V: 1, Scan: 1, Dir: 2, RowID: -5}),
	} {
		if w := e.do(req{method: "GET", path: base + url.QueryEscape(c), role: "owner"}); w.Code != 400 {
			t.Errorf("%s cursor = %d, want 400", name, w.Code)
		}
	}
}

func TestGetNodeHistory_Handler(t *testing.T) {
	e := newEnv(t)
	hash := totals.PathHash(1, []byte("a"))
	day := int64(86400)
	for _, age := range []int64{10, 40, 400} {
		e.exec(`INSERT INTO history_dirs (ts, root_id, path_hash, bytes_alloc, files, tier) VALUES (?, 1, ?, ?, 1, 'raw')`,
			e.now.Unix()-age*day, hash, age)
	}
	count := func(q string) int {
		return len(getJSON(t, e, "owner", "/api/v1/nodes/1.2/history"+q, 200)["items"].([]any))
	}
	for q, want := range map[string]int{"": 1, "?window=7d": 0, "?window=30d": 1, "?window=90d": 2, "?window=1y": 2, "?window=all": 3} {
		if got := count(q); got != want {
			t.Errorf("history%s = %d points, want %d", q, got, want)
		}
	}
	getJSON(t, e, "owner", "/api/v1/nodes/1.2/history?window=2d", 400)
	getJSON(t, e, "plain", "/api/v1/nodes/1.2/history", 404)
}
