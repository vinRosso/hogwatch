// Package repo is the only reader of scan generation data for the web
// process. Every method that reads generation data takes a Scope, so no query
// can run without one (invariant 2). A node outside the scope and a node that
// does not exist both give ErrNotFound.
package repo

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"errors"
	"fmt"
	"log/slog"
	"sync"

	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"

	"github.com/vinRosso/hogwatch/internal/db/dbq"
	"github.com/vinRosso/hogwatch/internal/gen"
	"github.com/vinRosso/hogwatch/internal/noderef"
	"github.com/vinRosso/hogwatch/internal/pathx"
)

// ErrNotFound is returned for a node that is out of scope, does not exist, or
// belongs to a generation that can no longer be served. Callers map it to one
// 404 body, so the cases cannot be told apart.
var ErrNotFound = errors.New("repo: not found")

const maxScopeCache = 4096

type scopeKey struct {
	user, scan int64
	root       int64
	file       string // the generation file, so a reused scan id cannot share an entry
	paths      [sha256.Size]byte
}

// genEntry is a cached generation handle. refs and evicted belong to Repo.mu.
// The handle is closed when it is evicted and the last reference is released, so
// a request never sees its database closed under it.
type genEntry struct {
	db      *sql.DB
	rootID  int64
	file    string
	refs    int
	evicted bool
}

// Repo reads generation files through scopes. It is safe for concurrent use.
type Repo struct {
	db      *sql.DB
	q       *dbq.Queries
	dataDir string
	log     *slog.Logger

	mu     sync.Mutex
	gens   map[int64]*genEntry
	scopes map[scopeKey][]span
}

// New returns a Repo over the main database and the generation files below
// dataDir.
func New(db *sql.DB, dataDir string, log *slog.Logger) *Repo {
	return &Repo{
		db: db, q: dbq.New(db), dataDir: dataDir, log: log,
		gens: map[int64]*genEntry{}, scopes: map[scopeKey][]span{},
	}
}

// Close closes every cached generation. One that a request still holds closes
// when that request releases it.
func (r *Repo) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	var errs []error
	for id, e := range r.gens {
		errs = append(errs, r.drop(e))
		delete(r.gens, id)
	}
	return errors.Join(errs...)
}

// Sweep closes cached generations whose scan is no longer servable. Call it
// periodically so a deleted generation's file handle is released.
func (r *Repo) Sweep(ctx context.Context) {
	r.mu.Lock()
	ids := make([]int64, 0, len(r.gens))
	for id := range r.gens {
		ids = append(ids, id)
	}
	r.mu.Unlock()
	for _, id := range ids {
		if _, err := r.servable(ctx, id); errors.Is(err, ErrNotFound) {
			r.evict(id)
		}
	}
}

// drop marks e evicted and closes it now if nothing holds it. r.mu is held and e
// is already out of r.gens (or about to be).
func (r *Repo) drop(e *genEntry) error {
	e.evicted = true
	if e.refs > 0 {
		return nil
	}
	return e.db.Close()
}

// release gives back a reference that open returned. The last reference of an
// evicted entry closes its handle.
func (r *Repo) release(e *genEntry) {
	r.mu.Lock()
	defer r.mu.Unlock()
	e.refs--
	if e.evicted && e.refs == 0 {
		_ = e.db.Close()
	}
}

// evict removes the cached handle of scanID and its scopes. The handle closes
// once the requests using it are done.
func (r *Repo) evict(scanID int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if e, ok := r.gens[scanID]; ok {
		_ = r.drop(e)
		delete(r.gens, scanID)
	}
	for k := range r.scopes {
		if k.scan == scanID {
			delete(r.scopes, k)
		}
	}
}

// servable returns the root of scan scanID if its row is done; otherwise
// ErrNotFound. A superseded generation stays done until the scanner removes
// it, which is the grace period for open pages.
func (r *Repo) servable(ctx context.Context, scanID int64) (sc dbq.GetServableScanRow, err error) {
	row, err := r.q.GetServableScan(ctx, scanID)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return sc, ErrNotFound
	case err != nil:
		return sc, fmt.Errorf("repo: scan %d: %w", scanID, err)
	case row.Status != "done":
		return sc, ErrNotFound
	}
	return row, nil
}

