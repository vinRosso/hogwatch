//go:build linux

package service

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"

	"github.com/vinRosso/hogwatch/internal/db"
	"github.com/vinRosso/hogwatch/internal/db/dbq"
	"github.com/vinRosso/hogwatch/internal/gen"
	"github.com/vinRosso/hogwatch/internal/scanner/build"
	"github.com/vinRosso/hogwatch/internal/totals"
	"github.com/vinRosso/hogwatch/internal/zfs"
)

// publishRetries is how many times a publish that hit SQLITE_BUSY or
// SQLITE_LOCKED is retried, waiting PublishBackoff, twice that, and so on.
const publishRetries = 5

// publication is everything one publish transaction writes, computed from the
// finished generation before the transaction starts, so the transaction
// holds the write lock only for inserts.
type publication struct {
	root   dbq.ScanRoot
	scanID int64
	jobID  int64
	now    time.Time

	finish dbq.FinishScanParams // status done, with the totals
	mount  dbq.InsertMountStatsParams
	usage  []totals.UsageRow
	// userPaths is the set of the root's user_paths that usage and paths were
	// computed from. publishOnce compares it with the table inside its
	// transaction; with lenient set it skips path rows whose path vanished
	// instead.
	userPaths []dbq.ListUserPathsByRootRow
	lenient   bool
	paths     []totals.PathRow
	zfs       map[int64]totals.PathZFS // by user path id
	hdirs     []totals.HistoryDir
	// rootTotals are the root dir row's totals, the mount history row.
	rootLogical, rootAlloc, rootFiles int64

	writable, eio *totals.IssueSummary
	dups          []dupIssue
}

// dupIssue is a cross-root duplicate with the root and scan of its path.
type dupIssue struct {
	totals.Duplicate
	rootID int64
	scanID sql.NullInt64
}

// preparePublication reads the finished generation (and the current
// generations of the other roots) and computes usage, path rows, mount
// stats, history rows and config issues.
func (s *service) preparePublication(ctx context.Context, root dbq.ScanRoot, scanID, jobID int64, res build.Result) (*publication, error) {
	gdb, _, err := gen.OpenReadOnly(ctx, res.Path)
	if err != nil {
		return nil, fmt.Errorf("open generation: %w", err)
	}
	defer func() { _ = gdb.Close() }()

	p := &publication{root: root, scanID: scanID, jobID: jobID, now: s.o.Now()}

	rootDir, err := gen.DirByID(ctx, gdb, 1)
	if err != nil {
		return nil, fmt.Errorf("read the root row: %w", err)
	}
	p.rootLogical, p.rootAlloc, p.rootFiles = rootDir.BytesLogical, rootDir.BytesAlloc, rootDir.Files
	var topRows int64
	if err := gdb.QueryRowContext(ctx, `SELECT count(*) FROM top_files`).Scan(&topRows); err != nil {
		return nil, fmt.Errorf("count top files: %w", err)
	}
	p.finish = dbq.FinishScanParams{
		Status: string(db.ScanDone), FinishedAt: sql.NullInt64{Int64: p.now.Unix(), Valid: true},
		Dirs: res.Stats.DirRows, Files: rootDir.Files, BytesLogical: rootDir.BytesLogical,
		BytesAlloc: rootDir.BytesAlloc, ErrorCount: res.Stats.Walk.Errors, TopNRows: topRows, ID: scanID,
	}

	rows, err := s.q.ListUserPathsByRoot(ctx, root.ID)
	if err != nil {
		return nil, fmt.Errorf("list user paths: %w", err)
	}
	p.userPaths = rows
	paths := make([]totals.Path, len(rows))
	for i, r := range rows {
		paths[i] = totals.Path{ID: r.ID, UserID: r.UserID, Rel: string(r.RelPath)}
	}
	if p.usage, p.paths, err = totals.Usage(ctx, gdb, paths); err != nil {
		return nil, err
	}

	byMount := map[string]zfs.Dataset{}
	if ds, err := zfs.Load(s.o.ZFSFile); err != nil {
		s.o.Log.Warn("ignoring ZFS numbers: cannot parse the zfs file", "file", s.o.ZFSFile, "err", err)
	} else {
		byMount = zfs.ByMountpoint(ds)
	}
	p.zfs = map[int64]totals.PathZFS{}
	for _, z := range totals.ApplyZFS(p.paths, paths, root.HostPath, byMount) {
		p.zfs[z.UserPathID] = z
	}
	p.mount = dbq.InsertMountStatsParams{ScanID: scanID, RootID: root.ID, FsTotal: res.FSTotal, FsFree: res.FSAvail}
	if d, ok := byMount[root.HostPath]; ok {
		p.mount.ZfsUsed = sql.NullInt64{Int64: d.Used, Valid: true}
		p.mount.ZfsAvail = sql.NullInt64{Int64: d.Avail, Valid: true}
		p.mount.ZfsUsedSnapshots = sql.NullInt64{Int64: d.UsedSnapshots, Valid: true}
		if q := d.EffectiveQuota(); q != nil {
			p.mount.ZfsQuota = sql.NullInt64{Int64: *q, Valid: true}
		}
	}

	if p.writable, p.eio, err = totals.ScanIssues(ctx, gdb, scanID); err != nil {
		return nil, err
	}
	if p.hdirs, err = totals.HistoryDirs(ctx, gdb); err != nil {
		return nil, err
	}
	if p.dups, err = s.crossRoot(ctx, root, scanID, gdb); err != nil {
		return nil, err
	}
	return p, nil
}

