package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

func testFS() fstest.MapFS {
	return fstest.MapFS{
		"index.html":           {Data: []byte(`<div id="app"></div>`)},
		"assets/app-abc123.js": {Data: []byte("x")},
	}
}

func do(t *testing.T, method, path string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	SPA(testFS()).ServeHTTP(rec, httptest.NewRequest(method, path, nil))
	return rec
}

func TestSPA_Root(t *testing.T) {
	rec := do(t, http.MethodGet, "/")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `id="app"`) {
		t.Fatalf("got %d %q", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-cache" {
		t.Errorf("Cache-Control = %q", got)
	}
}

func TestSPA_ClientRouteFallsBackToIndex(t *testing.T) {
	rec := do(t, http.MethodGet, "/nodes/12.34")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `id="app"`) {
		t.Fatalf("got %d %q", rec.Code, rec.Body.String())
	}
}

func TestSPA_Asset(t *testing.T) {
	rec := do(t, http.MethodGet, "/assets/app-abc123.js")
	if rec.Code != 200 || rec.Body.String() != "x" {
		t.Fatalf("got %d %q", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Cache-Control"); got != "public, max-age=31536000, immutable" {
		t.Errorf("Cache-Control = %q", got)
	}
}

func TestSPA_MissingAssetIs404(t *testing.T) {
	if rec := do(t, http.MethodGet, "/assets/nope.js"); rec.Code != 404 {
		t.Fatalf("code = %d", rec.Code)
	}
}

func TestSPA_APIPrefixReserved(t *testing.T) {
	for _, p := range []string{"/api", "/api/v1/x"} {
		rec := do(t, http.MethodGet, p)
		if rec.Code != 404 || strings.Contains(rec.Body.String(), `id="app"`) {
			t.Errorf("%s: got %d %q", p, rec.Code, rec.Body.String())
		}
	}
}

func TestSPA_MethodNotAllowed(t *testing.T) {
	rec := do(t, http.MethodPost, "/")
	if rec.Code != 405 || rec.Header().Get("Allow") != "GET, HEAD" {
		t.Fatalf("got %d Allow=%q", rec.Code, rec.Header().Get("Allow"))
	}
}
