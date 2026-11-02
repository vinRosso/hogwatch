package web

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/vinRosso/hogwatch/internal/audit"
	"github.com/vinRosso/hogwatch/internal/db"
	"github.com/vinRosso/hogwatch/internal/db/dbq"
	"github.com/vinRosso/hogwatch/internal/web/oapi"
)

const scansPageSize = 100

// scanCursor is the position after a page of the admin scan history. Root is
// the filter it was taken under (0 for none), so a cursor from one filter is
// refused under another.
type scanCursor struct {
	V         int   `json:"v"`
	Root      int64 `json:"root"`
	StartedAt int64 `json:"started_at"`
	ID        int64 `json:"id"`
}

// progressOf decodes a jobs.progress value for the response. A value that
// does not decode is shown as null and logged.
func (a *server) progressOf(raw sql.NullString, job int64) *map[string]interface{} {
	if !raw.Valid {
		return nil
	}
	var m map[string]interface{}
	if err := json.Unmarshal([]byte(raw.String), &m); err != nil {
		a.log.Warn("unreadable job progress", "job", job, "err", err)
		return nil
	}
	return &m
}

func nullInt(n sql.NullInt64) *int64 {
	if !n.Valid {
		return nil
	}
	return &n.Int64
}

func scanJobOf(a *server, j dbq.GetJobAdminRow) oapi.ScanJob {
	out := oapi.ScanJob{
		JobId: j.ID, RootId: j.RootID, Status: oapi.ScanJobStatus(j.Status), Progress: a.progressOf(j.Progress, j.ID),
		CreatedAt: j.CreatedAt, StartedAt: nullInt(j.StartedAt), FinishedAt: nullInt(j.FinishedAt),
	}
	if j.Error.Valid {
		out.Error = &j.Error.String
	}
	if id := j.ScanID; id.Valid {
		out.ScanId = &id.Int64
	}
	return out
}

func (a *server) CreateScan(ctx context.Context, req oapi.CreateScanRequestObject) (oapi.CreateScanResponseObject, error) {
	admin, ok := UserFrom(ctx)
	if !ok {
		return nil, &apiError{status: http.StatusUnauthorized}
	}
	if req.Body == nil || req.Body.RootId < 1 {
		return nil, badRequest("invalid request")
	}
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	q := a.q.WithTx(tx)
	root, err := q.GetAdminRoot(ctx, req.Body.RootId)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, &apiError{status: http.StatusNotFound}
	}
	if err != nil {
		return nil, err
	}
	if root.Enabled == 0 {
		return nil, &apiError{status: http.StatusConflict, detail: "root is disabled"}
	}
	active, err := q.CountActiveScanJobs(ctx, root.ID)
	if err != nil {
		return nil, err
	}
	if active > 0 {
		return nil, &apiError{status: http.StatusConflict, detail: "a scan of this root is queued or running"}
	}
	params, err := json.Marshal(db.ScanParams{Trigger: "manual"})
	if err != nil {
		return nil, err
	}
	id, err := q.InsertJob(ctx, dbq.InsertJobParams{
		Kind: string(db.KindScan), RootID: root.ID, Params: string(params),
		RequestedBy: sql.NullInt64{Int64: admin.ID, Valid: true}, CreatedAt: a.now().Unix(),
	})
	if err != nil {
		return nil, err
	}
	err = audit.Write(ctx, q, audit.Entry{
		ActorID: &admin.ID, Action: "scan.requested", Target: root.Name,
		Details: map[string]any{"root_id": root.ID, "job_id": id}, At: a.now(),
	})
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return oapi.CreateScan202JSONResponse{Id: id, Status: oapi.ListingCreatedStatusQueued}, nil
}

// scanJob loads a job and 404s anything that is not a scan job.
func scanJob(ctx context.Context, q *dbq.Queries, id int64) (dbq.GetJobAdminRow, error) {
	j, err := q.GetJobAdmin(ctx, id)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && j.Kind != string(db.KindScan)) {
		return j, &apiError{status: http.StatusNotFound}
	}
	return j, err
}

