package web

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/vinRosso/hogwatch/internal/auth/session"
	"github.com/vinRosso/hogwatch/internal/cube"
	"github.com/vinRosso/hogwatch/internal/fname"
	"github.com/vinRosso/hogwatch/internal/noderef"
	"github.com/vinRosso/hogwatch/internal/pathx"
	"github.com/vinRosso/hogwatch/internal/repo"
	"github.com/vinRosso/hogwatch/internal/web/oapi"
)

const (
	defaultTreemapDepth = 3
	maxTreemapDepth     = 6
	errorsPageSize      = 500
	maxFilterLen        = 512
)

// scopeFor is the scope of u on scan scanID: everything for an admin, the
// user's own subtrees otherwise. A scan that cannot be served is ErrNotFound.
func (a *server) scopeFor(ctx context.Context, u session.User, scanID int64) (repo.Scope, error) {
	if u.IsAdmin() {
		return repo.ScopeAll(), nil
	}
	return a.repo.ScopeFor(ctx, u.ID, scanID)
}

// nodeScope parses ref and returns it with the caller's scope on its scan.
func (a *server) nodeScope(ctx context.Context, rawRef string) (noderef.Ref, repo.Scope, error) {
	u, ok := UserFrom(ctx)
	if !ok {
		return noderef.Ref{}, repo.Scope{}, &apiError{status: http.StatusUnauthorized}
	}
	ref, err := noderef.Parse(rawRef)
	if err != nil {
		return noderef.Ref{}, repo.Scope{}, badRequest("invalid ref")
	}
	s, err := a.scopeFor(ctx, u, ref.ScanID)
	return ref, s, err
}

// parseFilter parses the filter parameter. Go drops a query pair that holds a
// raw ";", which would silently turn a filter into no filter, so a raw ";"
// anywhere in the query is refused: clients send it as %3B.
func parseFilter(ctx context.Context, s *string) (cube.Filter, error) {
	if _, r := httpFrom(ctx); strings.Contains(r.URL.RawQuery, ";") {
		return cube.Filter{}, badRequest("encode ; as %3B")
	}
	if s == nil {
		return cube.Filter{}, nil
	}
	f, err := cube.ParseFilter(*s)
	if err != nil {
		return cube.Filter{}, badRequest("invalid filter")
	}
	return f, nil
}

func refString(r *noderef.Ref) *string {
	if r == nil {
		return nil
	}
	s := r.String()
	return &s
}

func (a *server) GetNode(ctx context.Context, req oapi.GetNodeRequestObject) (oapi.GetNodeResponseObject, error) {
	ref, s, err := a.nodeScope(ctx, req.Ref)
	if err != nil {
		return nil, err
	}
	n, err := a.repo.Node(ctx, s, ref)
	if err != nil {
		return nil, err
	}
	return oapi.GetNode200JSONResponse{
		Ref: n.Ref.String(), RootId: n.RootID, Name: fname.Escape(n.Name), RelPath: fname.Escape(n.RelPath),
		ParentRef: refString(n.ParentRef), Files: n.Files, Dirs: n.Dirs,
		BytesLogical: n.BytesLogical, BytesAlloc: n.BytesAlloc, Mtime: n.Mtime, Flags: n.Flags, ErrorCount: n.ErrorCount,
	}, nil
}

func (a *server) Resolve(ctx context.Context, req oapi.ResolveRequestObject) (oapi.ResolveResponseObject, error) {
	u, ok := UserFrom(ctx)
	if !ok {
		return nil, &apiError{status: http.StatusUnauthorized}
	}
	raw, err := fname.Unescape(req.Params.Path)
	if err != nil {
		return nil, badRequest("invalid path")
	}
	rel, err := pathx.Normalize(string(raw))
	if err != nil {
		return nil, badRequest("invalid path")
	}
	root, err := a.q.GetRoot(ctx, req.Params.Root)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return nil, repo.ErrNotFound
	case err != nil:
		return nil, err
	case !root.CurrentScanID.Valid:
		return nil, repo.ErrNotFound
	}
	scan := root.CurrentScanID.Int64
	s, err := a.scopeFor(ctx, u, scan)
	if err != nil {
		return nil, err
	}
	ref, err := a.repo.Resolve(ctx, s, scan, []byte(rel))
	if err != nil {
		return nil, err
	}
	return oapi.Resolve200JSONResponse{Ref: ref.String()}, nil
}

var childSorts = map[oapi.ListChildrenParamsSort]repo.ChildSort{
	"size": repo.SortSize, "name": repo.SortName, "mtime": repo.SortMtime, "files": repo.SortFiles,
}

