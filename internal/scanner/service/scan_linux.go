//go:build linux

package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"time"

	"github.com/vinRosso/hogwatch/internal/settings"

	"golang.org/x/sys/unix"

	"github.com/vinRosso/hogwatch/internal/cube"
	"github.com/vinRosso/hogwatch/internal/db"
	"github.com/vinRosso/hogwatch/internal/db/dbq"
	"github.com/vinRosso/hogwatch/internal/gen"
	"github.com/vinRosso/hogwatch/internal/pathx"
	"github.com/vinRosso/hogwatch/internal/scanner/agg"
	"github.com/vinRosso/hogwatch/internal/scanner/build"
	"github.com/vinRosso/hogwatch/internal/scanner/fsx"
	"github.com/vinRosso/hogwatch/internal/scanner/walk"
)

// freeSpaceFactor is the share of the current generation's size that must be
// free in the data dir before a scan starts: need = ceil(size * 6 / 5).
const (
	freeSpaceNum = 6
	freeSpaceDen = 5
)

// startScan is the poll loop's startScan hook: it claims one queued scan job
// and spawns its pipeline at once, so a cancel job can find the running
// target on the same or the next poll.
func startScan(ctx context.Context, s *service) bool {
	j, err := s.q.ClaimJob(ctx, dbq.ClaimJobParams{
		Kind: kindScan, StartedAt: sql.NullInt64{Int64: s.o.Now().Unix(), Valid: true},
	})
	if errors.Is(err, sql.ErrNoRows) {
		return false
	}
	if err != nil {
		if ctx.Err() == nil {
			s.o.Log.Error("claim scan job", "err", err)
		}
		return false
	}
	s.spawn(ctx, kindScan, j.ID, func(jctx context.Context) {
		if err := s.runScan(jctx, j); err != nil {
			// runScan has written the terminal status; this is only the log.
			s.o.Log.Warn("scan did not finish", "job", j.ID, "root", j.RootID, "err", err)
		}
	})
	return true
}

// scanRun is the state of one runScan.
type scanRun struct {
	s      *service
	job    dbq.Job
	scanID int64       // 0 until the scans row exists
	last   db.Progress // the last walk progress, for the later phases
}

// runScan runs the whole pipeline of one claimed scan job and sets the job's
// terminal status itself:
//
//   - success: the publish transaction sets scan and job done;
//   - a cancel job (the context's cause is errUserCancel): scan and job
//     cancelled, the partial generation removed;
//   - any other cancellation, the service shutting down: scan and job are
//     failed with "scanner stopped" and the partial generation is removed, so
//     nothing is left running for Recover to find;
//   - any other error: scan and job failed with the error text.
//
// The terminal writes use a context that is never cancelled. The returned
// error is the cause (nil only after a published scan).
func (s *service) runScan(ctx context.Context, job dbq.Job) error {
	r := &scanRun{s: s, job: job}
	err := r.run(ctx)
	if err != nil {
		r.fail(ctx, err)
	}
	return err
}

func (r *scanRun) fail(ctx context.Context, cause error) {
	s := r.s
	status, msg := db.StatusFailed, cause.Error()
	if ctx.Err() != nil {
		if errors.Is(context.Cause(ctx), errUserCancel) {
			status, msg = db.StatusCancelled, ""
		} else {
			msg = "scanner stopped"
		}
	}
	text := sql.NullString{String: msg, Valid: msg != ""}
	fctx := context.WithoutCancel(ctx)
	now := sql.NullInt64{Int64: s.o.Now().Unix(), Valid: true}

	if r.scanID != 0 {
		if err := os.Remove(gen.Path(s.o.DataDir, r.job.RootID, r.scanID)); err != nil && !errors.Is(err, fs.ErrNotExist) {
			s.o.Log.Error("cannot delete the generation of a failed scan", "scan", r.scanID, "err", err)
		}
		scanStatus := db.ScanFailed
		if status == db.StatusCancelled {
			scanStatus = db.ScanCancelled
		}
		if _, err := s.q.FinishScan(fctx, dbq.FinishScanParams{Status: string(scanStatus), FinishedAt: now, Error: text, ID: r.scanID}); err != nil {
			s.o.Log.Error("cannot finish a scan row", "scan", r.scanID, "err", err)
		}
	}
	if _, err := s.q.FinishJob(fctx, dbq.FinishJobParams{Status: string(status), Error: text, FinishedAt: now, ID: r.job.ID}); err != nil {
		s.o.Log.Error("cannot finish a scan job", "job", r.job.ID, "err", err)
	}
}