func (a *server) CancelScan(ctx context.Context, req oapi.CancelScanRequestObject) (oapi.CancelScanResponseObject, error) {
	admin, ok := UserFrom(ctx)
	if !ok {
		return nil, &apiError{status: http.StatusUnauthorized}
	}
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	q := a.q.WithTx(tx)
	job, err := scanJob(ctx, q, req.Id)
	if err != nil {
		return nil, err
	}
	if job.Status != string(db.StatusQueued) && job.Status != string(db.StatusRunning) {
		return oapi.CancelScan204Response{}, nil
	}
	pending, err := q.HasActiveCancel(ctx, job.ID)
	if err != nil {
		return nil, err
	}
	if pending != 0 {
		return oapi.CancelScan204Response{}, nil
	}
	params, err := json.Marshal(db.CancelParams{JobID: job.ID})
	if err != nil {
		return nil, err
	}
	if _, err := q.InsertJob(ctx, dbq.InsertJobParams{
		Kind: string(db.KindCancel), RootID: job.RootID, Params: string(params),
		RequestedBy: sql.NullInt64{Int64: admin.ID, Valid: true}, CreatedAt: a.now().Unix(),
	}); err != nil {
		return nil, err
	}
	err = audit.Write(ctx, q, audit.Entry{
		ActorID: &admin.ID, Action: "scan.cancel_requested", Target: strconv.FormatInt(job.ID, 10),
		Details: map[string]any{"root_id": job.RootID, "job_id": job.ID}, At: a.now(),
	})
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return oapi.CancelScan204Response{}, nil
}

func (a *server) GetScanProgress(ctx context.Context, req oapi.GetScanProgressRequestObject) (oapi.GetScanProgressResponseObject, error) {
	j, err := scanJob(ctx, a.q, req.Id)
	if err != nil {
		return nil, err
	}
	return oapi.GetScanProgress200JSONResponse(scanJobOf(a, j)), nil
}

func (a *server) ListAdminScans(ctx context.Context, req oapi.ListAdminScansRequestObject) (oapi.ListAdminScansResponseObject, error) {
	pageSize := scansPageSize
	if req.Params.Limit != nil {
		if pageSize = int(*req.Params.Limit); pageSize < 1 || pageSize > scansPageSize {
			return nil, badRequest("invalid limit")
		}
	}
	p := dbq.ListScansAdminPageParams{Lim: int64(pageSize) + 1}
	var filter int64
	if req.Params.Root != nil {
		filter = *req.Params.Root
		p.RootID = filter
	}
	if req.Params.Cursor != nil {
		var c scanCursor
		if decodeCursor(*req.Params.Cursor, &c) != nil || c.V != cursorVersion || c.Root != filter || c.ID < 1 {
			return nil, badRequest("invalid cursor")
		}
		p.BeforeStarted = c.StartedAt
		p.BeforeID = sql.NullInt64{Int64: c.ID, Valid: true}
	}
	rows, err := a.q.ListScansAdminPage(ctx, p)
	if err != nil {
		return nil, err
	}
	out := oapi.ListAdminScans200JSONResponse{Items: []oapi.ScanAdmin{}}
	for i, r := range rows {
		if i == pageSize {
			next := encodeCursor(scanCursor{V: cursorVersion, Root: filter, StartedAt: rows[i-1].StartedAt, ID: rows[i-1].ID})
			out.NextCursor = &next
			break
		}
		s := oapi.ScanAdmin{
			Id: r.ID, JobId: nullInt(r.JobID), RootId: r.RootID, Status: r.Status, Trigger: oapi.ScanAdminTrigger(r.Trigger),
			StartedAt: r.StartedAt, FinishedAt: nullInt(r.FinishedAt), Dirs: r.Dirs, Files: r.Files, BytesAlloc: r.BytesAlloc,
			ErrorCount: r.ErrorCount, Progress: a.progressOf(r.Progress, r.JobID.Int64),
		}
		if r.Error.Valid {
			s.Error = &r.Error.String
		}
		out.Items = append(out.Items, s)
	}
	return out, nil
}
