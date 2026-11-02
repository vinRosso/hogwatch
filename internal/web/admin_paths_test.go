package web

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"sync"
	"testing"

	"github.com/vinRosso/hogwatch/internal/gen"
	"github.com/vinRosso/hogwatch/internal/totals"
)

func pathsURL(user int64) string { return userURL(user) + "/paths" }

func (e *env) createPath(user int64, root int64, rel string) (int, map[string]any) {
	e.t.Helper()
	b, _ := json.Marshal(map[string]any{"root_id": root, "rel_path": rel})
	return e.admin("POST", pathsURL(user), string(b))
}

func TestCreateUserPath_Overlap(t *testing.T) {
	// The fixture: owner has "a", lister has "d", on root 1.
	cases := []struct {
		name, user, rel string
		root            int64
		want            int
		redundant       bool
	}{
		{"inside another user's path", "plain", "a/b", 1, 422, false},
		{"equal to another user's path", "plain", "a", 1, 422, false},
		{"containing another user's path", "plain", "", 1, 422, false},
		{"the whole root vs two users", "owner", "", 1, 422, false},
		{"nested in own path", "owner", "a/b", 1, 201, true},
		{"equal own path", "owner", "a", 1, 409, false},
		{"disjoint, shares a prefix", "plain", "ab", 1, 201, false},
		{"dot-dot", "plain", "..", 1, 400, false},
		{"empty segment", "plain", "a//b", 1, 400, false},
		{"bad escape", "plain", `a\q`, 1, 400, false},
		{"unknown root", "plain", "x", 99, 404, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := newEnv(t)
			a := e.auditCount("path.assigned")
			code, body := e.createPath(e.users[c.user], c.root, c.rel)
			if code != c.want {
				t.Fatalf("%d %v, want %d", code, body, c.want)
			}
			if code == 201 {
				if body["redundant"] != c.redundant || e.auditCount("path.assigned") != a+1 {
					t.Fatalf("body %v, audits %d", body, e.auditCount("path.assigned"))
				}
			} else if e.auditCount("path.assigned") != a {
				t.Fatal("a refused assignment was audited")
			}
		})
	}
	e := newEnv(t)
	if code, _ := e.createPath(9999, 1, "x"); code != 404 {
		t.Fatalf("unknown user: %d", code)
	}
	// An unknown user is 404 even for a path that overlaps: no username leaks.
	if code, b := e.createPath(9999, 1, "a/b"); code != 404 {
		t.Fatalf("unknown user, overlapping path: %d %v, want 404", code, b)
	}
}

func TestUserPath_HostileName(t *testing.T) {
	e := newEnv(t)
	rel := "bad\\xff/new\nline" // escaped form of the bytes "bad\xff/new\nline"
	code, body := e.createPath(e.users["plain"], 1, rel)
	if code != 201 || body["rel_path"] != rel {
		t.Fatalf("%d %v", code, body)
	}
	var raw []byte
	if err := e.db.QueryRow(`SELECT rel_path FROM user_paths WHERE user_id = ?`, e.users["plain"]).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(raw, []byte("bad\xff/new\nline")) {
		t.Fatalf("stored %q", raw)
	}
	_, list := e.admin("GET", pathsURL(e.users["plain"]), "")
	items := list["items"].([]any)
	if len(items) != 1 || items[0].(map[string]any)["rel_path"] != rel {
		t.Fatalf("list %v", list)
	}
}

