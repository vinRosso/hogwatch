package web

import (
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"net/netip"
	"time"

	"github.com/vinRosso/hogwatch/api"
	"github.com/vinRosso/hogwatch/internal/auth/local"
	"github.com/vinRosso/hogwatch/internal/auth/oidc"
	"github.com/vinRosso/hogwatch/internal/auth/session"
	"github.com/vinRosso/hogwatch/internal/config"
	"github.com/vinRosso/hogwatch/internal/db/dbq"
	"github.com/vinRosso/hogwatch/internal/repo"
	"github.com/vinRosso/hogwatch/internal/web/oapi"
)

// Deps are everything the HTTP layer needs.
type Deps struct {
	DB       *sql.DB
	Cfg      config.Config
	Sessions *session.Store
	Repo     *repo.Repo
	Limiter  *local.Limiter
	Setup    *local.SetupToken
	OIDC     *oidc.Client // nil when OIDC is not configured
	Flows    *oidc.Flows  // pending OIDC logins; nil with OIDC
	SPA      fs.FS        // nil serves no SPA
	Log      *slog.Logger
	Now      func() time.Time

	afterCompute func() // test seam: runs between the usage recompute and its transaction
}

// server implements oapi.StrictServerInterface.
type server struct {
	db       *sql.DB
	q        *dbq.Queries
	cfg      config.Config
	sessions *session.Store
	repo     *repo.Repo
	limiter  *local.Limiter
	setup    *local.SetupToken
	oidc     *oidc.Client
	flows    *oidc.Flows
	log      *slog.Logger
	now      func() time.Time
	trusted  []netip.Prefix
	access   map[string]Access
	exports  exportSlots

	afterCompute func()
}

// New builds the HTTP handler: the generated routes and the SPA behind the
// middleware chain recover, headers, cross-origin check, CSRF check, body cap,
// session loader and access check. It fails if any operation of the contract
// lacks an access mark.
func New(d Deps) (http.Handler, error) {
	h, _, _, err := build(d)
	return h, err
}

// build is New plus the mux and the access table, which tests inspect.
func build(d Deps) (http.Handler, *http.ServeMux, map[string]Access, error) {
	table, err := AccessTable(api.Spec)
	if err != nil {
		return nil, nil, nil, err
	}
	return assemble(d, table)
}

// routeRecorder is the BaseRouter of the generated routes: it remembers every
// pattern they register.
type routeRecorder struct {
	*http.ServeMux
	patterns []string
}

func (r *routeRecorder) HandleFunc(pattern string, h func(http.ResponseWriter, *http.Request)) {
	r.patterns = append(r.patterns, pattern)
	r.ServeMux.HandleFunc(pattern, h)
}

// assemble builds the handler over an access table. It fails if a generated
// route is missing from the table, so a stale table stops startup rather than
// answering 500 at request time.
func assemble(d Deps, table map[string]Access) (http.Handler, *http.ServeMux, map[string]Access, error) {
	trusted, err := parsePrefixes(d.Cfg.TrustedProxies)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("web: trusted_proxies: %w", err)
	}
	if d.DB == nil || d.Sessions == nil || d.Log == nil || d.Now == nil {
		return nil, nil, nil, errors.New("web: incomplete dependencies")
	}
	a := &server{
		db: d.DB, q: dbq.New(d.DB), cfg: d.Cfg, sessions: d.Sessions, repo: d.Repo,
		limiter: d.Limiter, setup: d.Setup, oidc: d.OIDC, flows: d.Flows, log: d.Log, now: d.Now, trusted: trusted, access: table, afterCompute: d.afterCompute,
	}

	mux := http.NewServeMux()
	strict := oapi.NewStrictHandlerWithOptions(a, []oapi.StrictMiddlewareFunc{withHTTP}, oapi.StrictHTTPServerOptions{
		RequestErrorHandlerFunc:  requestError,
		ResponseErrorHandlerFunc: a.responseError,
	})
	rec := &routeRecorder{ServeMux: mux}
	oapi.HandlerWithOptions(strict, oapi.StdHTTPServerOptions{BaseRouter: rec, ErrorHandlerFunc: requestError})
	for _, p := range rec.patterns {
		if _, ok := table[p]; !ok {
			return nil, nil, nil, fmt.Errorf("web: route %q has no access mark", p)
		}
	}
	mux.HandleFunc("/api/", func(w http.ResponseWriter, _ *http.Request) { writeProblem(w, http.StatusNotFound, "") })
	if d.SPA != nil {
		mux.Handle("/", SPA(d.SPA))
	}

	var h http.Handler = mux
	h = a.checkAccess(mux, h)
	h = a.loadSession(h)
	h = patchKeys(h)
	h = bodyLimit(h)
	h = csrf(h)
	h = crossOrigin(h)
	h = headers(h)
	h = a.recoverer(h)
	return h, mux, table, nil
}
