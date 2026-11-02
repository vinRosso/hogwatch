package web

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"

	"github.com/vinRosso/hogwatch/internal/audit"
	"github.com/vinRosso/hogwatch/internal/db/dbq"
	"github.com/vinRosso/hogwatch/internal/fname"
	"github.com/vinRosso/hogwatch/internal/pathx"
	"github.com/vinRosso/hogwatch/internal/repo"
	"github.com/vinRosso/hogwatch/internal/totals"
	"github.com/vinRosso/hogwatch/internal/web/oapi"
)

// placeholderPath is the id a path gets in a recompute before it is inserted.
const placeholderPath = int64(1) << 62

// maxPathAttempts bounds the optimistic recompute: the usage is computed
// outside the write transaction, and redone once if the paths or the current
// scan changed meanwhile.
const maxPathAttempts = 2

var errPathsChangedConcurrently = &apiError{status: http.StatusConflict, detail: "paths changed concurrently, retry"}

func userPathOf(id, userID, rootID int64, rel []byte, found, redundant bool, created int64) oapi.UserPath {
	return oapi.UserPath{
		Id: id, UserId: userID, RootId: rootID, RelPath: fname.Escape(rel),
		Found: found, Redundant: redundant, CreatedAt: created,
	}
}

// parseRelPath turns an escaped rel_path into its normalised raw form.
func parseRelPath(escaped string) ([]byte, error) {
	raw, err := fname.Unescape(escaped)
	if err != nil {
		return nil, badRequest("rel_path is not a valid escaped name")
	}
	rel, err := pathx.Normalize(string(raw))
	if err != nil {
		return nil, badRequest("rel_path is not a valid relative path")
	}
	return []byte(rel), nil
}

// checkOverlap applies the overlap rule to a new assignment of rel to userID on the
// root whose current paths are all. Another user's overlapping path is 422; an
// equal path of the same user is 409. redundant reports that the new path is
// nested inside another path of the same user.
func checkOverlap(userID, rootID int64, rel []byte, all []dbq.ListUserPathsOnRootAllRow) (redundant bool, err error) {
	n := pathx.Assignment{UserID: userID, RootID: rootID, Path: string(rel)}
	for _, o := range all {
		other := pathx.Assignment{UserID: o.UserID, RootID: rootID, Path: string(o.RelPath)}
		v, inner := pathx.Classify(n, other)
		switch {
		case v == pathx.Conflict:
			return false, &apiError{status: http.StatusUnprocessableEntity,
				detail: fmt.Sprintf("overlaps a path of user %s", o.Username)}
		case v == pathx.Redundant && pathx.Compare(n.Path, other.Path) == pathx.Equal:
			return false, &apiError{status: http.StatusConflict, detail: "that path is already assigned to the user"}
		case v == pathx.Redundant && inner == n:
			redundant = true
		}
	}
	return redundant, nil
}

// userPathsIn returns the paths of userID among all.
func userPathsIn(all []dbq.ListUserPathsOnRootAllRow, userID int64) []dbq.ListUserPathsOnRootAllRow {
	var mine []dbq.ListUserPathsOnRootAllRow
	for _, p := range all {
		if p.UserID == userID {
			mine = append(mine, p)
		}
	}
	return mine
}

func sameUserPaths(a, b []dbq.ListUserPathsOnRootAllRow) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].ID != b[i].ID || !bytes.Equal(a[i].RelPath, b[i].RelPath) {
			return false
		}
	}
	return true
}

func totalsPaths(userID int64, ps []dbq.ListUserPathsOnRootAllRow, extra ...totals.Path) []totals.Path {
	out := make([]totals.Path, 0, len(ps)+len(extra))
	for _, p := range ps {
		out = append(out, totals.Path{ID: p.ID, UserID: userID, Rel: string(p.RelPath)})
	}
	return append(out, extra...)
}

func sameScan(a, b sql.NullInt64) bool { return a.Valid == b.Valid && a.Int64 == b.Int64 }

// computeUsage computes the user's usage against the root's current scan. ok
// is false when the root has no scan to compute against or its generation can
// no longer be served; the next scan then fixes the numbers.
func (a *server) computeUsage(ctx context.Context, scan sql.NullInt64, paths []totals.Path) (u repo.UserUsage, ok bool, err error) {
	if !scan.Valid {
		return u, false, nil
	}
	u, err = a.repo.ComputeUserUsage(ctx, scan.Int64, paths)
	if errors.Is(err, repo.ErrNotFound) {
		a.log.Warn("not recomputing usage: the current generation cannot be served", "scan", scan.Int64)
		return u, false, nil
	}
	return u, err == nil, err
}

