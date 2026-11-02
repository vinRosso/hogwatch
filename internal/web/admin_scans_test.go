package web

import (
	"fmt"
	"net/http"
	"testing"
)

func (e *env) scanJobs(kind string) int {
	var n int
	_ = e.db.QueryRow(`SELECT count(*) FROM jobs WHERE kind = ?`, kind).Scan(&n)
	return n
}

func (e *env) insertScanJob(root int64, status string, progress any) int64 {
	var id int64
	err := e.db.QueryRow(`INSERT INTO jobs (kind, root_id, params, status, progress, created_at)
	        VALUES ('scan', ?, '{"trigger":"manual"}', ?, ?, 1) RETURNING id`, root, status, progress).Scan(&id)
	if err != nil {
		panic(err)
	}
	return id
}

func TestCreateScan(t *testing.T) {
	e := newEnv(t)
	code, body := e.admin("POST", "/api/v1/scans", `{"root_id":1}`)
	if code != http.StatusAccepted || body["status"] != "queued" {
		t.Fatalf("create: %d %v", code, body)
	}
	var kind, params string
	var by int64
	if err := e.db.QueryRow(`SELECT kind, params, requested_by FROM jobs WHERE id = ?`, int64(body["id"].(float64))).Scan(&kind, &params, &by); err != nil {
		t.Fatal(err)
	}
	if kind != "scan" || params != `{"trigger":"manual"}` || by != e.users["admin"] {
		t.Fatalf("job %s %s %d", kind, params, by)
	}
	if e.auditCount("scan.requested") != 1 {
		t.Fatal("not audited once")
	}
	if code, _ := e.admin("POST", "/api/v1/scans", `{"root_id":1}`); code != 409 {
		t.Fatalf("second request: %d", code)
	}
	if code, _ := e.admin("POST", "/api/v1/scans", `{"root_id":99}`); code != 404 {
		t.Fatalf("unknown root: %d", code)
	}
	if code, _ := e.admin("POST", "/api/v1/scans", `{}`); code != 400 {
		t.Fatalf("empty body: %d", code)
	}
	e.exec(`INSERT INTO scan_roots (id, name, container_path, host_path, enabled) VALUES (2, 'off', '/mnt/off', '/srv/off', 0)`)
	if code, _ := e.admin("POST", "/api/v1/scans", `{"root_id":2}`); code != 409 {
		t.Fatalf("disabled root: %d", code)
	}
	if e.scanJobs("scan") != 1 || e.auditCount("scan.requested") != 1 {
		t.Fatal("a refused request left a trace")
	}
}

func TestCancelScan(t *testing.T) {
	e := newEnv(t)
	id := e.insertScanJob(1, "queued", nil)
	url := fmt.Sprintf("/api/v1/scans/%d/cancel", id)
	if code, b := e.admin("POST", url, ``); code != 204 {
		t.Fatalf("cancel: %d %v", code, b)
	}
	var params string
	var root int64
	if err := e.db.QueryRow(`SELECT params, root_id FROM jobs WHERE kind = 'cancel'`).Scan(&params, &root); err != nil {
		t.Fatal(err)
	}
	if params != fmt.Sprintf(`{"job_id":%d}`, id) || root != 1 {
		t.Fatalf("cancel job %s root %d", params, root)
	}
	e.admin("POST", url, ``)
	if e.scanJobs("cancel") != 1 || e.auditCount("scan.cancel_requested") != 1 {
		t.Fatal("repeat added a row")
	}
	var status string
	_ = e.db.QueryRow(`SELECT status FROM jobs WHERE id = ?`, id).Scan(&status)
	if status != "queued" {
		t.Fatalf("web changed status to %s", status)
	}
	done := e.insertScanJob(1, "done", nil)
	if code, _ := e.admin("POST", fmt.Sprintf("/api/v1/scans/%d/cancel", done), ``); code != 204 || e.scanJobs("cancel") != 1 {
		t.Fatal("finished job got a cancel")
	}
	l := e.insertListing("lister", "queued", nil, nil)
	if code, _ := e.admin("POST", fmt.Sprintf("/api/v1/scans/%d/cancel", l), ``); code != 404 {
		t.Fatalf("listing job: %d", code)
	}
	if code, _ := e.admin("POST", "/api/v1/scans/999/cancel", ``); code != 404 {
		t.Fatalf("unknown: %d", code)
	}
}

