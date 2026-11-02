package web

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/vinRosso/hogwatch/internal/audit"
	"github.com/vinRosso/hogwatch/internal/db"
	"github.com/vinRosso/hogwatch/internal/db/dbq"
	"github.com/vinRosso/hogwatch/internal/pathx"
	"github.com/vinRosso/hogwatch/internal/schedule"
	"github.com/vinRosso/hogwatch/internal/web/oapi"
)

const (
	maxExcludes   = 1000
	maxRootName   = 128
	maxScheduleLn = 128
)

func unprocessable(detail string) error {
	return &apiError{status: http.StatusUnprocessableEntity, detail: detail}
}

func adminRootOf(r dbq.ScanRoot) oapi.AdminRoot {
	out := oapi.AdminRoot{
		Id: r.ID, Name: r.Name, ContainerPath: r.ContainerPath, HostPath: r.HostPath, CrossFs: r.CrossFs != 0,
		Excludes: []string{}, Schedule: r.Schedule, Enabled: r.Enabled != 0,
	}
	_ = json.Unmarshal([]byte(r.Excludes), &out.Excludes)
	if r.ZfsDataset.Valid {
		out.ZfsDataset = &r.ZfsDataset.String
	}
	if r.CurrentScanID.Valid {
		out.CurrentScanId = &r.CurrentScanID.Int64
	}
	return out
}

// rootSpec is the part of a root the nesting rule looks at.
type rootSpec struct {
	ID            int64
	ContainerPath string
	CrossFS       bool
}

