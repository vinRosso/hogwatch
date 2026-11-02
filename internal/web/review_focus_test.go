package web

import (
	"encoding/csv"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vinRosso/hogwatch/internal/fname"
	"github.com/vinRosso/hogwatch/internal/gen/gentest"
)

// addScan2 builds scan 2 of root 1 from dirs and makes it the current scan.
func addScan2(e *env, dirs []gentest.Dir) {
	file := gentest.BuildWith(e.t, e.dataDir, 1, 2, dirs, gentest.BuildOpts{TopAnchors: []string{""}})
	e.exec(`INSERT INTO scans (id, root_id, file, status, trigger, started_at, finished_at) VALUES (2, 1, ?, 'done', 'manual', 3, 4)`, filepath.Base(file))
	e.exec(`UPDATE scan_roots SET current_scan_id = 2`)
}

func TestHostileNames_RoundTrip(t *testing.T) {
	e := newEnv(t)
	names := []string{
		"bad\xff", "new\nline", "tab\tx", "\u202eexe.txt", strings.Repeat("n", 255),
		"=cmd|' C calc'!A0", "+1", "-1", "@x",
	}
	var dirs []gentest.Dir
	for _, n := range names {
		dirs = append(dirs, gentest.Dir{Rel: n, Files: 1, BytesAlloc: 10})
	}
	addScan2(e, dirs)

	children := getJSON(t, e, "admin", "/api/v1/nodes/2.1/children?limit=500", 200)["items"].([]any)
	byName := map[string]string{}
	for _, c := range children {
		m := c.(map[string]any)
		if m["kind"] == "dir" {
			byName[m["name"].(string)] = m["ref"].(string)
		}
	}
	for _, raw := range names {
		esc := string(fname.Escape([]byte(raw)))
		ref, ok := byName[esc]
		if !ok {
			t.Errorf("child %q missing from %v", esc, byName)
			continue
		}
		node := getJSON(t, e, "admin", "/api/v1/nodes/"+ref, 200)
		if node["rel_path"] != esc || node["name"] != esc {
			t.Errorf("%q: node = %v", esc, node)
		}
		res := getJSON(t, e, "admin", "/api/v1/resolve?root=1&path="+url.QueryEscape(node["rel_path"].(string)), 200)
		if res["ref"] != ref {
			t.Errorf("%q: resolve = %v, want ref %s", esc, res, ref)
		}
	}
	tm := getJSON(t, e, "admin", "/api/v1/nodes/2.1/treemap?depth=1", 200)
	tmNames := map[string]bool{}
	for _, c := range tm["children"].([]any) {
		tmNames[c.(map[string]any)["name"].(string)] = true
	}
	for _, raw := range names {
		if esc := string(fname.Escape([]byte(raw))); !tmNames[esc] {
			t.Errorf("treemap lacks %q", esc)
		}
	}
	w := exportGet(e, "admin", "2.1", "format=csv&what=tree")
	rows, err := csv.NewReader(w.Body).ReadAll()
	if err != nil || w.Code != 200 {
		t.Fatalf("export: %d %v", w.Code, err)
	}
	want := map[string]bool{}
	for _, raw := range names {
		want[string(fname.Escape([]byte(raw)))] = true
	}
	for _, r := range rows[1:] {
		for _, cell := range r {
			if cell != "" && strings.ContainsRune("=+-@\t\r", rune(cell[0])) {
				t.Errorf("unguarded cell %q", cell)
			}
		}
		delete(want, strings.TrimPrefix(r[0], "'"))
	}
	if len(want) != 0 {
		t.Errorf("export lost names %v", want)
	}
}

func TestStaleRef_AllNodeEndpoints(t *testing.T) {
	e := newEnv(t)
	addScan2(e, []gentest.Dir{{Rel: "a", Files: 1, BytesAlloc: 10}})
	paths := []string{
		"/api/v1/nodes/%s", "/api/v1/nodes/%s/children", "/api/v1/nodes/%s/treemap", "/api/v1/nodes/%s/top?kind=largest",
		"/api/v1/nodes/%s/extensions", "/api/v1/nodes/%s/errors", "/api/v1/nodes/%s/history",
		"/api/v1/export/%s?format=csv&what=tree",
	}
	for _, p := range paths {
		if w := e.do(req{method: "GET", path: strings.Replace(p, "%s", "1.1", 1), role: "admin"}); w.Code != 200 {
			t.Errorf("grace period: %s = %d: %s", p, w.Code, w.Body)
		}
	}
	e.exec(`UPDATE scans SET status = 'deleted' WHERE id = 1`)
	var file string
	_ = e.db.QueryRow(`SELECT file FROM scans WHERE id = 1`).Scan(&file)
	_ = os.Remove(filepath.Join(e.dataDir, "scans", file))
	_ = os.Remove(filepath.Join(e.dataDir, file))
	e.repo.Sweep(t.Context())

	want := e.do(req{method: "GET", path: "/api/v1/nodes/9.9", role: "admin"})
	if want.Code != 404 {
		t.Fatalf("missing ref = %d", want.Code)
	}
	for _, p := range paths {
		w := e.do(req{method: "GET", path: strings.Replace(p, "%s", "1.1", 1), role: "admin"})
		if w.Code != 404 || w.Body.String() != want.Body.String() {
			t.Errorf("stale %s = %d %q, want the common 404", p, w.Code, w.Body)
		}
	}
	if code, body := listing(e, "admin", "1.1", ""); code != 404 || body != want.Body.String() {
		t.Errorf("stale createListing = %d %q", code, body)
	}
}

