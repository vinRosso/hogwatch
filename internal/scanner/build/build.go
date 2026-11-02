//go:build linux

package build

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/vinRosso/hogwatch/internal/cube"
	"github.com/vinRosso/hogwatch/internal/gen"
	"github.com/vinRosso/hogwatch/internal/scanner/finalize"
	"github.com/vinRosso/hogwatch/internal/scanner/fsx"
	"github.com/vinRosso/hogwatch/internal/scanner/walk"
)

// Options configures Run.
type Options struct {
	DataDir        string
	RootID, ScanID int64
	Root           string       // host path of the scan root
	Walk           walk.Options // Walk.ScanStart is overwritten from Now
	ScannerVersion string
	Now            func() time.Time // nil means time.Now
	// OnFinalize, if set, is called once after the walk has ended and before
	// the indexes and hardlink pass run, so a caller can report the phase.
	OnFinalize func()
	// OnWalk, if set, is called once after the generation file exists and
	// before the walk starts.
	OnWalk func()
}

// Stats is written as meta.stats. Walk is taken before hardlink subtraction.
// ErrorRowsStored and ErrorRowsDropped split the errors stored as rows from
// those only counted once the row cap was reached. TopN is the top-list size
// the scan ran with, which Verify bounds ranks by.
type Stats struct {
	Walk             walk.Stats             `json:"walk"`
	Hardlinks        finalize.HardlinkStats `json:"hardlinks"`
	ErrorRowsStored  int64                  `json:"error_rows_stored"`
	ErrorRowsDropped int64                  `json:"error_rows_dropped"`
	Workers          int                    `json:"workers"`
	TopN             int                    `json:"top_n"`
	// DirRows is the number of dirs rows. Walk.Dirs counts directories opened, so it
	// is lower when a directory failed to open or was skipped (those get a row).
	DirRows         int64   `json:"dir_rows"`
	WalkSeconds     float64 `json:"walk_seconds"`
	FinalizeSeconds float64 `json:"finalize_seconds"`
}

// ReadMounts reads /proc/self/mountinfo for Options.Walk.Mounts. It lets the
// command line fill that field without importing fsx, which the lint rules
// keep inside the scanner.
func ReadMounts() ([]fsx.Mount, error) { return fsx.ReadMounts() }

// Result is a finished generation.
type Result struct {
	Path  string
	Stats Stats
	// RootDev and RootIno identify the walk root's directory. FSTotal and
	// FSAvail are its filesystem's size and unprivileged free space in bytes.
	RootDev, RootIno uint64
	FSTotal, FSAvail int64
}

// Run builds the generation file for (RootID, ScanID) under DataDir. On any
// error, including cancellation, the file is removed and the cause returned;
// an existing file at the path is never touched.
func Run(ctx context.Context, o Options) (Result, error) {
	now := o.Now
	if now == nil {
		now = time.Now
	}
	started := now()
	o.Walk.ScanStart = started
	cats := o.Walk.Categories
	if cats == nil {
		cats = cube.DefaultCategories()
		o.Walk.Categories = cats
	}

	path := gen.Path(o.DataDir, o.RootID, o.ScanID)
	w, err := gen.Create(ctx, path)
	if err != nil {
		return Result{}, err
	}
	done := false
	defer func() {
		if !done {
			_ = w.Abort()
		}
	}()

	if o.OnWalk != nil {
		o.OnWalk()
	}
	st := Stats{Workers: o.Walk.Workers, TopN: o.Walk.Limits.TopN}
	t0 := time.Now()
	res, err := walk.Scan(ctx, o.Root, o.Walk, w)
	st.WalkSeconds = time.Since(t0).Seconds()
	if err != nil {
		return Result{}, err
	}
	st.Walk = res.Stats
	st.DirRows = res.DirRows
	st.ErrorRowsStored, st.ErrorRowsDropped = w.ErrorCounts()

	if o.OnFinalize != nil {
		o.OnFinalize()
	}
	t1 := time.Now()
	if err := w.BuildIndexes(ctx); err != nil {
		return Result{}, err
	}
	if err := w.NumberPrePost(ctx); err != nil {
		return Result{}, err
	}
	if st.Hardlinks, err = finalize.Hardlinks(ctx, w, started, cats); err != nil {
		return Result{}, err
	}
	st.FinalizeSeconds = time.Since(t1).Seconds()

	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	raw, err := json.Marshal(st)
	if err != nil {
		return Result{}, fmt.Errorf("build: encode stats: %w", err)
	}
	err = w.Finish(ctx, gen.Meta{
		ScanID: o.ScanID, RootID: o.RootID, ScannerVersion: o.ScannerVersion,
		StartedAt: started, FinishedAt: now(), Stats: raw,
	})
	if err != nil {
		return Result{}, err
	}
	done = true
	return Result{
		Path: path, Stats: st,
		RootDev: res.RootDev, RootIno: res.RootIno,
		FSTotal: res.FSTotal, FSAvail: res.FSAvail,
	}, nil
}
