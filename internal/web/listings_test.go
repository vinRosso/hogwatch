package web

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// Fixture refs: scan 1 numbers dirs in sorted path order, so the root is 1,
// "a" is 2, "a/b" is 3 and "d" is 4. lister owns "d".
const (
	refInScope  = "1.4"
	refOutScope = "1.2"
)

func listing(e *env, role, ref, extra string) (int, string) {
	body := fmt.Sprintf(`{"ref":%q,"mode":"children"%s}`, ref, extra)
	w := e.do(req{method: "POST", path: "/api/v1/listings", role: role, body: body})
	return w.Code, w.Body.String()
}

func (e *env) jobCount() int {
	var n int
	_ = e.db.QueryRow(`SELECT count(*) FROM jobs WHERE kind = 'listing'`).Scan(&n)
	return n
}

func TestCreateListing_InScope(t *testing.T) {
	e := newEnv(t)
	code, body := listing(e, "lister", refInScope, `,"exts":["jpg","mp4"],"min_size":100,"min_age_days":7`)
	if code != 202 {
		t.Fatalf("status %d: %s", code, body)
	}
	var kind, target, params string
	var root, by int64
	err := e.db.QueryRow(`SELECT kind, root_id, target_node, params, requested_by FROM jobs`).Scan(&kind, &root, &target, &params, &by)
	if err != nil {
		t.Fatal(err)
	}
	if kind != "listing" || root != 1 || target != refInScope || by != e.users["lister"] {
		t.Fatalf("job %s %d %s %d", kind, root, target, by)
	}
	var p map[string]any
	_ = json.Unmarshal([]byte(params), &p)
	if p["mode"] != "children" || p["min_size"] != float64(100) || p["min_age_days"] != float64(7) || len(p["exts"].([]any)) != 2 {
		t.Fatalf("params %s", params)
	}
	var n int
	_ = e.db.QueryRow(`SELECT count(*) FROM audit_log WHERE action = 'listing.created' AND target = ?`, refInScope).Scan(&n)
	if n != 1 {
		t.Fatalf("audit rows %d", n)
	}
}

func TestCreateListing_OutOfScope(t *testing.T) {
	e := newEnv(t)
	code, body := listing(e, "lister", refOutScope, "")
	mcode, mbody := listing(e, "lister", "1.999", "")
	if code != 404 || mcode != 404 || body != mbody {
		t.Fatalf("out of scope %d %q, missing %d %q", code, body, mcode, mbody)
	}
	if e.jobCount() != 0 {
		t.Fatal("a job was queued")
	}
}

func TestCreateListing_StaleRef(t *testing.T) {
	e := newEnv(t)
	if code, _ := listing(e, "lister", "2.1", ""); code != 404 {
		t.Errorf("unknown scan: %d", code)
	}
	for _, ref := range []string{"1.0", "x", ""} {
		if code, _ := listing(e, "lister", ref, ""); code != 400 {
			t.Errorf("malformed ref %q: %d", ref, code)
		}
	}
	e.exec(`UPDATE scans SET status = 'deleted' WHERE id = 1`)
	if code, _ := listing(e, "lister", refInScope, ""); code != 404 {
		t.Fatalf("deleted generation: %d", code)
	}
	if e.jobCount() != 0 {
		t.Fatal("a job was queued")
	}
}

func TestCreateListing_Limit(t *testing.T) {
	e := newEnv(t)
	for i := range 2 {
		if code, body := listing(e, "lister", refInScope, ""); code != 202 {
			t.Fatalf("request %d: %d %s", i, code, body)
		}
	}
	if code, _ := listing(e, "lister", refInScope, ""); code != 429 {
		t.Fatalf("third: %d", code)
	}
	e.exec(`UPDATE jobs SET status = 'done' WHERE id = (SELECT min(id) FROM jobs)`)
	if code, _ := listing(e, "lister", refInScope, ""); code != 202 {
		t.Fatalf("after one finished: %d", code)
	}
}

func TestCreateListing_AdminAnywhere(t *testing.T) {
	e := newEnv(t)
	for _, ref := range []string{"1.1", refOutScope} {
		if code, body := listing(e, "admin", ref, ""); code != 202 {
			t.Errorf("%s: %d %s", ref, code, body)
		}
	}
	if code, _ := listing(e, "admin", "1.999", ""); code != 404 {
		t.Errorf("missing node: %d", code)
	}
	e.exec(`UPDATE scans SET status = 'deleted' WHERE id = 1`)
	if code, _ := listing(e, "admin", "1.1", ""); code != 404 {
		t.Errorf("deleted generation: %d", code)
	}
}