func TestDegenerateScopes(t *testing.T) {
	e := newEnv(t)
	// plain: no paths. Give "disabled"-free extra shapes through plain's session.
	for _, role := range []string{"plain"} {
		roots := getJSON(t, e, role, "/api/v1/roots", 200)["items"].([]any)
		usage := getJSON(t, e, role, "/api/v1/me/usage", 200)
		scans := getJSON(t, e, role, "/api/v1/scans", 200)["items"].([]any)
		if len(roots) != 0 || len(usage["mounts"].([]any)) != 0 || len(usage["paths"].([]any)) != 0 || len(scans) != 0 {
			t.Errorf("%s: roots %v usage %v scans %v", role, roots, usage, scans)
		}
	}
	// plain gets a path that is not in the generation.
	e.exec(`INSERT INTO user_paths (user_id, root_id, rel_path, created_at) VALUES (?, 1, CAST('nope' AS BLOB), 1)`, e.users["plain"])
	roots := getJSON(t, e, "plain", "/api/v1/roots", 200)["items"].([]any)
	if len(roots) != 1 {
		t.Fatalf("roots = %v", roots)
	}
	an := roots[0].(map[string]any)["anchors"].([]any)[0].(map[string]any)
	if an["found"] != false || an["ref"] != nil {
		t.Errorf("missing-path anchor = %v", an)
	}
	getJSON(t, e, "plain", "/api/v1/me/usage", 200)
	for _, p := range []string{"/api/v1/nodes/1.1", "/api/v1/nodes/1.1/children", "/api/v1/nodes/1.2", "/api/v1/nodes/1.1/treemap"} {
		if w := e.do(req{method: "GET", path: p, role: "plain"}); w.Code != 404 {
			t.Errorf("plain %s = %d", p, w.Code)
		}
	}
	// A root without a current scan.
	e.exec(`UPDATE scan_roots SET current_scan_id = NULL`)
	getJSON(t, e, "owner", "/api/v1/roots", 200)
	getJSON(t, e, "owner", "/api/v1/me/usage", 200)
	getJSON(t, e, "admin", "/api/v1/roots", 200)
	e.exec(`UPDATE scan_roots SET current_scan_id = 1`)

	// The anchor's node reveals nothing above it.
	node := getJSON(t, e, "owner", "/api/v1/nodes/1.2", 200)
	if node["parent_ref"] != nil {
		t.Errorf("anchor parent_ref = %v", node["parent_ref"])
	}
	for _, p := range []string{"/api/v1/nodes/1.2", "/api/v1/nodes/1.2/children", "/api/v1/nodes/1.2/treemap", "/api/v1/nodes/1.2/errors"} {
		w := e.do(req{method: "GET", path: p, role: "owner"})
		body := w.Body.String()
		if w.Code != 200 || strings.Contains(body, `"1.1"`) || strings.Contains(body, `"1.4"`) {
			t.Errorf("owner %s = %d leaks an outside id: %s", p, w.Code, body)
		}
	}
	// A path that vanished in the next scan.
	addScan2(e, nil)
	getJSON(t, e, "owner", "/api/v1/roots", 200)
	getJSON(t, e, "owner", "/api/v1/me/usage", 200)
	if w := e.do(req{method: "GET", path: "/api/v1/nodes/2.1", role: "owner"}); w.Code != 404 {
		t.Errorf("owner on root of scan 2 = %d", w.Code)
	}
}

// A 255-byte name is legal; the cursor that follows it must still decode.
func TestChildren_CursorAfterLongName(t *testing.T) {
	e := newEnv(t)
	long := strings.Repeat("n", 255)
	addScan2(e, []gentest.Dir{{Rel: long, Files: 1, BytesAlloc: 10}, {Rel: "o", Files: 1, BytesAlloc: 10}, {Rel: "p", Files: 1, BytesAlloc: 10}})
	var names []string
	q := "/api/v1/nodes/2.1/children?sort=name&limit=1"
	for pages := 0; pages < 10; pages++ {
		m := getJSON(t, e, "admin", q, 200)
		for _, c := range m["items"].([]any) {
			if c.(map[string]any)["kind"] == "dir" {
				names = append(names, c.(map[string]any)["name"].(string))
			}
		}
		if m["next_cursor"] == nil {
			break
		}
		q = "/api/v1/nodes/2.1/children?sort=name&limit=1&cursor=" + m["next_cursor"].(string)
	}
	if len(names) != 3 || names[0] != long || names[2] != "p" {
		t.Errorf("paged names = %d, want 3 ending in p", len(names))
	}
}
