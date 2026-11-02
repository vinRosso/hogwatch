package totals

import (
	"cmp"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"

	"github.com/vinRosso/hogwatch/internal/gen"
	"github.com/vinRosso/hogwatch/internal/noderef"
)

// maxIssueNodes caps IssueSummary.Nodes.
const maxIssueNodes = 10

// errnoEIO is EIO on Linux, the only errno reported as a config issue.
const errnoEIO = 5

// IssueSummary is the details of one scan-derived config issue: how many
// occurrences, and the first few as node references.
type IssueSummary struct {
	Count int64
	Nodes []string
}

// ScanIssues summarises the writable-mount refusals and the EIO errors of a
// generation. Either result is nil when its Count is 0. Nodes are
// "<scan_id>.<dir_id>" references ordered by directory id.
func ScanIssues(ctx context.Context, genDB *sql.DB, scanID int64) (writable, eio *IssueSummary, err error) {
	writable, err = summarise(ctx, genDB, scanID,
		`SELECT count(*) FROM dirs WHERE flags & ? != 0`,
		`SELECT id FROM dirs WHERE flags & ? != 0 ORDER BY id LIMIT ?`,
		int64(gen.FlagWritableMountRefused))
	if err != nil {
		return nil, nil, err
	}
	eio, err = summarise(ctx, genDB, scanID,
		`SELECT count(*) FROM errors WHERE errno = ?`,
		`SELECT DISTINCT dir_id FROM errors WHERE errno = ? ORDER BY dir_id LIMIT ?`,
		errnoEIO)
	if err != nil {
		return nil, nil, err
	}
	return writable, eio, nil
}

func summarise(ctx context.Context, genDB *sql.DB, scanID int64, countQ, nodesQ string, arg int64) (*IssueSummary, error) {
	var n int64
	if err := genDB.QueryRowContext(ctx, countQ, arg).Scan(&n); err != nil {
		return nil, fmt.Errorf("totals: scan issues: %w", err)
	}
	if n == 0 {
		return nil, nil
	}
	rs, err := genDB.QueryContext(ctx, nodesQ, arg, maxIssueNodes)
	if err != nil {
		return nil, fmt.Errorf("totals: scan issues: %w", err)
	}
	defer func() { _ = rs.Close() }()
	s := &IssueSummary{Count: n}
	for rs.Next() {
		var id int64
		if err := rs.Scan(&id); err != nil {
			return nil, fmt.Errorf("totals: scan issues: %w", err)
		}
		s.Nodes = append(s.Nodes, noderef.Ref{ScanID: scanID, DirID: id}.String())
	}
	if err := rs.Err(); err != nil {
		return nil, fmt.Errorf("totals: scan issues: %w", err)
	}
	return s, nil
}

// RootedPath is an assigned path together with the root it is assigned on.
type RootedPath struct {
	Path
	RootID int64
}

// Duplicate says that the directory of an assigned path is also an entered
// directory of another root's current generation: the same bytes would
// be counted under two roots.
type Duplicate struct {
	UserPathID, OtherRootID int64
	Node                    string
}

// skippedFlags marks rows whose directory was not entered.
const skippedFlags = gen.FlagMountpointSkipped | gen.FlagNestedRootSkipped |
	gen.FlagWritableMountRefused | gen.FlagExcluded

// CrossRootDuplicates finds assigned paths whose (dev, ino) appears as an
// entered directory in the current generation of another root. gens and scans
// are keyed by root id. Each path is resolved in its own root's generation,
// then every other generation is streamed once. Paths missing from their own
// generation, or whose root has no generation, are skipped. The result is
// ordered by path id, then other root id.
func CrossRootDuplicates(ctx context.Context, gens map[int64]*sql.DB, scans map[int64]int64, paths []RootedPath) ([]Duplicate, error) {
	type key struct{ dev, ino int64 }
	byKey := map[key][]RootedPath{}
	for _, p := range paths {
		g, ok := gens[p.RootID]
		if !ok {
			continue
		}
		d, err := gen.Lookup(ctx, g, segments(p.Rel))
		switch {
		case errors.Is(err, gen.ErrNoDir):
			continue
		case err != nil:
			return nil, fmt.Errorf("totals: cross-root: path %d: %w", p.ID, err)
		}
		k := key{d.Dev, d.Ino}
		byKey[k] = append(byKey[k], p)
	}
	if len(byKey) == 0 {
		return nil, nil
	}

	roots := make([]int64, 0, len(gens))
	for r := range gens {
		roots = append(roots, r)
	}
	slices.Sort(roots)

	var out []Duplicate
	for _, other := range roots {
		seen := map[int64]bool{} // user path id: first (lowest) dir id wins
		rs, err := gens[other].QueryContext(ctx,
			`SELECT id, dev, ino FROM dirs WHERE flags & ? = 0 ORDER BY id`, int64(skippedFlags))
		if err != nil {
			return nil, fmt.Errorf("totals: cross-root: root %d: %w", other, err)
		}
		for rs.Next() {
			var id, dev, ino int64
			if err := rs.Scan(&id, &dev, &ino); err != nil {
				_ = rs.Close()
				return nil, fmt.Errorf("totals: cross-root: root %d: %w", other, err)
			}
			for _, p := range byKey[key{dev, ino}] {
				if p.RootID == other || seen[p.ID] {
					continue
				}
				seen[p.ID] = true
				out = append(out, Duplicate{
					UserPathID: p.ID, OtherRootID: other,
					Node: noderef.Ref{ScanID: scans[other], DirID: id}.String(),
				})
			}
		}
		err = rs.Err()
		_ = rs.Close()
		if err != nil {
			return nil, fmt.Errorf("totals: cross-root: root %d: %w", other, err)
		}
	}
	slices.SortFunc(out, func(a, b Duplicate) int {
		return cmp.Or(cmp.Compare(a.UserPathID, b.UserPathID), cmp.Compare(a.OtherRootID, b.OtherRootID))
	})
	return out, nil
}
