package web

import (
	"fmt"
	"testing"
)

func TestCategories(t *testing.T) {
	e := newEnv(t)
	code, b := e.admin("GET", "/api/v1/admin/categories", ``)
	if code != 200 || len(b["items"].([]any)) == 0 {
		t.Fatalf("list: %d %v", code, b)
	}
	if code, b := e.admin("PUT", "/api/v1/admin/categories/mp4", `{"category":"audio"}`); code != 200 || b["category"] != "audio" {
		t.Fatalf("put: %d %v", code, b)
	}
	var cat string
	_ = e.db.QueryRow(`SELECT category FROM file_categories WHERE ext = 'mp4'`).Scan(&cat)
	if cat != "audio" || e.auditCount("category.put") != 1 {
		t.Fatalf("stored %q, audits %d", cat, e.auditCount("category.put"))
	}
	if code, _ := e.admin("DELETE", "/api/v1/admin/categories/mp4", ``); code != 204 {
		t.Fatalf("delete: %d", code)
	}
	if code, _ := e.admin("DELETE", "/api/v1/admin/categories/mp4", ``); code != 404 {
		t.Fatalf("repeat delete: %d", code)
	}
	if e.auditCount("category.deleted") != 1 {
		t.Fatal("audit")
	}
	total := e.auditTotal()
	for _, c := range []struct{ path, body string }{
		{"/api/v1/admin/categories/MP4", `{"category":"audio"}`},
		{"/api/v1/admin/categories/" + string(make([]byte, 0)) + "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", `{"category":"audio"}`},
		{"/api/v1/admin/categories/mp4", `{"category":"nope"}`},
		{"/api/v1/admin/categories/mp4", `{}`},
	} {
		if code, _ := e.admin("PUT", c.path, c.body); code != 400 {
			t.Errorf("%s %s: %d", c.path, c.body, code)
		}
	}
	if e.auditTotal() != total {
		t.Fatal("a refused request was audited")
	}
}

func TestSettings_Validation(t *testing.T) {
	e := newEnv(t)
	for _, body := range []string{`{"bogus":"1"}`, `{"top_n":"0"}`, `{"quota.include_snapshots":"yes"}`, `{"top_n":"5","bogus":"1"}`} {
		if code, _ := e.admin("PUT", "/api/v1/admin/settings", body); code != 422 {
			t.Errorf("%s: %d", body, code)
		}
	}
	var n int
	_ = e.db.QueryRow(`SELECT count(*) FROM settings`).Scan(&n)
	if n != 0 || e.auditCount("settings.updated") != 0 {
		t.Fatalf("%d rows, %d audits after refusals", n, e.auditCount("settings.updated"))
	}
}

func TestSettings_Replace(t *testing.T) {
	e := newEnv(t)
	_, b := e.admin("GET", "/api/v1/admin/settings", ``)
	if len(b) != 5 || b["top_n"] != "50" || b["ui.users_see_mount_capacity"] != "true" {
		t.Fatalf("defaults: %v", b)
	}
	e.exec(`INSERT INTO settings (key, value) VALUES ('top_max_depth', '9')`)
	code, b := e.admin("PUT", "/api/v1/admin/settings", `{"top_n":"100","ui.users_see_mount_capacity":"false"}`)
	if code != 200 || b["top_n"] != "100" || b["top_max_depth"] != "5" {
		t.Fatalf("put: %d %v", code, b)
	}
	var n int
	_ = e.db.QueryRow(`SELECT count(*) FROM settings WHERE key = 'top_max_depth'`).Scan(&n)
	if n != 0 {
		t.Fatal("an absent key was kept")
	}
	d := e.auditDetails("settings.updated")
	for _, want := range []string{`"top_n":["50","100"]`, `"top_max_depth":["9","5"]`, `"ui.users_see_mount_capacity":["true","false"]`} {
		if !contains(d, want) {
			t.Errorf("audit details %s lack %s", d, want)
		}
	}
	if contains(d, "quota.include_snapshots") {
		t.Errorf("unchanged key audited: %s", d)
	}
	// A no-op replace writes no audit row.
	before := e.auditCount("settings.updated")
	e.admin("PUT", "/api/v1/admin/settings", `{"top_n":"100","ui.users_see_mount_capacity":"false"}`)
	if e.auditCount("settings.updated") != before {
		t.Fatal("a no-op was audited")
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

func TestListAudit_Paging(t *testing.T) {
	e := newEnv(t)
	for i := 0; i < 250; i++ {
		e.exec(`INSERT INTO audit_log (ts, actor_id, action, target, details) VALUES (?, ?, 'x.y', ?, '{"i":1}')`,
			int64(i), nilIf(i%2 == 0, e.users["admin"]), fmt.Sprint(i))
	}
	var ids []float64
	cursor := ""
	for {
		url := "/api/v1/admin/audit?x=1"
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
			m := it.(map[string]any)
			if _, ok := m["details"].(map[string]any); !ok {
				t.Fatalf("details: %v", m)
			}
			ids = append(ids, m["id"].(float64))
		}
		next, _ := b["next_cursor"].(string)
		if next == "" {
			break
		}
		cursor = next
	}
	if len(ids) < 250 {
		t.Fatalf("%d entries", len(ids))
	}
	for i := 1; i < len(ids); i++ {
		if ids[i] >= ids[i-1] {
			t.Fatalf("not newest first at %d", i)
		}
	}
	_, b := e.admin("GET", "/api/v1/admin/audit", ``)
	var sawNull bool
	for _, it := range b["items"].([]any) {
		if it.(map[string]any)["actor_id"] == nil {
			sawNull = true
		}
	}
	if !sawNull {
		t.Fatal("null actor not preserved")
	}
}

func nilIf(cond bool, v int64) any {
	if cond {
		return nil
	}
	return v
}