// crossRoot finds the cross-root duplicates over the current generation of
// every root, with this root's new generation substituted for its current one.
func (s *service) crossRoot(ctx context.Context, root dbq.ScanRoot, scanID int64, gdb *sql.DB) ([]dupIssue, error) {
	all, err := s.q.ListAllUserPaths(ctx)
	if err != nil {
		return nil, fmt.Errorf("list all user paths: %w", err)
	}
	rooted := make([]totals.RootedPath, len(all))
	for i, r := range all {
		rooted[i] = totals.RootedPath{Path: totals.Path{ID: r.ID, UserID: r.UserID, Rel: string(r.RelPath)}, RootID: r.RootID}
	}
	roots, err := s.q.ListRoots(ctx)
	if err != nil {
		return nil, fmt.Errorf("list roots: %w", err)
	}
	gens := map[int64]*sql.DB{root.ID: gdb}
	scans := map[int64]int64{root.ID: scanID}
	for _, r := range roots {
		if r.ID == root.ID || !r.CurrentScanID.Valid {
			continue
		}
		g, _, err := gen.OpenReadOnly(ctx, gen.Path(s.o.DataDir, r.ID, r.CurrentScanID.Int64))
		if err != nil {
			s.o.Log.Warn("cross-root check skips a root: cannot open its current generation", "root", r.ID, "err", err)
			continue
		}
		defer func() { _ = g.Close() }()
		gens[r.ID] = g
		scans[r.ID] = r.CurrentScanID.Int64
	}
	dups, err := totals.CrossRootDuplicates(ctx, gens, scans, rooted)
	if err != nil {
		return nil, err
	}
	rootOf := make(map[int64]int64, len(all))
	for _, r := range all {
		rootOf[r.ID] = r.RootID
	}
	out := make([]dupIssue, len(dups))
	for i, d := range dups {
		rid := rootOf[d.UserPathID]
		sc, ok := scans[rid]
		out[i] = dupIssue{Duplicate: d, rootID: rid, scanID: sql.NullInt64{Int64: sc, Valid: ok}}
	}
	return out, nil
}

// publishScan runs the publish transaction with the busy retry, then
// retention. On failure the caller removes the generation and fails the scan.
func (s *service) publishScan(ctx context.Context, p *publication) error {
	if h := s.o.hooks.beforePublish; h != nil {
		h()
	}
	attempt := s.publishOnce
	if h := s.o.hooks.publish; h != nil {
		attempt = func(ctx context.Context, p *publication) error { return h(ctx, s, p) }
	}
	delay := s.o.PublishBackoff
	var err error
	for retry := 0; ; retry++ {
		if err = attempt(ctx, p); err == nil {
			break
		}
		if !isBusy(err) || retry >= publishRetries || ctx.Err() != nil {
			return err
		}
		s.o.Log.Warn("publish busy, retrying", "scan", p.scanID, "retry", retry+1, "wait", delay, "err", err)
		select {
		case <-ctx.Done():
			return err
		case <-time.After(delay):
		}
		delay *= 2
	}

	// The scan is published. Retention is housekeeping in its own transaction:
	// a failure is logged and the next publish retries it.
	if err := s.retain(context.WithoutCancel(ctx), p.now); err != nil {
		s.o.Log.Error("history retention failed", "err", err)
	}
	return nil
}

func (s *service) retain(ctx context.Context, now time.Time) error {
	tx, err := s.o.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := totals.Retain(ctx, tx, now); err != nil {
		return err
	}
	return tx.Commit()
}

// isBusy reports whether err is SQLITE_BUSY or SQLITE_LOCKED, which a later
// attempt can overcome.
func isBusy(err error) bool {
	var se *sqlite.Error
	if !errors.As(err, &se) {
		return false
	}
	c := se.Code() & 0xff
	return c == sqlite3.SQLITE_BUSY || c == sqlite3.SQLITE_LOCKED
}

// errPathsChanged reports that the root's user_paths changed after usage was
// computed. The caller recomputes and tries again.
var errPathsChanged = errors.New("user paths changed during publish")

