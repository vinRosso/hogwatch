package web

import (
	"bytes"
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func exportGet(e *env, role, ref, q string) *httptest.ResponseRecorder {
	return e.do(req{method: "GET", path: "/api/v1/export/" + ref + "?" + q, role: role})
}

func csvRows(t *testing.T, w *httptest.ResponseRecorder) [][]string {
	t.Helper()
	if w.Code != 200 {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	rows, err := csv.NewReader(w.Body).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	return rows
}

func TestExport_TreeCSV(t *testing.T) {
	e := newEnv(t)
	w := exportGet(e, "admin", "1.1", "format=csv&what=tree")
	rows := csvRows(t, w)
	if cd := w.Header().Get("Content-Disposition"); cd != `attachment; filename="hogwatch-1.1-tree.csv"` {
		t.Errorf("Content-Disposition = %q", cd)
	}
	if !strings.HasPrefix(w.Header().Get("Content-Type"), "text/csv") {
		t.Errorf("Content-Type = %q", w.Header().Get("Content-Type"))
	}
	var paths []string
	for _, r := range rows[1:] {
		paths = append(paths, r[0])
	}
	if strings.Join(paths, "|") != "|a|a/b|d" || rows[0][0] != "rel_path" {
		t.Errorf("rows = %v", rows)
	}
	owner := csvRows(t, exportGet(e, "owner", "1.2", "format=csv&what=tree"))
	if len(owner) != 3 || owner[1][0] != "" || owner[2][0] != "b" {
		t.Errorf("owner rows = %v", owner)
	}
}

func TestExport_TopJSONAndUsage(t *testing.T) {
	e := newEnv(t)
	seedStats(e)
	w := exportGet(e, "owner", "1.2", "format=json&what=top")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"rel_path":"b/big.bin"`) || !strings.Contains(w.Header().Get("Content-Type"), "application/json") {
		t.Errorf("top json = %d %s", w.Code, w.Body)
	}
	rows := csvRows(t, exportGet(e, "owner", "1.2", "format=csv&what=usage"))
	if len(rows) != 2 || rows[1][0] != "owner" || rows[1][1] != "/srv/r/a" {
		t.Errorf("owner usage = %v", rows)
	}
	if rows = csvRows(t, exportGet(e, "admin", "1.1", "format=csv&what=usage")); len(rows) != 3 {
		t.Errorf("admin usage = %v", rows)
	}
}

func TestExport_Validation(t *testing.T) {
	e := newEnv(t)
	for _, q := range []string{"format=xml&what=tree", "format=csv&what=nope"} {
		if w := exportGet(e, "admin", "1.1", q); w.Code != 400 {
			t.Errorf("%s = %d", q, w.Code)
		}
	}
	if w := exportGet(e, "admin", "x", "format=csv&what=tree"); w.Code != 400 {
		t.Errorf("bad ref = %d", w.Code)
	}
}

func TestExport_OutOfScope(t *testing.T) {
	e := newEnv(t)
	for _, what := range []string{"tree", "top", "usage"} {
		w := exportGet(e, "owner", "1.4", "format=csv&what="+what)
		miss := exportGet(e, "owner", "1.999", "format=csv&what="+what)
		if w.Code != 404 || w.Body.String() != miss.Body.String() || strings.Contains(w.Body.String(), "rel_path") {
			t.Errorf("%s: %d %q vs %d %q", what, w.Code, w.Body, miss.Code, miss.Body)
		}
	}
}

func TestExportSlots(t *testing.T) {
	var s exportSlots
	first, second, third := s.acquire(1), s.acquire(1), s.acquire(1)
	if !first || !second || third {
		t.Fatalf("limit of 2 per user not enforced: %v %v %v", first, second, third)
	}
	if !s.acquire(2) {
		t.Error("another user was blocked")
	}
	s.release(1)
	if !s.acquire(1) {
		t.Error("a released slot was not reusable")
	}
}

func TestExport_ReleasesSlot(t *testing.T) {
	e := newEnv(t)
	for i := 0; i < 5; i++ {
		if w := exportGet(e, "admin", "1.1", "format=csv&what=tree"); w.Code != 200 {
			t.Fatalf("export %d = %d: slot leaked", i, w.Code)
		}
	}
	if w := exportGet(e, "admin", "1.999", "format=csv&what=tree"); w.Code != 404 {
		t.Fatal(w.Code)
	}
	for i := 0; i < 3; i++ {
		if w := exportGet(e, "admin", "1.1", "format=csv&what=tree"); w.Code != 200 {
			t.Fatalf("after a 404, export %d = %d: slot leaked", i, w.Code)
		}
	}
}

func TestExport_AbortMidStream(t *testing.T) {
	released := false
	resp := exportResponse{
		filename: "x.csv", contentType: "text/csv", release: func() { released = true },
		run: func(w io.Writer) error { _, _ = io.WriteString(w, "a,b\n"); return errors.New("disk gone") },
	}
	defer func() {
		if r := recover(); r != http.ErrAbortHandler {
			t.Errorf("recover() = %v, want http.ErrAbortHandler", r)
		}
		if !released {
			t.Error("slot not released after abort")
		}
	}()
	_ = resp.VisitExportNodeResponse(httptest.NewRecorder())
	t.Error("a failed stream returned normally")
}

type deadlineRecorder struct {
	*httptest.ResponseRecorder
	deadlines int
}

func (d *deadlineRecorder) SetWriteDeadline(time.Time) error { d.deadlines++; return nil }

func TestExport_ArmsWriteDeadlinePerWrite(t *testing.T) {
	dw := &deadlineRecorder{ResponseRecorder: httptest.NewRecorder()}
	resp := exportResponse{
		filename: "x.csv", contentType: "text/csv", release: func() {},
		run: func(w io.Writer) error {
			for i := 0; i < 3; i++ {
				_, _ = io.WriteString(w, "a\n")
			}
			return nil
		},
	}
	if err := resp.VisitExportNodeResponse(dw); err != nil {
		t.Fatal(err)
	}
	if dw.deadlines < 3 {
		t.Errorf("write deadline armed %d times for 3 writes", dw.deadlines)
	}
}

func TestResponseError_CanceledIsNot500(t *testing.T) {
	var buf bytes.Buffer
	e := newEnv(t, func(d *Deps) { d.Log = slog.New(slog.NewTextHandler(&buf, nil)) })
	a := &server{log: slog.New(slog.NewTextHandler(&buf, nil))}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r := httptest.NewRequest("GET", "/api/v1/nodes/1.1/children", nil).WithContext(ctx)
	w := httptest.NewRecorder()
	a.responseError(w, r, fmt.Errorf("repo: children: %w", ctx.Err()))
	if strings.Contains(buf.String(), "request failed") {
		t.Errorf("a cancelled request was logged as a failure: %s", buf.String())
	}
	_ = e
}

func TestExport_CheckKeepsNoSlot(t *testing.T) {
	e := newEnv(t)
	for i := 0; i < 5; i++ {
		if w := exportGet(e, "admin", "1.1", "format=csv&what=tree&check=true"); w.Code != http.StatusNoContent || w.Body.Len() != 0 {
			t.Fatalf("check %d = %d with %d body bytes", i, w.Code, w.Body.Len())
		}
	}
	// A check must not leak a slot: real exports still run afterwards.
	for i := 0; i < 3; i++ {
		if w := exportGet(e, "admin", "1.1", "format=csv&what=tree"); w.Code != 200 {
			t.Fatalf("export %d after checks = %d", i, w.Code)
		}
	}
	// A check still answers for scope, and for a node that does not exist.
	if w := exportGet(e, "admin", "1.999", "format=csv&what=tree&check=true"); w.Code != 404 {
		t.Fatalf("missing-node check = %d, want 404", w.Code)
	}
	if w := exportGet(e, "owner", "1.999", "format=csv&what=tree&check=true"); w.Code != 404 {
		t.Fatalf("out-of-scope check = %d, want 404", w.Code)
	}
}
