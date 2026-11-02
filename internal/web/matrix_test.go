package web

import (
	"net/http"
	"strings"
	"testing"
)

// publicMayReject lists public operations that legitimately answer 401 or
// 403 themselves (bad credentials, bad setup token).
var publicMayReject = map[string]bool{"POST /api/v1/auth/login": true, "POST /api/v1/setup": true}

var queryFor = map[string]string{
	"/api/v1/resolve":         "?root=1&path=a",
	"/api/v1/nodes/{ref}/top": "?kind=largest",
	"/api/v1/export/{ref}":    "?format=csv&what=tree",
}

// TestMatrix runs every operation of the contract as every role and checks
// the access rule of its mark. The operations come from the spec, so a new
// endpoint is covered the moment it exists.
func TestMatrix(t *testing.T) {
	for _, op := range specOps(t) {
		// A fresh environment per operation: logout and password change revoke sessions.
		e := newEnv(t)
		method, specPath := op[0], op[1]
		key := method + " " + specPath
		acc := e.table[key]
		path := samplePath(specPath) + queryFor[specPath]
		for _, role := range roles {
			body := ""
			if stateChanging(method) && strings.HasPrefix(specPath, "/api/") && !strings.HasSuffix(specPath, "/logout") {
				body = `{}`
			}
			w := e.do(req{method: method, path: path, role: role, body: body})
			code := w.Code
			is401, is403 := code == http.StatusUnauthorized, code == http.StatusForbidden
			var want401, want403 bool
			switch acc {
			case AccessPublic:
				// 401/403 only from operations that judge credentials themselves.
				if publicMayReject[key] {
					continue
				}
			case AccessUser, AccessScoped:
				want401 = role == "anon" || role == "disabled"
			case AccessListing:
				want401 = role == "anon" || role == "disabled"
				want403 = role == "plain" || role == "owner"
			case AccessAdmin:
				want401 = role == "anon" || role == "disabled"
				want403 = role != "admin" && !want401
			}
			if is401 != want401 || is403 != want403 {
				t.Errorf("%s as %s (%v): status %d, want 401=%v 403=%v", key, role, acc, code, want401, want403)
			}
			// Authorization runs before the body is parsed: a denied role never sees a 400.
			if (want401 || want403) && body != "" {
				if w2 := e.do(req{method: method, path: path, role: role, body: `{bad`}); w2.Code != code {
					t.Errorf("%s as %s: malformed body gave %d, want %d", key, role, w2.Code, code)
				}
			}
			if code == http.StatusInternalServerError {
				t.Errorf("%s as %s: 500", key, role)
			}
		}
	}
}