func containerSegments(p string) []string {
	var out []string
	for _, s := range strings.Split(p, "/") {
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}

// validateRoots applies the nesting rule to cand against the
// other roots: two roots on the same mount are refused, and when one root's
// container path lies inside another's, the outer root must not cross
// filesystems (it would count the inner mount twice). Roots with the id of
// cand are skipped, so an update does not collide with itself.
func validateRoots(all []rootSpec, cand rootSpec) error {
	cs := containerSegments(cand.ContainerPath)
	for _, o := range all {
		if o.ID == cand.ID {
			continue
		}
		osg := containerSegments(o.ContainerPath)
		switch pathx.Compare(strings.Join(cs, "/"), strings.Join(osg, "/")) {
		case pathx.Equal:
			return unprocessable("that mount is already used by another root")
		case pathx.AInsideB: // cand is inside o
			if o.CrossFS {
				return unprocessable("a root that contains this mount must not cross filesystems (cross_fs)")
			}
		case pathx.BInsideA: // o is inside cand
			if cand.CrossFS {
				return unprocessable("cross_fs is not allowed: another root's mount lies inside this one")
			}
		}
	}
	return nil
}

// normExcludes normalises each exclude to a non-empty relative path and
// refuses duplicates and more than maxExcludes.
func normExcludes(in []string) ([]string, error) {
	if len(in) > maxExcludes {
		return nil, unprocessable(fmt.Sprintf("at most %d excludes", maxExcludes))
	}
	out := make([]string, 0, len(in))
	seen := map[string]bool{}
	for _, x := range in {
		n, err := pathx.Normalize(x)
		if err != nil || n == "" {
			return nil, unprocessable("an exclude is not a valid relative path")
		}
		if seen[n] {
			return nil, unprocessable("duplicate exclude")
		}
		seen[n] = true
		out = append(out, n)
	}
	return out, nil
}

func checkSchedule(s string) error {
	if s == "" {
		return nil
	}
	if len(s) > maxScheduleLn {
		return unprocessable("schedule is too long")
	}
	if _, err := schedule.Parse(s); err != nil {
		return unprocessable("invalid schedule: " + err.Error())
	}
	return nil
}

func checkRootName(s string) error {
	s = strings.TrimSpace(s)
	if s == "" || len(s) > maxRootName || !utf8.ValidString(s) || strings.ContainsFunc(s, unicode.IsControl) {
		return badRequest("invalid root name")
	}
	return nil
}

func rootSpecs(rows []dbq.ScanRoot) []rootSpec {
	out := make([]rootSpec, len(rows))
	for i, r := range rows {
		out[i] = rootSpec{ID: r.ID, ContainerPath: r.ContainerPath, CrossFS: r.CrossFs != 0}
	}
	return out
}

// availableMounts reads the mounts the scanner published. None before its
// first start.
func availableMounts(ctx context.Context, q *dbq.Queries) ([]db.AvailableMount, error) {
	st, err := q.GetScannerStatus(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var ms []db.AvailableMount
	if err := json.Unmarshal([]byte(st.AvailableMounts), &ms); err != nil {
		return nil, fmt.Errorf("available_mounts: %w", err)
	}
	return ms, nil
}

func (a *server) ListAdminRoots(ctx context.Context, _ oapi.ListAdminRootsRequestObject) (oapi.ListAdminRootsResponseObject, error) {
	rows, err := a.q.ListAdminRoots(ctx)
	if err != nil {
		return nil, err
	}
	out := oapi.ListAdminRoots200JSONResponse{Items: make([]oapi.AdminRoot, 0, len(rows))}
	for _, r := range rows {
		out.Items = append(out.Items, adminRootOf(r))
	}
	return out, nil
}

func (a *server) ListAvailableMounts(ctx context.Context, _ oapi.ListAvailableMountsRequestObject) (oapi.ListAvailableMountsResponseObject, error) {
	ms, err := availableMounts(ctx, a.q)
	if err != nil {
		return nil, err
	}
	roots, err := a.q.ListAdminRoots(ctx)
	if err != nil {
		return nil, err
	}
	used := map[string]int64{}
	for _, r := range roots {
		used[r.ContainerPath] = r.ID
	}
	out := oapi.ListAvailableMounts200JSONResponse{Items: make([]oapi.AvailableMount, 0, len(ms))}
	for _, m := range ms {
		am := oapi.AvailableMount{ContainerPath: m.ContainerPath, HostPath: m.HostPath, Fstype: m.FSType, Readonly: m.ReadOnly}
		if id, ok := used[m.ContainerPath]; ok {
			am.RootId = &id
		}
		out.Items = append(out.Items, am)
	}
	return out, nil
}

func (a *server) CreateRoot(ctx context.Context, req oapi.CreateRootRequestObject) (oapi.CreateRootResponseObject, error) {
	admin, ok := UserFrom(ctx)
	if !ok {
		return nil, &apiError{status: http.StatusUnauthorized}
	}
	b := req.Body
	if b == nil {
		return nil, badRequest("invalid request")
	}
	if err := checkRootName(b.Name); err != nil {
		return nil, err
	}
	name := strings.TrimSpace(b.Name)
	var excludes []string
	if b.Excludes != nil {
		var err error
		if excludes, err = normExcludes(*b.Excludes); err != nil {
			return nil, err
		}
	}
	sched := ""
	if b.Schedule != nil {
		sched = *b.Schedule
	}
	if err := checkSchedule(sched); err != nil {
		return nil, err
	}
	crossFS := b.CrossFs != nil && *b.CrossFs
	enabled := b.Enabled == nil || *b.Enabled

	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	q := a.q.WithTx(tx)
	mounts, err := availableMounts(ctx, q)
	if err != nil {
		return nil, err
	}
	i := slices.IndexFunc(mounts, func(m db.AvailableMount) bool { return m.ContainerPath == b.ContainerPath })
	if i < 0 {
		return nil, unprocessable("container_path is not an available mount")
	}
	m := mounts[i]
	if !m.ReadOnly && !a.cfg.Scan.AllowWritableMounts {
		return nil, unprocessable("that mount is writable; the scanner refuses writable filesystems")
	}
	existing, err := q.ListAdminRoots(ctx)
	if err != nil {
		return nil, err
	}
	if err := validateRoots(rootSpecs(existing), rootSpec{ContainerPath: m.ContainerPath, CrossFS: crossFS}); err != nil {
		return nil, err
	}
	exJSON, _ := json.Marshal(append([]string{}, excludes...))
	id, err := q.InsertRoot(ctx, dbq.InsertRootParams{
		Name: name, ContainerPath: m.ContainerPath, HostPath: m.HostPath, CrossFs: b2i(crossFS),
		Excludes: string(exJSON), Schedule: sched, Enabled: b2i(enabled),
	})
	if uniqueOn(err, "scan_roots.name") {
		return nil, &apiError{status: http.StatusConflict, detail: "a root with that name exists"}
	}
	if err != nil {
		return nil, err
	}
	err = audit.Write(ctx, q, audit.Entry{
		ActorID: &admin.ID, Action: "root.created", Target: name,
		Details: map[string]any{"root_id": id, "container_path": m.ContainerPath}, At: a.now(),
	})
	if err != nil {
		return nil, err
	}
	row, err := q.GetAdminRoot(ctx, id)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return oapi.CreateRoot201JSONResponse(adminRootOf(row)), nil
}

func (a *server) UpdateRoot(ctx context.Context, req oapi.UpdateRootRequestObject) (oapi.UpdateRootResponseObject, error) {
	admin, ok := UserFrom(ctx)
	if !ok {
		return nil, &apiError{status: http.StatusUnauthorized}
	}
	b := req.Body
	if b == nil {
		return nil, badRequest("invalid request")
	}
	if present, _ := patchField(ctx, "container_path"); present {
		return nil, badRequest("container_path cannot be changed; delete and recreate the root")
	}
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	q := a.q.WithTx(tx)
	cur, err := q.GetAdminRoot(ctx, req.Id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, &apiError{status: http.StatusNotFound}
	}
	if err != nil {
		return nil, err
	}
	p := dbq.UpdateRootParams{
		Name: cur.Name, CrossFs: cur.CrossFs, Excludes: cur.Excludes, Schedule: cur.Schedule, Enabled: cur.Enabled, ID: cur.ID,
	}
	var fields []string
	if b.Name != nil {
		if err := checkRootName(*b.Name); err != nil {
			return nil, err
		}
		p.Name = strings.TrimSpace(*b.Name)
		fields = append(fields, "name")
	}
	if b.CrossFs != nil {
		p.CrossFs = b2i(*b.CrossFs)
		fields = append(fields, "cross_fs")
	}
	if b.Excludes != nil {
		ex, err := normExcludes(*b.Excludes)
		if err != nil {
			return nil, err
		}
		j, _ := json.Marshal(ex)
		p.Excludes = string(j)
		fields = append(fields, "excludes")
	}
	if b.Schedule != nil {
		if err := checkSchedule(*b.Schedule); err != nil {
			return nil, err
		}
		p.Schedule = *b.Schedule
		fields = append(fields, "schedule")
	}
	if b.Enabled != nil {
		p.Enabled = b2i(*b.Enabled)
		fields = append(fields, "enabled")
	}
	if len(fields) == 0 {
		return nil, badRequest("no fields to change")
	}
	if b.CrossFs != nil {
		all, err := q.ListAdminRoots(ctx)
		if err != nil {
			return nil, err
		}
		if err := validateRoots(rootSpecs(all), rootSpec{ID: cur.ID, ContainerPath: cur.ContainerPath, CrossFS: p.CrossFs != 0}); err != nil {
			return nil, err
		}
	}
	if _, err := q.UpdateRoot(ctx, p); err != nil {
		if uniqueOn(err, "scan_roots.name") {
			return nil, &apiError{status: http.StatusConflict, detail: "a root with that name exists"}
		}
		return nil, err
	}
	slices.Sort(fields)
	err = audit.Write(ctx, q, audit.Entry{
		ActorID: &admin.ID, Action: "root.updated", Target: p.Name,
		Details: map[string]any{"root_id": cur.ID, "fields": fields}, At: a.now(),
	})
	if err != nil {
		return nil, err
	}
	row, err := q.GetAdminRoot(ctx, cur.ID)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return oapi.UpdateRoot200JSONResponse(adminRootOf(row)), nil
}

func (a *server) DeleteRoot(ctx context.Context, req oapi.DeleteRootRequestObject) (oapi.DeleteRootResponseObject, error) {
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
	cur, err := q.GetAdminRoot(ctx, req.Id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, &apiError{status: http.StatusNotFound}
	}
	if err != nil {
		return nil, err
	}
	active, err := q.CountActiveRootJobs(ctx, cur.ID)
	if err != nil {
		return nil, err
	}
	if active > 0 {
		return nil, &apiError{status: http.StatusConflict, detail: "a scan or listing of this root is queued or running"}
	}
	paths, err := q.CountUserPathsOnRoot(ctx, cur.ID)
	if err != nil {
		return nil, err
	}
	// Scans, usage, mount stats, jobs, paths, path stats and history cascade.
	// The generation files stay: the scanner's sweep removes them.
	if err := q.DeleteDupIssuesForRoot(ctx, cur.ID); err != nil {
		return nil, err
	}
	if _, err := q.DeleteRoot(ctx, cur.ID); err != nil {
		return nil, err
	}
	err = audit.Write(ctx, q, audit.Entry{
		ActorID: &admin.ID, Action: "root.deleted", Target: cur.Name,
		Details: map[string]any{"root_id": cur.ID, "paths_removed": paths}, At: a.now(),
	})
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return oapi.DeleteRoot204Response{}, nil
}
