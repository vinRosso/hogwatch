package web

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strings"

	"github.com/vinRosso/hogwatch/internal/auth/session"
)

// maxBody caps every request body.
const maxBody = 64 << 10

// csrfHeader must be present, with value "1", on every state-changing /api
// request. A cross-site form or fetch without CORS approval cannot set it.
const (
	csrfHeader = "X-Hogwatch-CSRF"
	csrfValue  = "1"
)

type ctxKey int

const userKey ctxKey = iota

// UserFrom returns the logged-in user of the request, if any.
func UserFrom(ctx context.Context) (session.User, bool) {
	u, ok := ctx.Value(userKey).(session.User)
	return u, ok
}

const csp = "default-src 'self'; img-src 'self' data:; style-src 'self' 'unsafe-inline'; " +
	"base-uri 'none'; form-action 'self'; frame-ancestors 'none'"

func (a *server) recoverer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if v := recover(); v != nil {
				if v == http.ErrAbortHandler { //nolint:errorlint // the sentinel is compared by identity
					panic(v)
				}
				a.log.Error("panic in handler", "method", r.Method, "path", r.URL.Path, "panic", v)
				writeProblem(w, http.StatusInternalServerError, "")
			}
		}()
		next.ServeHTTP(w, r)
	})
}

func headers(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "same-origin")
		h.Set("Content-Security-Policy", csp)
		if strings.HasPrefix(r.URL.Path, "/api/") {
			h.Set("Cache-Control", "no-store")
		}
		next.ServeHTTP(w, r)
	})
}

func crossOrigin(next http.Handler) http.Handler {
	cop := http.NewCrossOriginProtection()
	cop.SetDenyHandler(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeProblem(w, http.StatusForbidden, "")
	}))
	return cop.Handler(next)
}

func stateChanging(method string) bool {
	switch method {
	case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
		return true
	}
	return false
}

// csrf requires the custom header and, when there is a body, a JSON content
// type. Both checks run before any handler or row write.
func csrf(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if stateChanging(r.Method) && strings.HasPrefix(r.URL.Path, "/api/") {
			if r.Header.Get(csrfHeader) != csrfValue {
				writeProblem(w, http.StatusForbidden, "")
				return
			}
			if r.ContentLength != 0 {
				mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
				if err != nil || mt != "application/json" {
					writeProblem(w, http.StatusForbidden, "")
					return
				}
			}
		}
		next.ServeHTTP(w, r)
	})
}

func bodyLimit(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.ContentLength > maxBody {
			writeProblem(w, http.StatusRequestEntityTooLarge, "")
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, maxBody)
		next.ServeHTTP(w, r)
	})
}

type patchKeysKey struct{}

// patchKeys reads a PATCH body once and exposes its top-level keys, so a
// handler can tell an absent field from an explicit null (the generated types
// cannot). The body is restored for the generated decoder.
func patchKeys(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPatch && r.Body != nil && r.Body != http.NoBody {
			b, err := io.ReadAll(r.Body)
			if err != nil {
				writeProblem(w, http.StatusRequestEntityTooLarge, "")
				return
			}
			r.Body = io.NopCloser(bytes.NewReader(b))
			var raw map[string]json.RawMessage
			if json.Unmarshal(b, &raw) == nil {
				// encoding/json matches keys without regard to case, so the
				// null-vs-absent lookup must too. Two spellings of one key are
				// ambiguous.
				keys := make(map[string]json.RawMessage, len(raw))
				for k, v := range raw {
					lk := strings.ToLower(k)
					if _, dup := keys[lk]; dup {
						writeProblem(w, http.StatusBadRequest, "duplicate key "+lk)
						return
					}
					keys[lk] = v
				}
				r = r.WithContext(context.WithValue(r.Context(), patchKeysKey{}, keys))
			}
		}
		next.ServeHTTP(w, r)
	})
}

// patchField reports whether the PATCH body named key, and whether it was null.
func patchField(ctx context.Context, key string) (present, null bool) {
	_, r := httpFrom(ctx)
	keys, _ := r.Context().Value(patchKeysKey{}).(map[string]json.RawMessage)
	raw, ok := keys[key]
	return ok, ok && string(bytes.TrimSpace(raw)) == "null"
}

func hasSessionCookie(r *http.Request) bool {
	for _, c := range r.Cookies() {
		if c.Name == session.CookieName {
			return true
		}
	}
	return false
}

// loadSession attaches the user of a valid session cookie. A cookie that is
// malformed, duplicated, expired or revoked is cleared and the request goes on
// anonymously; the access check then decides.
func (a *server) loadSession(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !hasSessionCookie(r) || r.URL.Path == "/healthz" || r.URL.Path == "/readyz" {
			next.ServeHTTP(w, r)
			return
		}
		token, ok := session.TokenFrom(r)
		if !ok {
			session.ClearCookie(w)
			next.ServeHTTP(w, r)
			return
		}
		u, err := a.sessions.Lookup(r.Context(), token)
		switch {
		case errors.Is(err, session.ErrNoSession):
			session.ClearCookie(w)
		case err != nil:
			a.log.Error("session lookup", "err", err)
			writeProblem(w, http.StatusInternalServerError, "")
			return
		default:
			r = r.WithContext(context.WithValue(r.Context(), userKey, u))
		}
		next.ServeHTTP(w, r)
	})
}

// checkAccess enforces the x-hogwatch-access mark of the route the request
// matches, before any parameter or body is read. A matched route without a
// mark is refused: the table is the only way in.
func (a *server) checkAccess(mux *http.ServeMux, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, pattern := mux.Handler(r)
		if pattern == "" || pattern == "/" || pattern == "/api/" {
			next.ServeHTTP(w, r) // the mux's own 404/405/redirect, the SPA, the API 404
			return
		}
		acc, ok := a.access[pattern]
		if !ok {
			a.log.Error("route without access mark", "pattern", pattern)
			writeProblem(w, http.StatusInternalServerError, "")
			return
		}
		if acc != AccessPublic {
			u, authed := UserFrom(r.Context())
			switch {
			case !authed:
				writeProblem(w, http.StatusUnauthorized, "")
				return
			case acc == AccessListing && !u.CanListFiles && !u.IsAdmin():
				writeProblem(w, http.StatusForbidden, "")
				return
			case acc == AccessAdmin && !u.IsAdmin():
				writeProblem(w, http.StatusForbidden, "")
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}