// oracle computes what a publish writes for a user's paths on root 1.
func (e *env) oracle(user int64) ([]totals.UsageRow, []totals.PathRow) {
	e.t.Helper()
	d, _, err := gen.OpenReadOnly(e.t.Context(), gen.Path(e.dataDir, 1, 1))
	if err != nil {
		e.t.Fatal(err)
	}
	defer func() { _ = d.Close() }()
	rows, err := e.db.Query(`SELECT id, rel_path FROM user_paths WHERE user_id = ? AND root_id = 1 ORDER BY id`, user)
	if err != nil {
		e.t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	var ps []totals.Path
	for rows.Next() {
		p := totals.Path{UserID: user}
		var rel []byte
		if err := rows.Scan(&p.ID, &rel); err != nil {
			e.t.Fatal(err)
		}
		p.Rel = string(rel)
		ps = append(ps, p)
	}
	u, pr, err := totals.Usage(e.t.Context(), d, ps)
	if err != nil {
		e.t.Fatal(err)
	}
	return u, pr
}

// stored reads the usage and path_stats rows of a user on scan 1.
func (e *env) stored(user int64) ([]totals.UsageRow, []totals.PathRow) {
	e.t.Helper()
	var us []totals.UsageRow
	r1, err := e.db.Query(`SELECT user_id, bytes_logical, bytes_alloc, files, shared_hardlink_bytes FROM usage WHERE scan_id = 1 AND user_id = ?`, user)
	if err != nil {
		e.t.Fatal(err)
	}
	for r1.Next() {
		var u totals.UsageRow
		_ = r1.Scan(&u.UserID, &u.BytesLogical, &u.BytesAlloc, &u.Files, &u.SharedHardlinkBytes)
		us = append(us, u)
	}
	_ = r1.Close()
	var ps []totals.PathRow
	r2, err := e.db.Query(`SELECT s.user_path_id, s.found, s.redundant, s.bytes_logical, s.bytes_alloc, s.files, s.shared_hardlink_bytes
		FROM path_stats s JOIN user_paths p ON p.id = s.user_path_id WHERE s.scan_id = 1 AND p.user_id = ? ORDER BY s.user_path_id`, user)
	if err != nil {
		e.t.Fatal(err)
	}
	for r2.Next() {
		var p totals.PathRow
		_ = r2.Scan(&p.UserPathID, &p.Found, &p.Redundant, &p.BytesLogical, &p.BytesAlloc, &p.Files, &p.SharedHardlinkBytes)
		ps = append(ps, p)
	}
	_ = r2.Close()
	return us, ps
}

func TestRecompute_MatchesPublish(t *testing.T) {
	e := newEnv(t)
	owner := e.users["owner"]
	check := func(step string) {
		t.Helper()
		wu, wp := e.oracle(owner)
		gu, gp := e.stored(owner)
		if len(wu) != 1 || wu[0].BytesAlloc == 0 || len(gp) == 0 {
			t.Fatalf("%s: the oracle is vacuous: %+v %+v", step, wu, wp)
		}
		if !reflect.DeepEqual(wu, gu) || !reflect.DeepEqual(wp, gp) {
			t.Fatalf("%s: stored != publish\n usage %+v vs %+v\n paths %+v vs %+v", step, gu, wu, gp, wp)
		}
	}
	if code, b := e.createPath(owner, 1, "a/b"); code != 201 {
		t.Fatalf("create a/b: %d %v", code, b)
	}
	check("after a/b (nested in a)")
	var aID int64
	_ = e.db.QueryRow(`SELECT id FROM user_paths WHERE user_id = ? AND rel_path = CAST('a' AS BLOB)`, owner).Scan(&aID)
	e.exec(`UPDATE path_stats SET zfs_used = 777, zfs_dataset = 'tank/a' WHERE user_path_id = ?`, aID)
	if code, _ := e.createPath(owner, 1, "missing"); code != 201 {
		t.Fatalf("create missing: %d", code)
	}
	check("after a missing path")
	var zused sql.NullInt64
	_ = e.db.QueryRow(`SELECT zfs_used FROM path_stats WHERE user_path_id = ?`, aID).Scan(&zused)
	if !zused.Valid || zused.Int64 != 777 {
		t.Fatalf("zfs_used of an unchanged path = %v, want 777", zused)
	}
	var newZ sql.NullInt64
	_ = e.db.QueryRow(`SELECT zfs_used FROM path_stats s JOIN user_paths p ON p.id = s.user_path_id WHERE p.rel_path = CAST('missing' AS BLOB)`).Scan(&newZ)
	if newZ.Valid {
		t.Fatal("a new path must have NULL zfs columns")
	}
	// Removing the enclosing path makes a/b effective.
	if code, _ := e.admin("DELETE", fmt.Sprintf("%s/%d", pathsURL(owner), aID), ""); code != 204 {
		t.Fatalf("delete a: %d", code)
	}
	check("after deleting a")
	// The last path on the root removes the usage row.
	for _, p := range e.pathIDs(owner) {
		if code, _ := e.admin("DELETE", fmt.Sprintf("%s/%d", pathsURL(owner), p), ""); code != 204 {
			t.Fatalf("delete %d: %d", p, code)
		}
	}
	if us, ps := e.stored(owner); len(us) != 0 || len(ps) != 0 {
		t.Fatalf("rows left after the last path: %+v %+v", us, ps)
	}
}

func (e *env) pathIDs(user int64) []int64 {
	e.t.Helper()
	rows, err := e.db.Query(`SELECT id FROM user_paths WHERE user_id = ? ORDER BY id`, user)
	if err != nil {
		e.t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	var ids []int64
	for rows.Next() {
		var id int64
		_ = rows.Scan(&id)
		ids = append(ids, id)
	}
	return ids
}

func TestCreateUserPath_ScopeImmediate(t *testing.T) {
	e := newEnv(t)
	plain := e.users["plain"]
	resolve := func() int {
		return e.do(req{method: "GET", path: "/api/v1/resolve?root=1&path=d", role: "plain"}).Code
	}
	if got := resolve(); got != http.StatusNotFound {
		t.Fatalf("before: %d, want 404", got)
	}
	if code, b := e.createPath(plain, 1, "d"); code != 422 {
		t.Fatalf("d belongs to lister: %d %v", code, b)
	}
	e.exec(`DELETE FROM user_paths WHERE user_id = ?`, e.users["lister"])
	if code, b := e.createPath(plain, 1, "d"); code != 201 {
		t.Fatalf("assign d: %d %v", code, b)
	}
	if got := resolve(); got != http.StatusOK {
		t.Fatalf("after the assignment: %d, want 200", got)
	}
	w := e.do(req{method: "GET", path: "/api/v1/me/usage", role: "plain"})
	var usage struct {
		Total struct {
			BytesAlloc int64 `json:"bytes_alloc"`
		}
	}
	_ = json.Unmarshal(w.Body.Bytes(), &usage)
	if w.Code != 200 || usage.Total.BytesAlloc != 30 {
		t.Fatalf("/me/usage right after the assignment: %d %s", w.Code, w.Body)
	}
	if code, _ := e.admin("GET", "/api/v1/admin/users", ""); code != 200 {
		t.Fatal("admin lost access")
	}
}

func TestDeleteUserPath(t *testing.T) {
	e := newEnv(t)
	owner, plain := e.users["owner"], e.users["plain"]
	ids := e.pathIDs(owner)
	// Another user's path id under this user's URL is 404.
	if code, _ := e.admin("DELETE", fmt.Sprintf("%s/%d", pathsURL(plain), ids[0]), ""); code != 404 {
		t.Fatalf("cross-user delete: %d, want 404", code)
	}
	if len(e.pathIDs(owner)) != 1 {
		t.Fatal("the path was deleted")
	}
	if code, _ := e.admin("DELETE", fmt.Sprintf("%s/%d", pathsURL(owner), ids[0]), ""); code != 204 {
		t.Fatalf("delete: %d", code)
	}
	if e.auditCount("path.removed") != 1 {
		t.Fatal("not audited once")
	}
	if code, _ := e.admin("DELETE", fmt.Sprintf("%s/%d", pathsURL(owner), ids[0]), ""); code != 404 {
		t.Fatalf("second delete: %d", code)
	}
}

func TestDeleteUserPath_ClearsIssue(t *testing.T) {
	e := newEnv(t)
	owner := e.users["owner"]
	id := e.pathIDs(owner)[0]
	e.exec(`INSERT INTO scan_roots (id, name, container_path, host_path) VALUES (2, 'o', '/mnt/o', '/srv/o')`)
	e.exec(`INSERT INTO config_issues (root_id, kind, details, created_at) VALUES (2, 'cross_root_duplicate', ?, 1)`,
		fmt.Sprintf(`{"user_path_id":%d,"other_root_id":1,"node":"x"}`, id))
	e.exec(`INSERT INTO config_issues (root_id, kind, details, created_at) VALUES (1, 'cross_root_duplicate', ?, 1)`,
		fmt.Sprintf(`{"user_path_id":%d,"other_root_id":2,"node":"x"}`, id))
	if code, _ := e.admin("DELETE", fmt.Sprintf("%s/%d", pathsURL(owner), id), ""); code != 204 {
		t.Fatalf("delete: %d", code)
	}
	var n int
	_ = e.db.QueryRow(`SELECT count(*) FROM config_issues WHERE kind = 'cross_root_duplicate' AND json_extract(details, '$.user_path_id') = ?`, id).Scan(&n)
	if n != 0 {
		t.Fatalf("%d issues of the deleted path remain", n)
	}
}

func TestRecompute_NoCurrentScan(t *testing.T) {
	e := newEnv(t)
	e.exec(`INSERT INTO scan_roots (id, name, container_path, host_path) VALUES (2, 'o', '/mnt/o', '/srv/o')`)
	if code, b := e.createPath(e.users["plain"], 2, "x"); code != 201 || b["found"] != false {
		t.Fatalf("%d %v", code, b)
	}
	var n int
	_ = e.db.QueryRow(`SELECT count(*) FROM usage WHERE user_id = ?`, e.users["plain"]).Scan(&n)
	if n != 0 {
		t.Fatal("usage rows written without a scan")
	}
}

func TestRecompute_ConcurrentChange(t *testing.T) {
	var fires, limit, seq int
	var e *env
	hook := func() {
		if fires >= limit {
			return
		}
		fires++
		seq++
		// Another path of the same user appears between compute and the transaction.
		e.exec(`INSERT INTO user_paths (user_id, root_id, rel_path, created_at) VALUES (?, 1, CAST(? AS BLOB), 1)`,
			e.users["plain"], fmt.Sprintf("sneak%d", seq))
	}
	e = newEnv(t, func(d *Deps) { d.afterCompute = hook })

	// A change on every attempt: 409, and nothing of ours was written.
	limit = 10
	if code, _ := e.createPath(e.users["plain"], 1, "mine"); code != http.StatusConflict {
		t.Fatalf("always-changing paths: %d, want 409", code)
	}
	var n int
	_ = e.db.QueryRow(`SELECT count(*) FROM user_paths WHERE rel_path = CAST('mine' AS BLOB)`).Scan(&n)
	if n != 0 || e.auditCount("path.assigned") != 0 {
		t.Fatal("a refused assignment wrote state")
	}

	// One change: the second attempt sees it and succeeds.
	fires, limit = 0, 1
	if code, b := e.createPath(e.users["plain"], 1, "mine"); code != http.StatusCreated {
		t.Fatalf("one concurrent change: %d %v, want 201", code, b)
	}
	if e.auditCount("path.assigned") != 1 {
		t.Fatal("not audited once")
	}
}

func TestCreateUserPath_Concurrent(t *testing.T) {
	e := newEnv(t)
	e.exec(`INSERT INTO scan_roots (id, name, container_path, host_path) VALUES (2, 'o', '/mnt/o', '/srv/o')`)
	var wg sync.WaitGroup
	for i := range 20 {
		codes := make([]int, 2)
		for j, role := range []string{"owner", "plain"} {
			wg.Add(1)
			go func() {
				defer wg.Done()
				b, _ := json.Marshal(map[string]any{"root_id": 2, "rel_path": fmt.Sprintf("p%d/x", i)})
				w := e.do(req{method: "POST", path: pathsURL(e.users[role]), role: "admin", body: string(b)})
				codes[j] = w.Code
			}()
		}
		wg.Wait()
		if codes[0]+codes[1] != 201+422 || codes[0] == codes[1] {
			t.Fatalf("pair %d: %v, want one 201 and one 422", i, codes)
		}
	}
}

func TestListAllUserPaths(t *testing.T) {
	e := newEnv(t)
	code, body := e.admin("GET", "/api/v1/admin/user-paths", "")
	if code != 200 {
		t.Fatalf("%d", code)
	}
	items, _ := body["items"].([]any)
	var n int
	_ = e.db.QueryRow(`SELECT COUNT(*) FROM user_paths`).Scan(&n)
	if len(items) != n || n == 0 {
		t.Fatalf("%d items for %d assignments", len(items), n)
	}
	first, _ := items[0].(map[string]any)
	for _, k := range []string{"user_id", "root_id", "rel_path"} {
		if _, ok := first[k]; !ok {
			t.Errorf("item lacks %s: %v", k, first)
		}
	}
	if w := e.do(req{method: "GET", path: "/api/v1/admin/user-paths", role: "owner"}); w.Code != http.StatusForbidden {
		t.Fatalf("non-admin got %d, want 403", w.Code)
	}
}