func (a *server) ListChildren(ctx context.Context, req oapi.ListChildrenRequestObject) (oapi.ListChildrenResponseObject, error) {
	ref, s, err := a.nodeScope(ctx, req.Ref)
	if err != nil {
		return nil, err
	}
	sortName := oapi.ListChildrenParamsSort("size")
	if req.Params.Sort != nil {
		sortName = *req.Params.Sort
	}
	sortBy, ok := childSorts[sortName]
	if !ok {
		return nil, badRequest("invalid sort")
	}
	limit := repo.DefaultChildLimit
	if req.Params.Limit != nil {
		if limit = *req.Params.Limit; limit < 1 || limit > repo.MaxChildLimit {
			return nil, badRequest("invalid limit")
		}
	}
	filter, err := parseFilter(ctx, req.Params.Filter)
	if err != nil {
		return nil, err
	}
	q := repo.ChildQuery{Sort: sortBy, Filter: filter, Limit: limit}
	if req.Params.Cursor != nil {
		var c childCursor
		if decodeCursor(*req.Params.Cursor, &c) != nil || c.V != cursorVersion ||
			c.Scan != ref.ScanID || c.Dir != ref.DirID || c.Sort != string(sortName) || c.Filter != filter.String() ||
			len(c.Name) > 255 {
			return nil, badRequest("invalid cursor")
		}
		q.After = &repo.ChildCursor{Key: c.Key, Name: c.Name}
	}
	items, next, err := a.repo.Children(ctx, s, ref, q)
	if err != nil {
		return nil, err
	}
	page := oapi.ChildrenPage{Items: make([]oapi.Child, 0, len(items))}
	for _, c := range items {
		kind := oapi.ChildKindDir
		if c.Kind == repo.ChildFiles {
			kind = oapi.ChildKindFiles
		}
		page.Items = append(page.Items, oapi.Child{
			Name: fname.Escape(c.Name), Kind: kind, Ref: refString(c.Ref), Files: c.Files,
			BytesLogical: c.BytesLogical, BytesAlloc: c.BytesAlloc, Mtime: c.Mtime,
		})
	}
	if next != nil {
		enc := encodeCursor(childCursor{
			V: cursorVersion, Scan: ref.ScanID, Dir: ref.DirID, Sort: string(sortName), Filter: filter.String(),
			Key: next.Key, Name: next.Name,
		})
		page.NextCursor = &enc
	}
	return oapi.ListChildren200JSONResponse(page), nil
}

func treemapJSON(n repo.TreeNode) oapi.TreemapNode {
	kind := oapi.TreemapNodeKindDir
	switch n.Kind {
	case repo.TreeFiles:
		kind = oapi.TreemapNodeKindFiles
	case repo.TreeRest:
		kind = oapi.TreemapNodeKindRest
	case repo.TreeExt:
		kind = oapi.TreemapNodeKindExt
	}
	out := oapi.TreemapNode{
		Kind: kind, Ref: refString(n.Ref), Name: fname.Escape(n.Name), BytesAlloc: n.BytesAlloc, Files: n.Files,
		Children: make([]oapi.TreemapNode, 0, len(n.Children)),
	}
	for _, c := range n.Children {
		out.Children = append(out.Children, treemapJSON(c))
	}
	return out
}

func (a *server) GetTreemap(ctx context.Context, req oapi.GetTreemapRequestObject) (oapi.GetTreemapResponseObject, error) {
	ref, s, err := a.nodeScope(ctx, req.Ref)
	if err != nil {
		return nil, err
	}
	depth := defaultTreemapDepth
	if req.Params.Depth != nil {
		if depth = *req.Params.Depth; depth < 1 || depth > maxTreemapDepth {
			return nil, badRequest("invalid depth")
		}
	}
	filter, err := parseFilter(ctx, req.Params.Filter)
	if err != nil {
		return nil, err
	}
	root, err := a.repo.Treemap(ctx, s, ref, depth, filter)
	if err != nil {
		return nil, err
	}
	return oapi.GetTreemap200JSONResponse(treemapJSON(root)), nil
}