func (a *server) ListUserPaths(ctx context.Context, req oapi.ListUserPathsRequestObject) (oapi.ListUserPathsResponseObject, error) {
	if _, err := a.q.GetAdminUser(ctx, req.Id); errors.Is(err, sql.ErrNoRows) {
		return nil, &apiError{status: http.StatusNotFound}
	} else if err != nil {
		return nil, err
	}
	rows, err := a.q.ListUserPathsOfUser(ctx, req.Id)
	if err != nil {
		return nil, err
	}
	stats, err := a.q.ListPathStatsForUser(ctx, req.Id)
	if err != nil {
		return nil, err
	}
	found := make(map[int64]bool, len(stats))
	for _, s := range stats {
		found[s.UserPathID] = s.Found != 0
	}
	out := oapi.ListUserPaths200JSONResponse{Items: []oapi.UserPath{}}
	for _, r := range rows {
		redundant := false
		for _, o := range rows {
			if o.RootID == r.RootID && o.ID != r.ID &&
				pathx.Compare(string(r.RelPath), string(o.RelPath)) == pathx.AInsideB {
				redundant = true
			}
		}
		out.Items = append(out.Items, userPathOf(r.ID, r.UserID, r.RootID, r.RelPath, found[r.ID], redundant, r.CreatedAt))
	}
	return out, nil
}

func (a *server) CreateUserPath(ctx context.Context, req oapi.CreateUserPathRequestObject) (oapi.CreateUserPathResponseObject, error) {
	admin, ok := UserFrom(ctx)
	if !ok {
		return nil, &apiError{status: http.StatusUnauthorized}
	}
	if req.Body == nil {
		return nil, badRequest("invalid request")
	}
	rel, err := parseRelPath(req.Body.RelPath)
	if err != nil {
		return nil, err
	}
	for range maxPathAttempts {
		up, retry, err := a.createPathOnce(ctx, admin.ID, req.Id, req.Body.RootId, rel)
		if err != nil {
			return nil, err
		}
		if !retry {
			return oapi.CreateUserPath201JSONResponse(up), nil
		}
	}
	return nil, errPathsChangedConcurrently
}

func (a *server) createPathOnce(ctx context.Context, actor, userID, rootID int64, rel []byte) (up oapi.UserPath, retry bool, err error) {
	root, err := a.q.GetAdminRoot(ctx, rootID)
	if errors.Is(err, sql.ErrNoRows) {
		return up, false, &apiError{status: http.StatusNotFound}
	}
	if err != nil {
		return up, false, err
	}
	if _, err := a.q.GetAdminUser(ctx, userID); errors.Is(err, sql.ErrNoRows) {
		return up, false, &apiError{status: http.StatusNotFound}
	} else if err != nil {
		return up, false, err
	}
	all, err := a.q.ListUserPathsOnRootAll(ctx, rootID)
	if err != nil {
		return up, false, err
	}
	if _, err := checkOverlap(userID, rootID, rel, all); err != nil {
		return up, false, err
	}
	mine := userPathsIn(all, userID)
	paths := totalsPaths(userID, mine, totals.Path{ID: placeholderPath, UserID: userID, Rel: string(rel)})
	usage, computed, err := a.computeUsage(ctx, root.CurrentScanID, paths)
	if err != nil {
		return up, false, err
	}
	if a.afterCompute != nil {
		a.afterCompute()
	}

	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return up, false, err
	}
	defer func() { _ = tx.Rollback() }()
	q := a.q.WithTx(tx)
	user, err := q.GetAdminUser(ctx, userID)
	if errors.Is(err, sql.ErrNoRows) {
		return up, false, &apiError{status: http.StatusNotFound}
	}
	if err != nil {
		return up, false, err
	}
	root2, err := q.GetAdminRoot(ctx, rootID)
	if errors.Is(err, sql.ErrNoRows) {
		return up, false, &apiError{status: http.StatusNotFound}
	}
	if err != nil {
		return up, false, err
	}
	all2, err := q.ListUserPathsOnRootAll(ctx, rootID)
	if err != nil {
		return up, false, err
	}
	if !sameScan(root.CurrentScanID, root2.CurrentScanID) || !sameUserPaths(mine, userPathsIn(all2, userID)) {
		return up, true, nil
	}
	redundant, err := checkOverlap(userID, rootID, rel, all2)
	if err != nil {
		return up, false, err
	}
	row, err := q.InsertUserPath(ctx, dbq.InsertUserPathParams{
		UserID: userID, RootID: rootID, RelPath: rel,
		CreatedBy: sql.NullInt64{Int64: actor, Valid: true}, CreatedAt: a.now().Unix(),
	})
	if err != nil {
		if uniqueOn(err, "user_paths") {
			return up, false, &apiError{status: http.StatusConflict, detail: "that path is already assigned"}
		}
		return up, false, err
	}
	found := false
	if computed {
		for i := range usage.Paths {
			if usage.Paths[i].UserPathID == placeholderPath {
				usage.Paths[i].UserPathID = row.ID
				found = usage.Paths[i].Found
			}
		}
		if err := repo.ApplyUserUsage(ctx, q, root.CurrentScanID.Int64, rootID, userID, usage); err != nil {
			return up, false, err
		}
	}
	err = audit.Write(ctx, q, audit.Entry{
		ActorID: &actor, Action: "path.assigned", Target: user.Username,
		Details: map[string]any{"root_id": rootID, "rel_path": fname.Escape(rel), "path_id": row.ID}, At: a.now(),
	})
	if err != nil {
		return up, false, err
	}
	if err := tx.Commit(); err != nil {
		return up, false, err
	}
	return userPathOf(row.ID, userID, rootID, row.RelPath, found, redundant, row.CreatedAt), false, nil
}