// usageOfStablePaths drops the usage rows of every user whose set of paths
// differs between what usage was computed from and what the table holds now.
// Their totals would include a deleted path or miss a new one; the next scan
// publishes them again. It also returns the ids of those users, whose history
// rows must be skipped too.
func usageOfStablePaths(usage []totals.UsageRow, was, now []dbq.ListUserPathsByRootRow) ([]totals.UsageRow, map[int64]bool) {
	key := func(r dbq.ListUserPathsByRootRow) string {
		return strconv.FormatInt(r.ID, 10) + ":" + string(r.RelPath)
	}
	seen := map[string]int{}
	for _, r := range was {
		seen[key(r)]++
	}
	changed := map[int64]bool{}
	for _, r := range now {
		if seen[key(r)] == 0 {
			changed[r.UserID] = true
		}
		seen[key(r)]--
	}
	for _, r := range was {
		if seen[key(r)] > 0 {
			changed[r.UserID] = true
		}
	}
	var out []totals.UsageRow
	for _, u := range usage {
		if !changed[u.UserID] {
			out = append(out, u)
		}
	}
	return out, changed
}

func samePaths(a, b []dbq.ListUserPathsByRootRow) bool {
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

// publishOnce is one attempt of the publish transaction (plan, Definitions):
// one transaction inserts the scan's totals and history, replaces the root's
// scan-derived config issues, and sets the scan done, the root's current scan
// and the job done. Any failure rolls all of it back.
func (s *service) publishOnce(ctx context.Context, p *publication) (err error) {
	tx, err := s.o.DB.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin publish: %w", err)
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()
	q := s.q.WithTx(tx)
	ts := p.now.Unix()
	root := p.root.ID

	// The transaction holds the write lock, so this set cannot change before
	// commit.
	cur, err := q.ListUserPathsByRoot(ctx, root)
	if err != nil {
		return fmt.Errorf("re-read user paths: %w", err)
	}
	alive := make(map[int64]bool, len(cur))
	for _, c := range cur {
		alive[c.ID] = true
	}
	if !p.lenient && !samePaths(p.userPaths, cur) {
		return errPathsChanged
	}
	usage := p.usage
	var dropped map[int64]bool // lenient only: users whose usage was dropped
	if p.lenient {
		usage, dropped = usageOfStablePaths(p.usage, p.userPaths, cur)
	}

	for _, u := range usage {
		err = q.InsertUsage(ctx, dbq.InsertUsageParams{
			ScanID: p.scanID, UserID: u.UserID, RootID: root, BytesLogical: u.BytesLogical,
			BytesAlloc: u.BytesAlloc, Files: u.Files, SharedHardlinkBytes: u.SharedHardlinkBytes,
		})
		if err != nil {
			return fmt.Errorf("insert usage: %w", err)
		}
	}
	if err = q.InsertMountStats(ctx, p.mount); err != nil {
		return fmt.Errorf("insert mount stats: %w", err)
	}
	for _, r := range p.paths {
		if !alive[r.UserPathID] {
			continue // lenient only: the path vanished after usage was computed
		}
		ps := dbq.InsertPathStatsParams{
			ScanID: p.scanID, UserPathID: r.UserPathID, Found: b2i(r.Found), Redundant: b2i(r.Redundant),
			BytesLogical: r.BytesLogical, BytesAlloc: r.BytesAlloc, Files: r.Files, SharedHardlinkBytes: r.SharedHardlinkBytes,
		}
		if z, ok := p.zfs[r.UserPathID]; ok {
			ps.ZfsDataset = sql.NullString{String: z.Dataset, Valid: true}
			ps.ZfsUsed = sql.NullInt64{Int64: z.Used, Valid: true}
			ps.ZfsAvail = sql.NullInt64{Int64: z.Avail, Valid: true}
			ps.ZfsUsedSnapshots = sql.NullInt64{Int64: z.UsedSnapshots, Valid: true}
			if z.Quota != nil {
				ps.ZfsQuota = sql.NullInt64{Int64: *z.Quota, Valid: true}
			}
		}
		if err = q.InsertPathStats(ctx, ps); err != nil {
			return fmt.Errorf("insert path stats: %w", err)
		}
	}

	var n int64
	if n, err = q.FinishScan(ctx, p.finish); err != nil {
		return fmt.Errorf("finish scan: %w", err)
	}
	if n != 1 {
		return errors.New("scan is no longer running")
	}
	if err = q.SetCurrentScan(ctx, dbq.SetCurrentScanParams{ScanID: sql.NullInt64{Int64: p.scanID, Valid: true}, RootID: root}); err != nil {
		return fmt.Errorf("set current scan: %w", err)
	}

	if err = s.writeIssues(ctx, q, p); err != nil {
		return err
	}

	// History. The user rows sum every root's current scan, which now includes
	// this one because the usage rows and the current scan are set above. A user
	// whose usage was dropped gets no user and no user_mount row.
	hist := func(scope string, user, rootID sql.NullInt64, logical, alloc, files int64) error {
		return q.InsertHistoryTotal(ctx, dbq.InsertHistoryTotalParams{
			Ts: ts, Scope: scope, UserID: user, RootID: rootID, BytesLogical: logical, BytesAlloc: alloc, Files: files,
		})
	}
	rid := sql.NullInt64{Int64: root, Valid: true}
	if err = hist("mount", sql.NullInt64{}, rid, p.rootLogical, p.rootAlloc, p.rootFiles); err != nil {
		return fmt.Errorf("insert mount history: %w", err)
	}
	for _, u := range usage {
		if err = hist("user_mount", sql.NullInt64{Int64: u.UserID, Valid: true}, rid, u.BytesLogical, u.BytesAlloc, u.Files); err != nil {
			return fmt.Errorf("insert user_mount history: %w", err)
		}
	}
	sums, err := q.SumCurrentUsageByUser(ctx)
	if err != nil {
		return fmt.Errorf("sum current usage: %w", err)
	}
	for _, u := range sums {
		if dropped[u.UserID] {
			continue // the sum misses this root: it would record a drop that did not happen
		}
		if err = hist("user", sql.NullInt64{Int64: u.UserID, Valid: true}, sql.NullInt64{}, u.BytesLogical, u.BytesAlloc, u.Files); err != nil {
			return fmt.Errorf("insert user history: %w", err)
		}
	}
	for _, d := range p.hdirs {
		if err = q.InsertHistoryDir(ctx, dbq.InsertHistoryDirParams{Ts: ts, RootID: root, PathHash: d.PathHash, BytesAlloc: d.BytesAlloc, Files: d.Files}); err != nil {
			return fmt.Errorf("insert history dir: %w", err)
		}
		if err = q.UpsertHistoryPath(ctx, dbq.UpsertHistoryPathParams{PathHash: d.PathHash, RootID: root, RelPath: d.Rel}); err != nil {
			return fmt.Errorf("upsert history path: %w", err)
		}
	}

	n, err = q.FinishJob(ctx, dbq.FinishJobParams{
		Status: string(db.StatusDone), FinishedAt: sql.NullInt64{Int64: ts, Valid: true}, ID: p.jobID,
	})
	if err != nil {
		return fmt.Errorf("finish job: %w", err)
	}
	if n != 1 {
		return errors.New("job is no longer running")
	}
	if err = tx.Commit(); err != nil {
		return fmt.Errorf("commit publish: %w", err)
	}
	return nil
}

// writeIssues replaces this root's writable_mount and eio issues and every
// cross_root_duplicate issue (that one is computed globally).
func (s *service) writeIssues(ctx context.Context, q *dbq.Queries, p *publication) error {
	ts := p.now.Unix()
	root := p.root.ID
	scan := sql.NullInt64{Int64: p.scanID, Valid: true}
	add := func(rootID int64, scanID sql.NullInt64, kind db.IssueKind, details any) error {
		js, err := json.Marshal(details)
		if err != nil {
			return err
		}
		return q.InsertIssue(ctx, dbq.InsertIssueParams{RootID: rootID, ScanID: scanID, Kind: string(kind), Details: string(js), CreatedAt: ts})
	}
	for _, k := range []struct {
		kind db.IssueKind
		sum  *totals.IssueSummary
	}{{db.IssueWritableMount, p.writable}, {db.IssueEIO, p.eio}} {
		if err := q.DeleteIssues(ctx, dbq.DeleteIssuesParams{RootID: root, Kind: string(k.kind)}); err != nil {
			return fmt.Errorf("delete %s issues: %w", k.kind, err)
		}
		if k.sum == nil {
			continue
		}
		if err := add(root, scan, k.kind, db.NodesIssueDetails{Nodes: k.sum.Nodes, Count: k.sum.Count}); err != nil {
			return fmt.Errorf("insert %s issue: %w", k.kind, err)
		}
	}
	if err := q.DeleteIssuesByKind(ctx, string(db.IssueCrossRootDuplicate)); err != nil {
		return fmt.Errorf("delete cross_root_duplicate issues: %w", err)
	}
	for _, d := range p.dups {
		err := add(d.rootID, d.scanID, db.IssueCrossRootDuplicate,
			db.CrossRootDuplicateDetails{UserPathID: d.UserPathID, OtherRootID: d.OtherRootID, Node: d.Node})
		if err != nil {
			return fmt.Errorf("insert cross_root_duplicate issue: %w", err)
		}
	}
	return nil
}

func b2i(b bool) int64 {
	if b {
		return 1
	}
	return 0
}