func (r *scanRun) run(ctx context.Context) error {
	s := r.s
	o := s.o

	root, err := s.q.GetRoot(ctx, r.job.RootID)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return errors.New("root disabled")
	case err != nil:
		return fmt.Errorf("load root: %w", err)
	case root.Enabled == 0:
		return errors.New("root disabled")
	}
	if err := s.checkFreeSpace(root); err != nil {
		return err
	}

	var p db.ScanParams
	_ = json.Unmarshal([]byte(r.job.Params), &p)
	trigger := p.Trigger
	if trigger != "schedule" {
		trigger = "manual"
	}
	prevFiles := s.currentFiles(ctx, root)

	r.scanID, err = s.q.InsertScan(ctx, dbq.InsertScanParams{
		RootID: root.ID, JobID: sql.NullInt64{Int64: r.job.ID, Valid: true},
		Trigger: trigger, StartedAt: o.Now().Unix(),
	})
	if err != nil {
		r.scanID = 0
		return fmt.Errorf("insert scan: %w", err)
	}
	// The file name needs the scan id, which InsertScan returns.
	file := filepath.Base(gen.Path(o.DataDir, root.ID, r.scanID))
	if _, err := o.DB.ExecContext(ctx, `UPDATE scans SET file = ? WHERE id = ?`, file, r.scanID); err != nil {
		return fmt.Errorf("set scan file: %w", err)
	}

	wo, err := s.walkOptions(ctx, root)
	if err != nil {
		return err
	}
	started := time.Now()
	wo.ProgressEvery = o.ProgressEvery
	wo.Progress = func(st walk.Stats) { r.walkProgress(ctx, st, started, prevFiles) }

	res, err := build.Run(ctx, build.Options{
		DataDir: o.DataDir, RootID: root.ID, ScanID: r.scanID, Root: root.ContainerPath,
		Walk: wo, ScannerVersion: o.Version, Now: o.Now,
		OnWalk:     func() { r.phaseHook("walk") },
		OnFinalize: func() { r.writePhase(ctx, "finalize"); r.phaseHook("finalize") },
	})
	if err != nil {
		return err
	}

	r.writePhase(ctx, "publish")
	pub, err := s.preparePublication(ctx, root, r.scanID, r.job.ID, res)
	if err != nil {
		return err
	}
	err = s.publishScan(ctx, pub)
	if !errors.Is(err, errPathsChanged) {
		return err
	}
	// A user path changed while the generation was being summed. Sum again
	// against the new set; on a second change publish what still exists.
	s.o.Log.Info("user paths changed during publish, recomputing", "scan", r.scanID)
	if pub, err = s.preparePublication(ctx, root, r.scanID, r.job.ID, res); err != nil {
		return err
	}
	pub.lenient = true
	return s.publishScan(ctx, pub)
}

// walkProgress is the walk's progress callback: it writes jobs.progress with
// the rate and, when the previous scan saw more files, an ETA.
func (r *scanRun) walkProgress(ctx context.Context, st walk.Stats, started time.Time, prevFiles int64) {
	p := db.Progress{
		Dirs: st.Dirs, Files: st.Files, BytesLogical: st.BytesLogical, BytesAlloc: st.BytesAlloc,
		Errors: st.Errors, Phase: "walk",
	}
	if elapsed := time.Since(started).Seconds(); elapsed > 0 {
		p.RatePerSec = float64(st.Files) / elapsed
	}
	if prevFiles > st.Files && p.RatePerSec > 0 {
		eta := int64(float64(prevFiles-st.Files) / p.RatePerSec)
		p.ETASeconds = &eta
	}
	r.last = p
	r.writeProgress(ctx, p)
	if h := r.s.o.hooks.onProgress; h != nil {
		h(ctx, p)
	}
}

// writePhase rewrites the last walk progress with another phase.
func (r *scanRun) writePhase(ctx context.Context, phase string) {
	p := r.last
	p.Phase = phase
	p.ETASeconds = nil
	r.writeProgress(ctx, p)
}

// writeProgress stores p in jobs.progress. A failure is only logged: progress
// is informational and the scan must not stop for it.
func (r *scanRun) writeProgress(ctx context.Context, p db.Progress) {
	js, err := json.Marshal(p)
	if err == nil {
		err = r.s.q.UpdateJobProgress(ctx, dbq.UpdateJobProgressParams{Progress: sql.NullString{String: string(js), Valid: true}, ID: r.job.ID})
	}
	if err != nil && ctx.Err() == nil {
		r.s.o.Log.Warn("cannot write job progress", "job", r.job.ID, "err", err)
	}
}

// currentFiles is the file count of the root's current scan, 0 if it has none.
func (s *service) currentFiles(ctx context.Context, root dbq.ScanRoot) int64 {
	if !root.CurrentScanID.Valid {
		return 0
	}
	var n int64
	if err := s.o.DB.QueryRowContext(ctx, `SELECT files FROM scans WHERE id = ?`, root.CurrentScanID.Int64).Scan(&n); err != nil {
		return 0
	}
	return n
}

// checkFreeSpace refuses a scan unless the data dir has at least 1.2 times
// the size of the root's current generation free. With no current generation
// the requirement is 0.
func (s *service) checkFreeSpace(root dbq.ScanRoot) error {
	if !root.CurrentScanID.Valid {
		return nil
	}
	fi, err := os.Stat(gen.Path(s.o.DataDir, root.ID, root.CurrentScanID.Int64))
	if err != nil {
		return nil // a missing current file needs nothing: the new scan replaces it
	}
	need := (fi.Size()*freeSpaceNum + freeSpaceDen - 1) / freeSpaceDen
	free, err := s.freeBytes()
	if err != nil {
		return fmt.Errorf("free space in data dir: %w", err)
	}
	if free < need {
		return fmt.Errorf("insufficient free space in data dir: need %d bytes, have %d", need, free)
	}
	return nil
}