// open returns the cached read-only handle of a servable scan with a reference
// held; the caller must release it.
func (r *Repo) open(ctx context.Context, scanID int64) (*genEntry, error) {
	sc, err := r.servable(ctx, scanID)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			r.evict(scanID)
		}
		return nil, err
	}
	r.mu.Lock()
	e, ok := r.gens[scanID]
	if ok && e.rootID == sc.RootID && e.file == sc.File {
		e.refs++
		r.mu.Unlock()
		return e, nil
	}
	r.mu.Unlock()
	if ok {
		// The scan id was reused for another generation.
		r.evict(scanID)
	}
	// Open outside the lock: a slow open must not block other requests.
	d, _, err := gen.OpenReadOnly(ctx, gen.Path(r.dataDir, sc.RootID, scanID))
	switch {
	case errors.Is(err, gen.ErrNotExist), errors.Is(err, gen.ErrSchemaVersion), cannotOpen(err):
		r.log.Warn("generation file is not servable", "scan", scanID, "err", err)
		return nil, ErrNotFound
	case err != nil:
		return nil, fmt.Errorf("repo: open scan %d: %w", scanID, err)
	}
	ne := &genEntry{db: d, rootID: sc.RootID, file: sc.File, refs: 1}
	r.mu.Lock()
	defer r.mu.Unlock()
	if cur, ok := r.gens[scanID]; ok && cur.rootID == ne.rootID && cur.file == ne.file {
		_ = d.Close() // another request opened it first
		cur.refs++
		return cur, nil
	}
	r.gens[scanID] = ne
	return ne, nil
}

func cannotOpen(err error) bool {
	var se *sqlite.Error
	return errors.As(err, &se) && se.Code()&0xff == sqlite3.SQLITE_CANTOPEN
}

// fix maps the errors of a generation read that mean "gone" to ErrNotFound
// and evicts the handle.
func (r *Repo) fix(scanID int64, err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, gen.ErrNoDir):
		return ErrNotFound
	case cannotOpen(err):
		r.evict(scanID)
		return ErrNotFound
	}
	return err
}

// ScopeFor builds the scope of userID on scan scanID: the subtrees of the
// user's paths that exist in that generation. A user without paths, or whose
// paths are all missing, gets a scope that denies everything. A scan that
// cannot be served gives ErrNotFound.
//
// The result is cached under (user, scan, digest of the user's path rows), so
// an edit of the paths is picked up by the next call with no invalidation.
func (r *Repo) ScopeFor(ctx context.Context, userID, scanID int64) (Scope, error) {
	e, err := r.open(ctx, scanID)
	if err != nil {
		return Scope{}, err
	}
	defer r.release(e)
	rows, err := r.q.ListUserPathsOnRoot(ctx, dbq.ListUserPathsOnRootParams{UserID: userID, RootID: e.rootID})
	if err != nil {
		return Scope{}, fmt.Errorf("repo: user paths: %w", err)
	}
	h := sha256.New()
	var n [8]byte
	for _, p := range rows {
		binary.BigEndian.PutUint64(n[:], uint64(p.ID)) //nolint:gosec // an id, never negative
		h.Write(n[:])
		binary.BigEndian.PutUint64(n[:], uint64(len(p.RelPath)))
		h.Write(n[:])
		h.Write(p.RelPath)
	}
	key := scopeKey{user: userID, scan: scanID, root: e.rootID, file: e.file}
	copy(key.paths[:], h.Sum(nil))

	r.mu.Lock()
	spans, ok := r.scopes[key]
	r.mu.Unlock()
	if ok {
		return Scope{scanID: scanID, spans: spans}, nil
	}
	var raw []span
	for _, p := range rows {
		if norm, err := pathx.Normalize(string(p.RelPath)); err != nil || norm != string(p.RelPath) {
			r.log.Warn("ignoring a non-normalised user path", "path_id", p.ID)
			continue
		}
		var segs [][]byte
		for _, s := range pathx.Segments(string(p.RelPath)) {
			segs = append(segs, []byte(s))
		}
		d, err := gen.Lookup(ctx, e.db, segs)
		if errors.Is(err, gen.ErrNoDir) {
			continue
		}
		if err := r.fix(scanID, err); err != nil {
			return Scope{}, err
		}
		raw = append(raw, span{d.Pre, d.Post})
	}
	spans = mergeSpans(raw)
	r.mu.Lock()
	if len(r.scopes) >= maxScopeCache {
		clear(r.scopes)
	}
	r.scopes[key] = spans
	r.mu.Unlock()
	return Scope{scanID: scanID, spans: spans}, nil
}