func TestGetScanProgress(t *testing.T) {
	e := newEnv(t)
	q := e.insertScanJob(1, "queued", nil)
	_, b := e.admin("GET", fmt.Sprintf("/api/v1/scans/%d/progress", q), ``)
	if b["status"] != "queued" || b["scan_id"] != nil || b["progress"] != nil {
		t.Fatalf("queued: %v", b)
	}
	r := e.insertScanJob(1, "running", `{"phase":"walk","dirs":5}`)
	_, b = e.admin("GET", fmt.Sprintf("/api/v1/scans/%d/progress", r), ``)
	p, _ := b["progress"].(map[string]any)
	if p["phase"] != "walk" {
		t.Fatalf("running: %v", b)
	}
	f := e.insertScanJob(1, "failed", `[1]`)
	e.exec(`UPDATE jobs SET error = 'boom' WHERE id = ?`, f)
	_, b = e.admin("GET", fmt.Sprintf("/api/v1/scans/%d/progress", f), ``)
	if b["error"] != "boom" || b["progress"] != nil {
		t.Fatalf("failed: %v", b)
	}
	if code, _ := e.admin("GET", "/api/v1/scans/999/progress", ``); code != 404 {
		t.Fatal("unknown job")
	}
}

func TestListAdminScans_Paging(t *testing.T) {
	e := newEnv(t)
	e.exec(`INSERT INTO scan_roots (id, name, container_path, host_path) VALUES (2, 'r2', '/mnt/r2', '/srv/r2')`)
	for i := 0; i < 250; i++ {
		e.exec(`INSERT INTO scans (id, root_id, file, status, trigger, started_at, finished_at) VALUES (?, ?, ?, 'done', 'manual', ?, ?)`,
			100+i, 1+i%2, fmt.Sprintf("f%d.db", i), 1000+i/3, 2000)
	}
	walk := func(q string) (ids []float64) {
		cursor := ""
		for {
			url := "/api/v1/admin/scans?" + q
			if cursor != "" {
				url += "&cursor=" + cursor
			}
			code, b := e.admin("GET", url, ``)
			if code != 200 {
				t.Fatalf("%d %v", code, b)
			}
			items := b["items"].([]any)
			if len(items) > 100 {
				t.Fatalf("page of %d", len(items))
			}
			for _, it := range items {
				ids = append(ids, it.(map[string]any)["id"].(float64))
			}
			next, _ := b["next_cursor"].(string)
			if next == "" {
				return ids
			}
			cursor = next
		}
	}
	all := walk("x=1")
	seen := map[float64]bool{}
	for _, id := range all {
		if seen[id] {
			t.Fatalf("duplicate %v", id)
		}
		seen[id] = true
	}
	if len(all) != 251 {
		t.Fatalf("got %d", len(all))
	}
	if r2 := walk("root=2"); len(r2) != 125 {
		t.Fatalf("root 2: %d", len(r2))
	}
	_, b := e.admin("GET", "/api/v1/admin/scans?root=1", ``)
	cur := b["next_cursor"].(string)
	if code, _ := e.admin("GET", "/api/v1/admin/scans?root=2&cursor="+cur, ``); code != 400 {
		t.Fatalf("cross-root cursor: %d", code)
	}
}

func TestListAdminScans_Limit(t *testing.T) {
	e := newEnv(t)
	for i := 0; i < 5; i++ {
		e.exec(`INSERT INTO scans (id, root_id, file, status, trigger, started_at, finished_at) VALUES (?, 1, ?, 'done', 'manual', ?, 2000)`,
			100+i, fmt.Sprintf("f%d.db", i), 1000+i)
	}
	code, b := e.admin("GET", "/api/v1/admin/scans?limit=2", ``)
	if code != 200 || len(b["items"].([]any)) != 2 || b["next_cursor"] == nil {
		t.Fatalf("limit=2: %d %v", code, b)
	}
	for _, q := range []string{"limit=0", "limit=101"} {
		if code, _ := e.admin("GET", "/api/v1/admin/scans?"+q, ``); code != 400 {
			t.Fatalf("%s: %d", q, code)
		}
	}
}
