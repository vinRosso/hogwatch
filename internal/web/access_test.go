package web

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/vinRosso/hogwatch/api"
)

var paramRE = regexp.MustCompile(`\{([^}]+)\}`)

func samplePath(p string) string {
	return paramRE.ReplaceAllStringFunc(p, func(m string) string {
		switch m {
		case "{ref}":
			return "1.1"
		case "{ext}":
			return "mp4"
		}
		return "1"
	})
}

// specOps lists every operation of the contract as (METHOD, spec path).
func specOps(t testing.TB) [][2]string {
	t.Helper()
	var doc struct {
		Paths map[string]map[string]any `yaml:"paths"`
	}
	if err := yaml.Unmarshal(api.Spec, &doc); err != nil {
		t.Fatal(err)
	}
	var out [][2]string
	for p, item := range doc.Paths {
		for m := range item {
			if m == "parameters" || m == "summary" || m == "description" {
				continue
			}
			out = append(out, [2]string{strings.ToUpper(m), p})
		}
	}
	return out
}

func TestAccessTable_CoversMux(t *testing.T) {
	e := newEnv(t)
	ops := specOps(t)
	if len(ops) < 50 {
		t.Fatalf("only %d operations", len(ops))
	}
	for _, op := range ops {
		key := op[0] + " " + op[1]
		if _, ok := e.table[key]; !ok {
			t.Errorf("%s: no access mark", key)
		}
		r := httptest.NewRequest(op[0], samplePath(op[1]), nil)
		_, pattern := e.mux.Handler(r)
		if pattern != key {
			t.Errorf("%s routes to pattern %q", key, pattern)
		}
	}
}

func TestAccessTable_MissingMarkFailsStartup(t *testing.T) {
	broken := strings.Replace(string(api.Spec), "x-hogwatch-access: admin", "x-hogwatch-note: admin", 1)
	if broken == string(api.Spec) {
		t.Fatal("test spec was not changed")
	}
	if _, err := AccessTable([]byte(broken)); err == nil {
		t.Fatal("AccessTable accepted an operation without a mark")
	}
	bad := strings.Replace(string(api.Spec), "x-hogwatch-access: public", "x-hogwatch-access: everyone", 1)
	if _, err := AccessTable([]byte(bad)); err == nil {
		t.Fatal("AccessTable accepted an unknown mark")
	}
	if _, err := AccessTable(api.Spec); err != nil {
		t.Fatal(err)
	}
}

// A route the generated code registers but the table lacks stops startup; the
// request-time refusal in checkAccess is only the second line of defence.
func TestBuild_RouteWithoutMarkFailsStartup(t *testing.T) {
	e := newEnv(t)
	d := Deps{
		DB: e.db, Cfg: e.cfg, Sessions: e.sessions, Repo: e.repo, Limiter: e.limiter,
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Now: func() time.Time { return e.now },
	}
	full := map[string]Access{}
	for k, v := range e.table {
		full[k] = v
	}
	if _, _, _, err := assemble(d, full); err != nil {
		t.Fatalf("complete table: %v", err)
	}
	const missing = "GET /api/v1/me"
	delete(full, missing)
	_, _, _, err := assemble(d, full)
	if err == nil || !strings.Contains(err.Error(), missing) {
		t.Fatalf("assemble with %q removed = %v, want an error naming it", missing, err)
	}
}

func TestAccessTable_Rules(t *testing.T) {
	e := newEnv(t)
	for key, want := range map[string]Access{
		"POST /api/v1/auth/login":      AccessPublic,
		"GET /api/v1/me":               AccessUser,
		"GET /api/v1/nodes/{ref}":      AccessScoped,
		"POST /api/v1/listings":        AccessListing,
		"GET /api/v1/admin/users":      AccessAdmin,
		"GET /healthz":                 AccessPublic,
		"GET /api/v1/setup/status":     AccessPublic,
		"DELETE /api/v1/listings/{id}": AccessListing,
	} {
		if e.table[key] != want {
			t.Errorf("%s: %v, want %v", key, e.table[key], want)
		}
	}
}

func TestUnknownAPIPath_ProblemNotFound(t *testing.T) {
	e := newEnv(t)
	w := e.do(req{method: "GET", path: "/api/v1/nothing", role: "admin"})
	if w.Code != http.StatusNotFound || w.Header().Get("Content-Type") != "application/problem+json" {
		t.Fatalf("%d %s", w.Code, w.Header().Get("Content-Type"))
	}
}