// Node is one directory as the API shows it.
type Node struct {
	Ref          noderef.Ref
	RootID       int64
	Name         []byte
	RelPath      []byte       // raw name segments from the scan root joined by "/"; empty for the root
	ParentRef    *noderef.Ref // nil for the root and when the parent is out of scope
	Files, Dirs  int64
	BytesLogical int64
	BytesAlloc   int64
	Mtime, Flags int64
	ErrorCount   int64
}

// visible reports whether directory id of the generation is inside s.
func visible(ctx context.Context, e *genEntry, s Scope, id int64) (bool, error) {
	pred, args := s.where("pre")
	var one int
	err := e.db.QueryRowContext(ctx, `SELECT 1 FROM dirs WHERE id = ? AND `+pred, append([]any{id}, args...)...).Scan(&one)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return false, nil
	case err != nil:
		return false, err
	}
	return true, nil
}

// Node returns the directory ref names, or ErrNotFound if it is outside s,
// does not exist, or its generation cannot be served.
func (r *Repo) Node(ctx context.Context, s Scope, ref noderef.Ref) (Node, error) {
	if !s.allows(ref.ScanID) {
		return Node{}, ErrNotFound
	}
	e, err := r.open(ctx, ref.ScanID)
	if err != nil {
		return Node{}, err
	}
	defer r.release(e)
	ok, err := visible(ctx, e, s, ref.DirID)
	if err != nil {
		return Node{}, r.fix(ref.ScanID, err)
	}
	if !ok {
		return Node{}, ErrNotFound
	}
	d, err := gen.DirByID(ctx, e.db, ref.DirID)
	if err != nil {
		return Node{}, r.fix(ref.ScanID, err)
	}
	n := Node{
		Ref: ref, RootID: e.rootID, Name: d.Name,
		Files: d.Files, Dirs: d.Dirs, BytesLogical: d.BytesLogical, BytesAlloc: d.BytesAlloc,
		Mtime: d.Mtime, Flags: d.Flags, ErrorCount: d.ErrorCount,
	}
	segs, err := gen.DirPath(ctx, e.db, ref.DirID)
	if err != nil {
		return Node{}, r.fix(ref.ScanID, err)
	}
	n.RelPath = bytes.Join(segs, []byte("/"))
	if d.ParentID != 0 {
		pok, err := visible(ctx, e, s, d.ParentID)
		if err != nil {
			return Node{}, r.fix(ref.ScanID, err)
		}
		if pok {
			n.ParentRef = &noderef.Ref{ScanID: ref.ScanID, DirID: d.ParentID}
		}
	}
	return n, nil
}

// Resolve maps a root-relative path (already normalised by the caller) to the
// reference of that directory in scan scanID. It matches name segments in the
// generation only; a path outside the scope is ErrNotFound, like a missing one.
func (r *Repo) Resolve(ctx context.Context, s Scope, scanID int64, rel []byte) (noderef.Ref, error) {
	if !s.allows(scanID) {
		return noderef.Ref{}, ErrNotFound
	}
	e, err := r.open(ctx, scanID)
	if err != nil {
		return noderef.Ref{}, err
	}
	defer r.release(e)
	var segs [][]byte
	if len(rel) > 0 {
		segs = bytes.Split(rel, []byte("/"))
	}
	d, err := gen.Lookup(ctx, e.db, segs)
	if err != nil {
		return noderef.Ref{}, r.fix(scanID, err)
	}
	ok, err := visible(ctx, e, s, d.ID)
	if err != nil {
		return noderef.Ref{}, r.fix(scanID, err)
	}
	if !ok {
		return noderef.Ref{}, ErrNotFound
	}
	return noderef.Ref{ScanID: scanID, DirID: d.ID}, nil
}