func (a *server) DeleteUserPath(ctx context.Context, req oapi.DeleteUserPathRequestObject) (oapi.DeleteUserPathResponseObject, error) {
	admin, ok := UserFrom(ctx)
	if !ok {
		return nil, &apiError{status: http.StatusUnauthorized}
	}
	for range maxPathAttempts {
		retry, err := a.deletePathOnce(ctx, admin.ID, req.Id, req.PathId)
		if err != nil {
			return nil, err
		}
		if !retry {
			return oapi.DeleteUserPath204Response{}, nil
		}
	}
	return nil, errPathsChangedConcurrently
}

func (a *server) deletePathOnce(ctx context.Context, actor, userID, pathID int64) (retry bool, err error) {
	target, err := a.q.GetUserPath(ctx, dbq.GetUserPathParams{ID: pathID, UserID: userID})
	if errors.Is(err, sql.ErrNoRows) {
		return false, &apiError{status: http.StatusNotFound}
	}
	if err != nil {
		return false, err
	}
	root, err := a.q.GetAdminRoot(ctx, target.RootID)
	if errors.Is(err, sql.ErrNoRows) {
		return false, &apiError{status: http.StatusNotFound}
	}
	if err != nil {
		return false, err
	}
	all, err := a.q.ListUserPathsOnRootAll(ctx, target.RootID)
	if err != nil {
		return false, err
	}
	mine := userPathsIn(all, userID)
	var rest []dbq.ListUserPathsOnRootAllRow
	for _, p := range mine {
		if p.ID != pathID {
			rest = append(rest, p)
		}
	}
	usage, computed, err := a.computeUsage(ctx, root.CurrentScanID, totalsPaths(userID, rest))
	if err != nil {
		return false, err
	}
	if a.afterCompute != nil {
		a.afterCompute()
	}

	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()
	q := a.q.WithTx(tx)
	user, err := q.GetAdminUser(ctx, userID)
	if errors.Is(err, sql.ErrNoRows) {
		return false, &apiError{status: http.StatusNotFound}
	}
	if err != nil {
		return false, err
	}
	root2, err := q.GetAdminRoot(ctx, target.RootID)
	if errors.Is(err, sql.ErrNoRows) {
		return false, &apiError{status: http.StatusNotFound}
	}
	if err != nil {
		return false, err
	}
	all2, err := q.ListUserPathsOnRootAll(ctx, target.RootID)
	if err != nil {
		return false, err
	}
	if !sameScan(root.CurrentScanID, root2.CurrentScanID) || !sameUserPaths(mine, userPathsIn(all2, userID)) {
		return true, nil
	}
	n, err := q.DeleteUserPath(ctx, dbq.DeleteUserPathParams{ID: pathID, UserID: userID})
	if err != nil {
		return false, err
	}
	if n == 0 {
		return false, &apiError{status: http.StatusNotFound}
	}
	if err := q.DeleteDupIssuesForPath(ctx, pathID); err != nil {
		return false, err
	}
	if computed {
		if err := repo.ApplyUserUsage(ctx, q, root.CurrentScanID.Int64, target.RootID, userID, usage); err != nil {
			return false, err
		}
	} else if root.CurrentScanID.Valid {
		// The generation cannot be served, so the numbers cannot be redone. A row
		// that still counts the removed path would be wrong; none is honest until
		// the next scan.
		if err := q.DeleteUsageForUser(ctx, dbq.DeleteUsageForUserParams{ScanID: root.CurrentScanID.Int64, UserID: userID}); err != nil {
			return false, err
		}
	}
	err = audit.Write(ctx, q, audit.Entry{
		ActorID: &actor, Action: "path.removed", Target: user.Username,
		Details: map[string]any{"root_id": target.RootID, "rel_path": fname.Escape(target.RelPath), "path_id": pathID}, At: a.now(),
	})
	if err != nil {
		return false, err
	}
	return false, tx.Commit()
}

// ListAllUserPaths lists every assignment in one response; the browse view colours folders
// by owner from it.
func (a *server) ListAllUserPaths(ctx context.Context, _ oapi.ListAllUserPathsRequestObject) (oapi.ListAllUserPathsResponseObject, error) {
	rows, err := a.q.ListAllUserPaths(ctx)
	if err != nil {
		return nil, err
	}
	out := oapi.ListAllUserPaths200JSONResponse{Items: make([]oapi.OwnedPath, 0, len(rows))}
	for _, r := range rows {
		out.Items = append(out.Items, oapi.OwnedPath{UserId: r.UserID, RootId: r.RootID, RelPath: fname.Escape(r.RelPath)})
	}
	return out, nil
}
