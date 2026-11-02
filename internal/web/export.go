package web

import (
	"context"
	"io"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/vinRosso/hogwatch/internal/export"
	"github.com/vinRosso/hogwatch/internal/fname"
	"github.com/vinRosso/hogwatch/internal/repo"
	"github.com/vinRosso/hogwatch/internal/web/oapi"
)

const (
	maxConcurrentExports = 2
	exportRetryAfter     = 5 * time.Second
)

// exportSlots counts the exports running per user. The zero value is ready.
type exportSlots struct {
	mu sync.Mutex
	n  map[int64]int
}

func (x *exportSlots) acquire(user int64) bool {
	x.mu.Lock()
	defer x.mu.Unlock()
	if x.n == nil {
		x.n = map[int64]int{}
	}
	if x.n[user] >= maxConcurrentExports {
		return false
	}
	x.n[user]++
	return true
}

func (x *exportSlots) release(user int64) {
	x.mu.Lock()
	defer x.mu.Unlock()
	if x.n[user]--; x.n[user] <= 0 {
		delete(x.n, user)
	}
}

// exportResponse streams an export. Everything that can fail with a 404 has
// happened before it is built; run only writes. A write failure after the
// first byte aborts the connection, so the client sees a broken transfer and
// never a short file that looks complete.
type exportResponse struct {
	filename    string
	contentType string
	run         func(io.Writer) error
	release     func()
}

func (e exportResponse) VisitExportNodeResponse(w http.ResponseWriter) error {
	defer e.release()
	w.Header().Set("Content-Type", e.contentType)
	w.Header().Set("Content-Disposition", `attachment; filename="`+e.filename+`"`)
	w.WriteHeader(http.StatusOK)
	if err := e.run(&deadlineWriter{w: w, rc: http.NewResponseController(w)}); err != nil {
		panic(http.ErrAbortHandler)
	}
	return nil
}

// rowWriter hides the difference between CSV and JSON for the three exports.
type rowWriter struct {
	add   func(csvCells []string, obj map[string]any) error
	close func() error
}

func newRowWriter(w io.Writer, format oapi.ExportNodeParamsFormat, header []string) (rowWriter, error) {
	if format == oapi.Csv {
		c := export.NewCSV(w)
		if err := c.Write(header...); err != nil {
			return rowWriter{}, err
		}
		return rowWriter{
			add:   func(cells []string, _ map[string]any) error { return c.Write(cells...) },
			close: c.Flush,
		}, nil
	}
	j := export.NewJSONArray(w)
	return rowWriter{add: func(_ []string, obj map[string]any) error { return j.Add(obj) }, close: j.Close}, nil
}

func i64(v int64) string { return strconv.FormatInt(v, 10) }

func boolStr(b bool) string { return strconv.FormatBool(b) }