func (s *service) freeBytes() (int64, error) {
	if f := s.o.hooks.freeBytes; f != nil {
		return f(s.o.DataDir)
	}
	var st unix.Statfs_t
	if err := unix.Statfs(s.o.DataDir, &st); err != nil {
		return 0, err
	}
	return int64(st.Bavail) * int64(st.Frsize), nil //nolint:gosec // G115: block counts fit int64
}

// walkOptions builds the walk configuration of a scan of root from the
// service options and the database.
func (s *service) walkOptions(ctx context.Context, root dbq.ScanRoot) (walk.Options, error) {
	o := s.o
	wo := walk.Options{
		Workers: o.Workers, CrossFS: root.CrossFs != 0, AllowWritable: o.AllowWritable,
		Gate: o.Gate.Low(), Limits: s.limits(ctx),
	}

	var ex []string
	if err := json.Unmarshal([]byte(root.Excludes), &ex); err != nil {
		return wo, fmt.Errorf("invalid excludes of root %d: %w", root.ID, err)
	}
	for _, e := range ex {
		n, err := pathx.Normalize(e)
		if err != nil || n == "" {
			return wo, fmt.Errorf("invalid exclude %q", e)
		}
		wo.Excludes = append(wo.Excludes, n)
	}

	paths, err := s.q.ListUserPathsByRoot(ctx, root.ID)
	if err != nil {
		return wo, fmt.Errorf("list user paths: %w", err)
	}
	for _, p := range paths {
		if a := string(p.RelPath); a != "" && !slices.Contains(wo.Anchors, a) {
			wo.Anchors = append(wo.Anchors, a)
		}
	}

	roots, err := s.q.ListRoots(ctx)
	if err != nil {
		return wo, fmt.Errorf("list roots: %w", err)
	}
	for _, other := range roots {
		if other.ID == root.ID || other.Enabled == 0 {
			continue
		}
		di, err := topDirIdent(other.ContainerPath)
		if err != nil {
			o.Log.Warn("skipping a root as nested root: cannot open it", "root", other.ID, "err", err)
			continue
		}
		wo.NestedRoots = append(wo.NestedRoots, di)
	}

	if ms, err := o.Mounts(); err != nil {
		o.Log.Warn("cannot read mountinfo, detecting pseudo filesystems by statfs only", "err", err)
	} else {
		wo.Mounts = ms
	}

	cats, err := s.q.ListCategories(ctx)
	if err != nil {
		return wo, fmt.Errorf("list categories: %w", err)
	}
	wo.Categories = make(map[string]cube.Category, len(cats))
	for _, c := range cats {
		cat, err := cube.ParseCategory(c.Category)
		if err != nil {
			o.Log.Warn("ignoring a file category", "ext", c.Ext, "err", err)
			continue
		}
		wo.Categories[c.Ext] = cat
	}
	return wo, nil
}

func topDirIdent(path string) (walk.DevIno, error) {
	d, err := fsx.OpenRoot(path)
	if err != nil {
		return walk.DevIno{}, err
	}
	defer func() { _ = d.Close() }()
	st, err := d.Stat()
	if err != nil {
		return walk.DevIno{}, err
	}
	return walk.DevIno{Dev: st.Dev, Ino: st.Ino}, nil
}

// limits reads the top-list settings top_n, top_max_depth and top_min_bytes.
// A missing setting keeps its default; an invalid one keeps the default and
// logs a warning.
func (s *service) limits(ctx context.Context) agg.Limits {
	l := agg.DefaultLimits()
	if n, ok := s.intSetting(ctx, settings.TopN); ok {
		l.TopN = int(n)
	}
	if d, ok := s.intSetting(ctx, settings.TopMaxDepth); ok {
		l.TopMaxDepth = d
	}
	if b, ok := s.intSetting(ctx, settings.TopMinBytes); ok {
		l.TopMinAlloc = b
	}
	return l
}

// intSetting reads an integer setting and checks it against the registry. ok
// is false when the setting is missing or invalid.
func (s *service) intSetting(ctx context.Context, key string) (v int64, ok bool) {
	raw, err := s.q.GetSetting(ctx, key)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false
	}
	if err != nil {
		s.o.Log.Warn("cannot read a setting, using the default", "key", key, "err", err)
		return 0, false
	}
	def, known := settings.Lookup(key)
	if !known || def.Validate(raw) != nil {
		s.o.Log.Warn("invalid setting, using the default", "key", key, "value", raw)
		return 0, false
	}
	v, _ = strconv.ParseInt(raw, 10, 64)
	return v, true
}

// phaseHook calls the test seam hooks.onPhase, if any, as a phase begins.
func (r *scanRun) phaseHook(phase string) {
	if h := r.s.o.hooks.onPhase; h != nil {
		h(phase)
	}
}
