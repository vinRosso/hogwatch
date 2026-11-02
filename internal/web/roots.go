package web

import (
	"context"
	"net/http"

	"github.com/vinRosso/hogwatch/internal/auth/session"
	"github.com/vinRosso/hogwatch/internal/fname"
	"github.com/vinRosso/hogwatch/internal/repo"
	"github.com/vinRosso/hogwatch/internal/web/oapi"
)

func callerOf(ctx context.Context) (repo.Caller, error) {
	u, ok := UserFrom(ctx)
	if !ok {
		return repo.Caller{}, &apiError{status: http.StatusUnauthorized}
	}
	return callerFor(u), nil
}

func callerFor(u session.User) repo.Caller { return repo.Caller{UserID: u.ID, Admin: u.IsAdmin()} }

type anchorJSON = struct {
	Found    bool             `json:"found"`
	HostPath oapi.EscapedName `json:"host_path"`
	Ref      *string          `json:"ref"`
}

func (a *server) ListRoots(ctx context.Context, _ oapi.ListRootsRequestObject) (oapi.ListRootsResponseObject, error) {
	c, err := callerOf(ctx)
	if err != nil {
		return nil, err
	}
	roots, err := a.repo.Roots(ctx, c)
	if err != nil {
		return nil, err
	}
	res := oapi.ListRoots200JSONResponse{Items: make([]oapi.RootInfo, 0, len(roots))}
	for _, r := range roots {
		info := oapi.RootInfo{
			Id: r.ID, Name: r.Name, LastScannedAt: r.LastScannedAt, Scanning: r.Scanning,
			FsTotal: r.FSTotal, FsFree: r.FSFree, Anchors: make([]anchorJSON, 0, len(r.Anchors)),
		}
		for _, an := range r.Anchors {
			info.Anchors = append(info.Anchors, anchorJSON{Found: an.Found, HostPath: fname.Escape(an.HostPath), Ref: refString(an.Ref)})
		}
		res.Items = append(res.Items, info)
	}
	return res, nil
}

func totalsJSON(t repo.Totals) oapi.Totals {
	return oapi.Totals{Files: t.Files, BytesLogical: t.BytesLogical, BytesAlloc: t.BytesAlloc}
}

type mountJSON = struct {
	FsFree              *int64      `json:"fs_free"`
	FsTotal             *int64      `json:"fs_total"`
	GrowthBytes         *int64      `json:"growth_bytes"`
	RootId              int64       `json:"root_id"`
	RootName            string      `json:"root_name"`
	SharedHardlinkBytes int64       `json:"shared_hardlink_bytes"`
	Usage               oapi.Totals `json:"usage"`
}

type pathUsageJSON = struct {
	Found bool `json:"found"`

	HostPath   oapi.EscapedName `json:"host_path"`
	Redundant  bool             `json:"redundant"`
	RootId     int64            `json:"root_id"`
	Usage      oapi.Totals      `json:"usage"`
	UserPathId int64            `json:"user_path_id"`
}

func (a *server) GetMyUsage(ctx context.Context, req oapi.GetMyUsageRequestObject) (oapi.GetMyUsageResponseObject, error) {
	c, err := callerOf(ctx)
	if err != nil {
		return nil, err
	}
	var w *string
	if req.Params.Window != nil {
		s := string(*req.Params.Window)
		w = &s
	}
	d, err := growthWindow(w)
	if err != nil {
		return nil, err
	}
	u, err := a.repo.MyUsage(ctx, c, a.now().Add(-d).Unix())
	if err != nil {
		return nil, err
	}
	res := oapi.MyUsage{
		QuotaBytes: u.QuotaBytes, QuotaBasis: oapi.MyUsageQuotaBasis(u.QuotaBasis), Total: totalsJSON(u.Total), GrowthBytes: u.GrowthBytes,
		Mounts: make([]mountJSON, 0, len(u.Mounts)), Paths: make([]pathUsageJSON, 0, len(u.Paths)),
	}
	for _, m := range u.Mounts {
		res.Mounts = append(res.Mounts, mountJSON{
			FsFree: m.FSFree, FsTotal: m.FSTotal, GrowthBytes: m.GrowthBytes, RootId: m.RootID, RootName: m.RootName,
			SharedHardlinkBytes: m.SharedHardlinkBytes, Usage: totalsJSON(m.Usage),
		})
	}
	for _, p := range u.Paths {
		res.Paths = append(res.Paths, pathUsageJSON{
			Found: p.Found, HostPath: fname.Escape(p.HostPath), Redundant: p.Redundant, RootId: p.RootID,
			Usage: totalsJSON(p.Usage), UserPathId: p.UserPathID,
		})
	}
	return oapi.GetMyUsage200JSONResponse(res), nil
}

func (a *server) ListScans(ctx context.Context, req oapi.ListScansRequestObject) (oapi.ListScansResponseObject, error) {
	c, err := callerOf(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := a.repo.Scans(ctx, c, req.Params.Root)
	if err != nil {
		return nil, err
	}
	res := oapi.ListScans200JSONResponse{Items: make([]oapi.ScanSummary, 0, len(rows))}
	for _, s := range rows {
		res.Items = append(res.Items, oapi.ScanSummary{
			Id: s.ID, RootId: s.RootID, Status: s.Status, Trigger: oapi.ScanSummaryTrigger(s.Trigger),
			StartedAt: s.StartedAt, FinishedAt: s.FinishedAt, Dirs: s.Dirs, Files: s.Files, BytesAlloc: s.BytesAlloc,
		})
	}
	return res, nil
}
