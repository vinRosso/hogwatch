package web

import (
	"context"
	"encoding/json"
	"time"

	"github.com/vinRosso/hogwatch/internal/web/oapi"
)

var growthWindows = map[string]time.Duration{
	"7d": 7 * 24 * time.Hour, "30d": 30 * 24 * time.Hour, "90d": 90 * 24 * time.Hour,
}

// growthWindow resolves the optional growth window of /me/usage, ranking and
// mounts; absent means 7d.
func growthWindow(w *string) (time.Duration, error) {
	name := "7d"
	if w != nil {
		name = *w
	}
	d, ok := growthWindows[name]
	if !ok {
		return 0, badRequest("invalid window")
	}
	return d, nil
}

func (a *server) GetRanking(ctx context.Context, req oapi.GetRankingRequestObject) (oapi.GetRankingResponseObject, error) {
	sortBy := string(oapi.GetRankingParamsSortUsage)
	if req.Params.Sort != nil {
		sortBy = string(*req.Params.Sort)
		if !req.Params.Sort.Valid() {
			return nil, badRequest("invalid sort")
		}
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
	var rootID int64
	if req.Params.Root != nil {
		rootID = *req.Params.Root
	}
	rows, err := a.repo.Ranking(ctx, sortBy, d, rootID, a.now())
	if err != nil {
		return nil, err
	}
	out := oapi.GetRanking200JSONResponse{Items: make([]oapi.RankingRow, 0, len(rows))}
	for _, r := range rows {
		row := oapi.RankingRow{
			UserId: r.UserID, Username: r.Username, BytesAlloc: r.Total.BytesAlloc, BytesLogical: r.Total.BytesLogical,
			SharedHardlinkBytes: r.SharedHardlinkBytes, QuotaBytes: r.QuotaBytes, GrowthBytes: r.GrowthBytes,
		}
		if r.QuotaPct != nil {
			p := float32(*r.QuotaPct)
			row.QuotaPct = &p
		}
		out.Items = append(out.Items, row)
	}
	return out, nil
}

func (a *server) ListMounts(ctx context.Context, req oapi.ListMountsRequestObject) (oapi.ListMountsResponseObject, error) {
	var w *string
	if req.Params.Window != nil {
		s := string(*req.Params.Window)
		w = &s
	}
	d, err := growthWindow(w)
	if err != nil {
		return nil, err
	}
	rows, err := a.repo.Mounts(ctx, d, a.now())
	if err != nil {
		return nil, err
	}
	out := oapi.ListMounts200JSONResponse{Items: make([]oapi.MountStat, 0, len(rows))}
	for _, m := range rows {
		out.Items = append(out.Items, oapi.MountStat{
			RootId: m.RootID, RootName: m.RootName, BytesAlloc: m.BytesAlloc, GrowthBytes: m.GrowthBytes, FsTotal: m.FSTotal, FsFree: m.FSFree,
			ZfsUsed: m.ZFSUsed, ZfsAvail: m.ZFSAvail, ZfsQuota: m.ZFSQuota, ZfsUsedSnapshots: m.ZFSSnapshots,
		})
	}
	return out, nil
}

func (a *server) ListConfigIssues(ctx context.Context, _ oapi.ListConfigIssuesRequestObject) (oapi.ListConfigIssuesResponseObject, error) {
	issues, err := a.repo.ConfigIssues(ctx, a.now())
	if err != nil {
		return nil, err
	}
	out := oapi.ListConfigIssues200JSONResponse{Items: make([]oapi.ConfigIssue, 0, len(issues))}
	for _, i := range issues {
		d := map[string]interface{}{}
		if err := json.Unmarshal(i.Details, &d); err != nil {
			d = map[string]interface{}{}
		}
		out.Items = append(out.Items, oapi.ConfigIssue{
			Id: i.ID, RootId: i.RootID, Kind: oapi.ConfigIssueKind(i.Kind), Details: d, CreatedAt: i.CreatedAt,
		})
	}
	return out, nil
}
