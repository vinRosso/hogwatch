package repo

import (
	"context"
	"errors"
	"fmt"

	"github.com/vinRosso/hogwatch/internal/settings"

	"github.com/vinRosso/hogwatch/internal/noderef"
)

// Anchor is a place a caller can start browsing a root: the root itself for an
// admin, each effective assigned path for a user. Ref is nil when the path does
// not exist in the current scan.
type Anchor struct {
	Ref      *noderef.Ref
	HostPath []byte
	Found    bool
}

// RootInfo is a root as the caller sees it.
type RootInfo struct {
	ID            int64
	Name          string
	LastScannedAt *int64
	Scanning      bool
	FSTotal       *int64
	FSFree        *int64
	Anchors       []Anchor
}

type rootRow struct {
	id      int64
	name    string
	host    string
	current *int64
}

// Roots lists the roots the caller can browse: every root for an admin, the
// roots with a path of the user otherwise.
func (r *Repo) Roots(ctx context.Context, c Caller) ([]RootInfo, error) {
	var rows []rootRow
	if c.Admin {
		all, err := r.q.ListRootsAll(ctx)
		if err != nil {
			return nil, fmt.Errorf("repo: roots: %w", err)
		}
		for _, x := range all {
			rows = append(rows, rootRow{x.ID, x.Name, x.HostPath, nullable(x.CurrentScanID.Valid, x.CurrentScanID.Int64)})
		}
	} else {
		mine, err := r.q.ListRootsForUser(ctx, c.UserID)
		if err != nil {
			return nil, fmt.Errorf("repo: roots: %w", err)
		}
		for _, x := range mine {
			rows = append(rows, rootRow{x.ID, x.Name, x.HostPath, nullable(x.CurrentScanID.Valid, x.CurrentScanID.Int64)})
		}
	}
	showCapacity := c.Admin || r.boolSetting(ctx, settings.UsersSeeMountCapacity)
	var paths []pathRow
	if !c.Admin {
		var err error
		if paths, err = r.pathRows(ctx, c.UserID); err != nil {
			return nil, err
		}
	}
	out := make([]RootInfo, 0, len(rows))
	for _, rt := range rows {
		info := RootInfo{ID: rt.id, Name: rt.name, Anchors: []Anchor{}}
		running, err := r.q.RootScanning(ctx, rt.id)
		if err != nil {
			return nil, fmt.Errorf("repo: roots: %w", err)
		}
		info.Scanning = running != 0
		if rt.current != nil {
			if err := r.currentScanInfo(ctx, &info, *rt.current, showCapacity); err != nil {
				return nil, err
			}
		}
		if c.Admin {
			a := Anchor{HostPath: []byte(rt.host)}
			if rt.current != nil {
				a.Ref, a.Found = r.anchorRef(ctx, ScopeAll(), *rt.current, nil)
			}
			info.Anchors = append(info.Anchors, a)
		} else {
			var scope Scope
			haveScope := false
			for _, p := range paths {
				if p.rootID != rt.id || p.redundant {
					continue
				}
				a := Anchor{HostPath: hostPath(rt.host, p.rel)}
				if rt.current != nil && p.found {
					if !haveScope {
						var err error
						if scope, err = r.ScopeFor(ctx, c.UserID, *rt.current); err != nil && !errors.Is(err, ErrNotFound) {
							return nil, err
						}
						haveScope = true
					}
					a.Ref, a.Found = r.anchorRef(ctx, scope, *rt.current, p.rel)
				}
				info.Anchors = append(info.Anchors, a)
			}
		}
		out = append(out, info)
	}
	return out, nil
}

func nullable(valid bool, v int64) *int64 {
	if !valid {
		return nil
	}
	return &v
}

// currentScanInfo fills the fields that come from the current scan.
func (r *Repo) currentScanInfo(ctx context.Context, info *RootInfo, scanID int64, showCapacity bool) error {
	sum, err := r.q.GetScanSummary(ctx, scanID)
	if err == nil {
		info.LastScannedAt = nullable(sum.FinishedAt.Valid, sum.FinishedAt.Int64)
	} else if !isNoRows(err) {
		return fmt.Errorf("repo: scan %d: %w", scanID, err)
	}
	if !showCapacity {
		return nil
	}
	m, err := r.q.GetMountStats(ctx, scanID)
	switch {
	case err == nil:
		total, free := m.FsTotal, m.FsFree
		info.FSTotal, info.FSFree = &total, &free
	case !isNoRows(err):
		return fmt.Errorf("repo: mount stats %d: %w", scanID, err)
	}
	return nil
}

// anchorRef resolves rel in the scan within s; a path that is not there, or a
// scan that cannot be served, is found = false.
func (r *Repo) anchorRef(ctx context.Context, s Scope, scanID int64, rel []byte) (*noderef.Ref, bool) {
	ref, err := r.Resolve(ctx, s, scanID, rel)
	if err != nil {
		if !errors.Is(err, ErrNotFound) {
			r.log.Error("resolve anchor", "scan", scanID, "err", err)
		}
		return nil, false
	}
	return &ref, true
}