func (a *server) ExportNode(ctx context.Context, req oapi.ExportNodeRequestObject) (oapi.ExportNodeResponseObject, error) {
	u, ok := UserFrom(ctx)
	if !ok {
		return nil, &apiError{status: http.StatusUnauthorized}
	}
	format, what := req.Params.Format, req.Params.What
	if !format.Valid() || !what.Valid() {
		return nil, badRequest("invalid format or what")
	}
	ref, s, err := a.nodeScope(ctx, req.Ref)
	if err != nil {
		return nil, err
	}
	if req.Params.Check != nil && *req.Params.Check {
		// Only what a refusal depends on: the node is in scope and exists, and a slot is
		// free. The slot is taken and given back inside this request, so the download that
		// follows cannot be crowded out by it, and none of the export's work is done twice.
		if _, err := a.repo.Node(ctx, s, ref); err != nil {
			return nil, err
		}
		if !a.exports.acquire(u.ID) {
			return nil, &apiError{status: http.StatusTooManyRequests, detail: "too many exports", retryAfter: exportRetryAfter}
		}
		a.exports.release(u.ID)
		return oapi.ExportNode204Response{}, nil
	}
	if !a.exports.acquire(u.ID) {
		return nil, &apiError{status: http.StatusTooManyRequests, detail: "too many exports", retryAfter: exportRetryAfter}
	}
	var once sync.Once
	release := func() { once.Do(func() { a.exports.release(u.ID) }) }
	ext := "csv"
	ctype := "text/csv; charset=utf-8"
	if format == oapi.Json {
		ext, ctype = "json", "application/json"
	}
	resp := exportResponse{
		filename: "hogwatch-" + ref.String() + "-" + string(what) + "." + ext, contentType: ctype, release: release,
	}
	switch what {
	case oapi.ExportNodeParamsWhatTree:
		st, err := a.repo.ExportTree(ctx, s, ref)
		if err != nil {
			release()
			return nil, err
		}
		resp.release = func() { st.Close(); release() }
		resp.run = func(w io.Writer) error {
			rw, err := newRowWriter(w, format, []string{"rel_path", "files", "dirs", "bytes_logical", "bytes_alloc", "mtime"})
			if err != nil {
				return err
			}
			if err := st.Each(func(r repo.TreeRow) error {
				p := string(fname.Escape(r.RelPath))
				return rw.add([]string{p, i64(r.Files), i64(r.Dirs), i64(r.BytesLogical), i64(r.BytesAlloc), i64(r.Mtime)},
					map[string]any{"rel_path": p, "files": r.Files, "dirs": r.Dirs, "bytes_logical": r.BytesLogical, "bytes_alloc": r.BytesAlloc, "mtime": r.Mtime})
			}); err != nil {
				return err
			}
			return rw.close()
		}
	case oapi.ExportNodeParamsWhatTop:
		rows, err := a.repo.ExportTop(ctx, s, ref)
		if err != nil {
			release()
			return nil, err
		}
		resp.run = func(w io.Writer) error {
			rw, err := newRowWriter(w, format, []string{"kind", "rank", "rel_path", "size", "alloc", "mtime"})
			if err != nil {
				return err
			}
			for _, r := range rows {
				p := string(fname.Escape(joinRel(r.DirRel, r.Name)))
				if err := rw.add([]string{r.Kind, strconv.Itoa(r.Rank), p, i64(r.Size), i64(r.Alloc), i64(r.Mtime)},
					map[string]any{"kind": r.Kind, "rank": r.Rank, "rel_path": p, "size": r.Size, "alloc": r.Alloc, "mtime": r.Mtime}); err != nil {
					return err
				}
			}
			return rw.close()
		}
	default:
		rows, err := a.repo.ExportUsage(ctx, callerFor(u), s, ref)
		if err != nil {
			release()
			return nil, err
		}
		resp.run = func(w io.Writer) error {
			rw, err := newRowWriter(w, format, []string{"username", "host_path", "found", "redundant", "files", "bytes_logical", "bytes_alloc"})
			if err != nil {
				return err
			}
			for _, r := range rows {
				hp := string(fname.Escape(r.HostPath))
				if err := rw.add([]string{r.Username, hp, boolStr(r.Found), boolStr(r.Redundant), i64(r.Files), i64(r.BytesLogical), i64(r.BytesAlloc)},
					map[string]any{"username": r.Username, "host_path": hp, "found": r.Found, "redundant": r.Redundant, "files": r.Files, "bytes_logical": r.BytesLogical, "bytes_alloc": r.BytesAlloc}); err != nil {
					return err
				}
			}
			return rw.close()
		}
	}
	return resp, nil
}

// joinRel is dir/name, or name alone for a file directly in the node.
func joinRel(dir, name []byte) []byte {
	if len(dir) == 0 {
		return name
	}
	return append(append(append([]byte(nil), dir...), '/'), name...)
}

// exportWriteTimeout is how long one write may block. A client that stops
// reading would otherwise hold the generation handle and an export slot for
// good.
const exportWriteTimeout = 60 * time.Second

// deadlineWriter re-arms the write deadline before every write. Writers that
// cannot take a deadline (tests) are written to as they are.
type deadlineWriter struct {
	w  http.ResponseWriter
	rc *http.ResponseController
}

func (d *deadlineWriter) Write(p []byte) (int, error) {
	_ = d.rc.SetWriteDeadline(time.Now().Add(exportWriteTimeout))
	return d.w.Write(p)
}