func TestCreateListing_Validation(t *testing.T) {
	e := newEnv(t)
	many := `"` + strings.Repeat(`a","`, 20) + `a"`
	for name, extra := range map[string]string{
		"negative size": `,"min_size":-1`,
		"negative age":  `,"min_age_days":-1`,
		"huge age":      `,"min_age_days":36501`,
		"upper ext":     `,"exts":["JPG"]`,
		"dotted ext":    `,"exts":[".jpg"]`,
		"empty ext":     `,"exts":[""]`,
		"long ext":      `,"exts":["` + strings.Repeat("a", 33) + `"]`,
		"21 exts":       `,"exts":[` + many + `]`,
	} {
		if code, _ := listing(e, "lister", refInScope, extra); code != 400 {
			t.Errorf("%s: %d", name, code)
		}
	}
	w := e.do(req{method: "POST", path: "/api/v1/listings", role: "lister", body: `{"ref":"1.4","mode":"everything"}`})
	if w.Code != 400 {
		t.Errorf("bad mode: %d", w.Code)
	}
	if e.jobCount() != 0 {
		t.Fatal("an invalid request queued a job")
	}
}

func TestCreateListing_NeedsListingRight(t *testing.T) {
	e := newEnv(t)
	if code, _ := listing(e, "owner", "1.2", ""); code != 403 {
		t.Fatalf("owner without can_list_files: %d", code)
	}
}

func (e *env) insertListing(user string, status string, progress, errText any) int64 {
	var id int64
	err := e.db.QueryRow(`INSERT INTO jobs (kind, root_id, target_node, params, requested_by, status, progress, error, created_at)
	        VALUES ('listing', 1, '1.4', '{"mode":"children"}', ?, ?, ?, ?, 1) RETURNING id`,
		e.users[user], status, progress, errText).Scan(&id)
	if err != nil {
		panic(err)
	}
	return id
}

func (e *env) addResults(job int64, n int) {
	for i := 1; i <= n; i++ {
		e.exec(`INSERT INTO listing_results (job_id, seq, rel_name, type, size, alloc, mtime, uid) VALUES (?, ?, CAST(? AS BLOB), 'file', 1, 1, 1, 0)`,
			job, i, fmt.Sprintf("f%04d", i))
	}
}

func TestGetListing_Handler(t *testing.T) {
	e := newEnv(t)
	id := e.insertListing("lister", "done", `{"rows":1200,"truncated":true}`, nil)
	e.addResults(id, 1200)
	path := fmt.Sprintf("/api/v1/listings/%d", id)
	m := getJSON(t, e, "lister", path, 200)
	items := m["items"].([]any)
	if len(items) != 500 || m["status"] != "done" || m["truncated"] != true || m["error"] != nil || m["next_cursor"] == nil {
		t.Fatalf("page 1: %d items, %v", len(items), m)
	}
	seen := len(items)
	for m["next_cursor"] != nil {
		m = getJSON(t, e, "lister", path+"?cursor="+m["next_cursor"].(string), 200)
		seen += len(m["items"].([]any))
	}
	if seen != 1200 {
		t.Errorf("paged %d rows, want 1200", seen)
	}
	// a cursor from another job is refused
	other := e.insertListing("lister", "done", nil, nil)
	m = getJSON(t, e, "lister", path, 200)
	getJSON(t, e, "lister", fmt.Sprintf("/api/v1/listings/%d?cursor=%s", other, m["next_cursor"]), 400)
}

func TestGetListing_Visibility(t *testing.T) {
	e := newEnv(t)
	e.exec(`UPDATE users SET can_list_files = 1 WHERE id = ?`, e.users["owner"])
	id := e.insertListing("lister", "done", nil, nil)
	path := fmt.Sprintf("/api/v1/listings/%d", id)
	getJSON(t, e, "admin", path, 200)
	w := e.do(req{method: "GET", path: path, role: "owner"})
	miss := e.do(req{method: "GET", path: "/api/v1/listings/9999", role: "owner"})
	if w.Code != 404 || miss.Code != 404 || w.Body.String() != miss.Body.String() {
		t.Errorf("foreign job %d %s vs missing %d %s", w.Code, w.Body, miss.Code, miss.Body)
	}
	var scan int64
	_ = e.db.QueryRow(`INSERT INTO jobs (kind, root_id, params, requested_by, created_at) VALUES ('scan', 1, '{}', ?, 1) RETURNING id`, e.users["lister"]).Scan(&scan)
	if w = e.do(req{method: "GET", path: fmt.Sprintf("/api/v1/listings/%d", scan), role: "lister"}); w.Code != 404 {
		t.Errorf("scan job read as listing: %d", w.Code)
	}
}