func (a *server) GetTop(ctx context.Context, req oapi.GetTopRequestObject) (oapi.GetTopResponseObject, error) {
	ref, s, err := a.nodeScope(ctx, req.Ref)
	if err != nil {
		return nil, err
	}
	kind := string(req.Params.Kind)
	if kind != "largest" && kind != "oldest" {
		return nil, badRequest("invalid kind")
	}
	scope := repo.TopSubtree
	if sc := req.Params.Scope; sc != nil {
		switch *sc {
		case oapi.GetTopParamsScopeSubtree:
		case oapi.GetTopParamsScopeDir:
			scope = repo.TopDir
		case oapi.GetTopParamsScopeBoth:
			scope = repo.TopBoth
		default:
			return nil, badRequest("invalid scope")
		}
	}
	c, err := callerOf(ctx)
	if err != nil {
		return nil, err
	}
	items, stored, err := a.repo.Top(ctx, c, s, ref, kind, scope, a.now())
	if err != nil {
		return nil, err
	}
	res := oapi.TopResult{Stored: stored}
	res.Items = make([]struct {
		Alloc  int64            `json:"alloc"`
		DirRef oapi.Ref         `json:"dir_ref"`
		Mtime  int64            `json:"mtime"`
		Name   oapi.EscapedName `json:"name"`
		Size   int64            `json:"size"`
	}, 0, len(items))
	for _, t := range items {
		res.Items = append(res.Items, struct {
			Alloc  int64            `json:"alloc"`
			DirRef oapi.Ref         `json:"dir_ref"`
			Mtime  int64            `json:"mtime"`
			Name   oapi.EscapedName `json:"name"`
			Size   int64            `json:"size"`
		}{Alloc: t.Alloc, DirRef: t.DirRef.String(), Mtime: t.Mtime, Name: fname.Escape(t.Name), Size: t.Size})
	}
	return oapi.GetTop200JSONResponse(res), nil
}

func (a *server) ListExtensions(ctx context.Context, req oapi.ListExtensionsRequestObject) (oapi.ListExtensionsResponseObject, error) {
	ref, s, err := a.nodeScope(ctx, req.Ref)
	if err != nil {
		return nil, err
	}
	exts, err := a.repo.Extensions(ctx, s, ref)
	if err != nil {
		return nil, err
	}
	res := oapi.ListExtensions200JSONResponse{Items: make([]oapi.ExtensionStat, 0, len(exts))}
	for _, x := range exts {
		res.Items = append(res.Items, oapi.ExtensionStat{
			Ext: fname.Escape([]byte(x.Ext)), Category: oapi.CategoryName(x.Category), Files: x.Files, BytesAlloc: x.Bytes,
		})
	}
	return res, nil
}

func (a *server) ListNodeErrors(ctx context.Context, req oapi.ListNodeErrorsRequestObject) (oapi.ListNodeErrorsResponseObject, error) {
	ref, s, err := a.nodeScope(ctx, req.Ref)
	if err != nil {
		return nil, err
	}
	var after int64
	if req.Params.Cursor != nil {
		var c errCursor
		if decodeCursor(*req.Params.Cursor, &c) != nil || c.V != cursorVersion ||
			c.Scan != ref.ScanID || c.Dir != ref.DirID || c.RowID < 0 {
			return nil, badRequest("invalid cursor")
		}
		after = c.RowID
	}
	items, next, err := a.repo.Errors(ctx, s, ref, after, errorsPageSize)
	if err != nil {
		return nil, err
	}
	var page oapi.ErrorPage
	page.Items = make([]struct {
		Errno int              `json:"errno"`
		Name  oapi.EscapedName `json:"name"`
	}, 0, len(items))
	for _, it := range items {
		page.Items = append(page.Items, struct {
			Errno int              `json:"errno"`
			Name  oapi.EscapedName `json:"name"`
		}{Errno: int(it.Errno), Name: fname.Escape(it.RelName)})
	}
	if next != 0 {
		enc := encodeCursor(errCursor{V: cursorVersion, Scan: ref.ScanID, Dir: ref.DirID, RowID: next})
		page.NextCursor = &enc
	}
	return oapi.ListNodeErrors200JSONResponse(page), nil
}

var historyWindows = map[oapi.GetNodeHistoryParamsWindow]time.Duration{
	"7d": 7 * 24 * time.Hour, "30d": 30 * 24 * time.Hour, "90d": 90 * 24 * time.Hour, "1y": 365 * 24 * time.Hour,
}

func (a *server) GetNodeHistory(ctx context.Context, req oapi.GetNodeHistoryRequestObject) (oapi.GetNodeHistoryResponseObject, error) {
	ref, s, err := a.nodeScope(ctx, req.Ref)
	if err != nil {
		return nil, err
	}
	window := oapi.GetNodeHistoryParamsWindow("30d")
	if req.Params.Window != nil {
		window = *req.Params.Window
	}
	var since int64
	if window != "all" {
		d, ok := historyWindows[window]
		if !ok {
			return nil, badRequest("invalid window")
		}
		since = a.now().Add(-d).Unix()
	}
	pts, err := a.repo.History(ctx, s, ref, since)
	if err != nil {
		return nil, err
	}
	res := oapi.GetNodeHistory200JSONResponse{Items: make([]oapi.HistoryPoint, 0, len(pts))}
	for _, p := range pts {
		res.Items = append(res.Items, oapi.HistoryPoint{Ts: p.TS, BytesAlloc: p.BytesAlloc, Files: p.Files})
	}
	return res, nil
}
