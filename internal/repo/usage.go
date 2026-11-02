package repo

import (
	"context"
	"fmt"

	"github.com/vinRosso/hogwatch/internal/settings"
)

// Totals is a files / bytes triple.
type Totals struct{ Files, BytesLogical, BytesAlloc int64 }

// MountUsage is the usage of the caller on one root.
type MountUsage struct {
	RootID              int64
	RootName            string
	Usage               Totals
	SharedHardlinkBytes int64
	FSTotal, FSFree     *int64
	GrowthBytes         *int64
}

// PathUsage is the usage of one assigned path.
type PathUsage struct {
	UserPathID, RootID int64
	HostPath           []byte
	Found, Redundant   bool
	Usage              Totals
}

// MyUsage is the caller's usage against their quota.
type MyUsage struct {
	QuotaBytes  *int64
	QuotaBasis  string
	Total       Totals
	Mounts      []MountUsage
	Paths       []PathUsage
	GrowthBytes *int64
}

// MyUsage reports the totals of the caller over the current scan of each root.
// Snapshot space joins the allocated total only when quota.include_snapshots is
// true, and only for found, non-redundant paths. since is the growth cutoff
// (unix seconds): growth is the allocated figure now minus the latest history
// point at or before it, for the total (user rows) and each mount (user_mount rows).
func (r *Repo) MyUsage(ctx context.Context, c Caller, since int64) (MyUsage, error) {
	var out MyUsage
	q, err := r.q.GetUserQuota(ctx, c.UserID)
	if err != nil {
		return out, fmt.Errorf("repo: quota: %w", err)
	}
	out.QuotaBasis = q.QuotaBasis
	out.QuotaBytes = nullable(q.QuotaBytes.Valid, q.QuotaBytes.Int64)

	showCapacity := c.Admin || r.boolSetting(ctx, settings.UsersSeeMountCapacity)
	usage, err := r.q.ListUsageForUser(ctx, c.UserID)
	if err != nil {
		return out, fmt.Errorf("repo: usage: %w", err)
	}
	for _, u := range usage {
		m := MountUsage{
			RootID: u.RootID, RootName: u.RootName, SharedHardlinkBytes: u.SharedHardlinkBytes,
			Usage: Totals{Files: u.Files, BytesLogical: u.BytesLogical, BytesAlloc: u.BytesAlloc},
		}
		if showCapacity {
			ms, err := r.q.GetMountStats(ctx, u.ScanID)
			switch {
			case err == nil:
				total, free := ms.FsTotal, ms.FsFree
				m.FSTotal, m.FSFree = &total, &free
			case !isNoRows(err):
				return out, fmt.Errorf("repo: mount stats: %w", err)
			}
		}
		out.Mounts = append(out.Mounts, m)
		out.Total.Files += m.Usage.Files
		out.Total.BytesLogical += m.Usage.BytesLogical
		out.Total.BytesAlloc += m.Usage.BytesAlloc
	}

	mountBase, err := r.growthBasesForUser(ctx, "user_mount", c.UserID, since)
	if err != nil {
		return out, err
	}
	for i := range out.Mounts {
		if b, ok := mountBase[[2]int64{c.UserID, out.Mounts[i].RootID}]; ok {
			g := out.Mounts[i].Usage.BytesAlloc - b
			out.Mounts[i].GrowthBytes = &g
		}
	}

	paths, err := r.pathRows(ctx, c.UserID)
	if err != nil {
		return out, err
	}
	include := r.boolSetting(ctx, settings.IncludeSnapshots)
	var snapSum int64
	for _, p := range paths {
		out.Paths = append(out.Paths, PathUsage{
			UserPathID: p.id, RootID: p.rootID, HostPath: hostPath(p.host, p.rel), Found: p.found, Redundant: p.redundant,
			Usage: Totals{Files: p.files, BytesLogical: p.logical, BytesAlloc: p.alloc},
		})
		if p.found && !p.redundant {
			snapSum += p.snapshots
		}
	}
	// History rows are written without snapshot space, so growth is measured on
	// the figure before snapshots; adding them would show fake growth.
	preSnap := out.Total.BytesAlloc
	out.Total = withSnapshots(out.Total, snapSum, include)
	userBase, err := r.growthBasesForUser(ctx, "user", c.UserID, since)
	if err != nil {
		return out, err
	}
	if b, ok := userBase[[2]int64{c.UserID, 0}]; ok {
		g := preSnap - b
		out.GrowthBytes = &g
	}
	return out, nil
}