func TestGetListing_ErrorIsFixed(t *testing.T) {
	e := newEnv(t)
	id := e.insertListing("lister", "failed", nil, "open /srv/secret/x: permission denied")
	m := getJSON(t, e, "lister", fmt.Sprintf("/api/v1/listings/%d", id), 200)
	if m["error"] != "listing failed" {
		t.Errorf("raw error leaked: %v", m["error"])
	}
	id = e.insertListing("lister", "failed", nil, "not permitted")
	if m = getJSON(t, e, "lister", fmt.Sprintf("/api/v1/listings/%d", id), 200); m["error"] != "not permitted" {
		t.Errorf("fixed message lost: %v", m["error"])
	}
}

func TestCancelListing_Handler(t *testing.T) {
	e := newEnv(t)
	e.exec(`UPDATE users SET can_list_files = 1 WHERE id = ?`, e.users["owner"])
	id := e.insertListing("lister", "queued", nil, nil)
	path := fmt.Sprintf("/api/v1/listings/%d", id)
	cancels := func() int {
		var n int
		_ = e.db.QueryRow(`SELECT count(*) FROM jobs WHERE kind = 'cancel'`).Scan(&n)
		return n
	}
	if w := e.do(req{method: "DELETE", path: path, role: "owner"}); w.Code != 404 || cancels() != 0 {
		t.Fatalf("foreign cancel: %d, %d cancel rows", w.Code, cancels())
	}
	for i := 0; i < 2; i++ {
		if w := e.do(req{method: "DELETE", path: path, role: "lister"}); w.Code != 204 {
			t.Fatalf("cancel %d: %d %s", i, w.Code, w.Body)
		}
	}
	var params, status string
	_ = e.db.QueryRow(`SELECT params FROM jobs WHERE kind = 'cancel'`).Scan(&params)
	_ = e.db.QueryRow(`SELECT status FROM jobs WHERE id = ?`, id).Scan(&status)
	if cancels() != 1 || params != fmt.Sprintf(`{"job_id":%d}`, id) || status != "queued" {
		t.Errorf("cancels %d params %s status %s", cancels(), params, status)
	}
	var n int
	_ = e.db.QueryRow(`SELECT count(*) FROM audit_log WHERE action = 'listing.cancelled'`).Scan(&n)
	if n != 1 {
		t.Errorf("audit rows %d, want 1 (only the inserting call)", n)
	}
	done := e.insertListing("lister", "done", nil, nil)
	if w := e.do(req{method: "DELETE", path: fmt.Sprintf("/api/v1/listings/%d", done), role: "lister"}); w.Code != 204 || cancels() != 1 {
		t.Errorf("finished job: %d, %d cancel rows", w.Code, cancels())
	}
}

func TestListing_ExpiredIsGone(t *testing.T) {
	e := newEnv(t)
	id := e.insertListing("lister", "queued", nil, nil)
	e.exec(`UPDATE jobs SET expires_at = ? WHERE id = ?`, e.now.Unix()-1, id)
	path := fmt.Sprintf("/api/v1/listings/%d", id)
	if w := e.do(req{method: "GET", path: path, role: "lister"}); w.Code != 404 {
		t.Errorf("expired GET = %d", w.Code)
	}
	if w := e.do(req{method: "DELETE", path: path, role: "lister"}); w.Code != 404 {
		t.Errorf("expired DELETE = %d", w.Code)
	}
	var n int
	_ = e.db.QueryRow(`SELECT count(*) FROM jobs WHERE kind = 'cancel'`).Scan(&n)
	if n != 0 {
		t.Errorf("%d cancel rows for an expired job", n)
	}
	e.exec(`UPDATE jobs SET expires_at = ? WHERE id = ?`, e.now.Unix()+60, id)
	getJSON(t, e, "lister", path, 200)
}

func TestListing_UnreadableProgressIsTruncated(t *testing.T) {
	e := newEnv(t)
	id := e.insertListing("lister", "done", `"garbage"`, nil)
	m := getJSON(t, e, "lister", fmt.Sprintf("/api/v1/listings/%d", id), 200)
	if m["truncated"] != true {
		t.Errorf("a listing with unreadable progress looks complete: %v", m)
	}
}
