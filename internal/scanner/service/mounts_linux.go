//go:build linux

package service

import (
	"slices"
	"strings"

	"github.com/vinRosso/hogwatch/internal/db"
	"github.com/vinRosso/hogwatch/internal/scanner/fsx"
)

// AvailableMounts lists the mounts the admin can pick a scan root from: every
// mount at or below prefix (the host's filesystem as seen in the scanner
// container) that is not a pseudo filesystem, sorted by container path.
// HostPath is the container path with prefix removed, "/" for prefix itself.
// The result is never nil, so it encodes as [] and not null.
func AvailableMounts(mounts []fsx.Mount, prefix string) []db.AvailableMount {
	p := strings.TrimRight(prefix, "/")
	out := []db.AvailableMount{}
	for _, m := range mounts {
		if m.MountPoint != p && !strings.HasPrefix(m.MountPoint, p+"/") {
			continue
		}
		if fsx.IsPseudoType(m.FSType) {
			continue
		}
		host := m.MountPoint[len(p):]
		if host == "" {
			host = "/"
		}
		out = append(out, db.AvailableMount{
			ContainerPath: m.MountPoint,
			HostPath:      host,
			FSType:        m.FSType,
			ReadOnly:      m.ReadOnly,
		})
	}
	slices.SortFunc(out, func(a, b db.AvailableMount) int {
		return strings.Compare(a.ContainerPath, b.ContainerPath)
	})
	return out
}
