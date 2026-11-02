package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"

	"github.com/vinRosso/hogwatch/internal/config"
	"github.com/vinRosso/hogwatch/internal/db"
	"github.com/vinRosso/hogwatch/internal/pathx"
	"github.com/vinRosso/hogwatch/internal/scanner/agg"
	"github.com/vinRosso/hogwatch/internal/scanner/build"
	"github.com/vinRosso/hogwatch/internal/scanner/service"
	"github.com/vinRosso/hogwatch/internal/scanner/walk"
)

// scanReport is the -json output of scan --once.
type scanReport struct {
	Path     string      `json:"path"`
	Stats    build.Stats `json:"stats"`
	Verified bool        `json:"verified"`
	// PeakRSSBytes is the process's peak resident set (getrusage Maxrss, which
	// Linux reports in KiB), for sizing the scanner's memory limit.
	PeakRSSBytes int64 `json:"peak_rss_bytes"`
}

// peakRSSBytes returns the peak resident set size of this process, or 0.
func peakRSSBytes() int64 {
	var ru syscall.Rusage
	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &ru); err != nil {
		return 0
	}
	return int64(ru.Maxrss) * 1024
}

// verifyGeneration is build.Verify; tests replace it to interrupt a verify.
var verifyGeneration = build.Verify

type anchorList []string

func (a *anchorList) String() string { return strings.Join(*a, ",") }
func (a *anchorList) Set(v string) error {
	*a = append(*a, v)
	return nil
}

// scan runs the scanner. Without --once it is the long-running service (see
// scanService). With --once it builds one generation file and exits 0 when done (and verified,
// with --verify), 1 on failure or a failed verification, 2 on bad usage and
// 130 when interrupted (no file is left, unless the interrupt came during
// --verify: the finished generation stays, unverified).
func scan(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fl := flag.NewFlagSet("scan", flag.ContinueOnError)
	fl.SetOutput(stderr)
	fl.Usage = func() {
		_, _ = fmt.Fprintf(stderr, "Usage:\n  hogwatch scan [--allow-writable-mounts]    run the scan service until SIGINT or SIGTERM (settings come from the configuration)\n  hogwatch scan --once --root PATH [flags]   scan one directory and exit\n\nFlags:\n")
		fl.PrintDefaults()
	}
	once := fl.Bool("once", false, "scan one root, write its generation file and exit; without it, run the scan service")
	root := fl.String("root", "", "host directory to scan (required)")
	dataDir := fl.String("data", "/data", "data directory; the generation goes to <data>/scans/<root-id>-<scan-id>.db")
	rootID := fl.Int64("root-id", 1, "root id (>= 1)")
	scanID := fl.Int64("scan-id", 1, "scan id (>= 1)")
	gmp := runtime.GOMAXPROCS(0)
	workers := fl.Int("workers", max(1, gmp/2), fmt.Sprintf("walk workers, 1..%d", config.WorkerCap(gmp)))
	crossFS := fl.Bool("cross-fs", false, "descend into other filesystems (pseudo ones are still skipped)")
	allowWritable := fl.Bool("allow-writable-mounts", false, "walk writable filesystems too (the scanner refuses them by default)")
	var anchors anchorList
	fl.Var(&anchors, "anchor", "path below the root where depth restarts at 0 (repeatable)")
	lim := agg.DefaultLimits()
	topN := fl.Int("top-n", lim.TopN, "entries per top list")
	topDepth := fl.Int64("top-depth", lim.TopMaxDepth, "deepest depth_anchor that gets top lists")
	topMin := fl.Int64("top-min-bytes", lim.TopMinAlloc, "smallest subtree allocation that gets top lists")
	verify := fl.Bool("verify", false, "check the finished generation's invariants")
	asJSON := fl.Bool("json", false, "print the result as one JSON object")
	if err := fl.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	usageErr := func(format string, a ...any) int {
		_, _ = fmt.Fprintf(stderr, "hogwatch scan: "+format+"\n", a...)
		return 2
	}
	if !*once {
		// Service mode takes its settings from the configuration; only
		// --allow-writable-mounts may override it.
		var stray []string
		fl.Visit(func(f *flag.Flag) {
			if f.Name != "once" && f.Name != "allow-writable-mounts" {
				stray = append(stray, "--"+f.Name)
			}
		})
		if len(stray) > 0 {
			return usageErr("%s only applies with --once", strings.Join(stray, ", "))
		}
		if fl.NArg() != 0 {
			return usageErr("unexpected argument %q", fl.Arg(0))
		}
		var override *bool
		fl.Visit(func(f *flag.Flag) {
			if f.Name == "allow-writable-mounts" {
				override = allowWritable
			}
		})
		return scanService(ctx, override, stdout, stderr)
	}
	if fl.NArg() != 0 {
		return usageErr("unexpected argument %q", fl.Arg(0))
	}
	if *root == "" {
		return usageErr("--root is required")
	}
	if *dataDir == "" {
		return usageErr("--data must not be empty")
	}
	if *rootID < 1 || *scanID < 1 {
		return usageErr("--root-id and --scan-id must be >= 1")
	}
	if c := config.WorkerCap(gmp); *workers < 1 || *workers > c {
		return usageErr("--workers must be between 1 and %d, got %d", c, *workers)
	}
	if *topN < 1 || *topDepth < 0 || *topMin < 0 {
		return usageErr("--top-n must be >= 1 and --top-depth, --top-min-bytes >= 0")
	}
	norm := make([]string, 0, len(anchors))
	for _, a := range anchors {
		n, err := pathx.Normalize(a)
		if err != nil {
			return usageErr("--anchor %q: %v", a, err)
		}
		norm = append(norm, n)
	}

	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	mounts, err := build.ReadMounts()
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "hogwatch scan: warning: cannot read /proc/self/mountinfo, detecting pseudo filesystems by statfs only: %v\n", err)
		mounts = nil
	}
	lim.TopN, lim.TopMaxDepth, lim.TopMinAlloc = *topN, *topDepth, *topMin
	res, err := build.Run(ctx, build.Options{
		DataDir: *dataDir, RootID: *rootID, ScanID: *scanID, Root: *root,
		ScannerVersion: version,
		Walk: walk.Options{
			Workers: *workers, CrossFS: *crossFS, AllowWritable: *allowWritable,
			Anchors: norm, Mounts: mounts, Limits: lim,
		},
	})
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			_, _ = fmt.Fprintln(stderr, "hogwatch scan: interrupted")
			return 130
		}
		_, _ = fmt.Fprintf(stderr, "hogwatch scan: %v\n", err)
		return 1
	}
	if *verify {
		if err := verifyGeneration(ctx, res.Path); err != nil {
			if ctx.Err() != nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				_, _ = fmt.Fprintln(stderr, "hogwatch scan: interrupted during verify; the generation is kept but unverified")
				return 130
			}
			_, _ = fmt.Fprintf(stderr, "hogwatch scan: verify %s: %v\n", res.Path, err)
			return 1
		}
	}
	rep := scanReport{Path: res.Path, Stats: res.Stats, Verified: *verify, PeakRSSBytes: peakRSSBytes()}
	if *asJSON {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(rep); err != nil {
			_, _ = fmt.Fprintf(stderr, "hogwatch scan: %v\n", err)
			return 1
		}
		return 0
	}
	writeScanText(stdout, rep)
	return 0
}

func writeScanText(w io.Writer, r scanReport) {
	line := func(label string, v any) { _, _ = fmt.Fprintf(w, "%-18s %v\n", label, v) }
	s := r.Stats
	line("generation", r.Path)
	line("verified", r.Verified)
	line("dir rows", group(s.DirRows))
	line("dirs opened", group(s.Walk.Dirs))
	line("files", group(s.Walk.Files))
	line("bytes logical", group(s.Walk.BytesLogical))
	line("bytes alloc", group(s.Walk.BytesAlloc))
	line("errors", group(s.Walk.Errors))
	line("mounts skipped", group(s.Walk.MountsSkipped))
	line("writable refused", group(s.Walk.WritableRefused))
	line("workers", s.Workers)
	line("walk seconds", fmt.Sprintf("%.3f", s.WalkSeconds))
	line("finalize seconds", fmt.Sprintf("%.3f", s.FinalizeSeconds))
}

// scanService runs the scanner service until SIGINT or SIGTERM: it loads the
// configuration, opens and migrates the main database and hands over to
// service.Run. It returns 0 after a signal and 1 on a startup or run error.
// A non-nil allowWritable overrides scan.allow_writable_mounts.
func scanService(ctx context.Context, allowWritable *bool, stdout, stderr io.Writer) int {
	fail := func(err error) int {
		if ctx.Err() != nil && errors.Is(err, context.Canceled) {
			return 0 // a signal arrived during startup: a clean stop, not a failure
		}
		_, _ = fmt.Fprintf(stderr, "hogwatch scan: %v\n", err)
		return 1
	}
	cfg, err := config.Load(config.FromOS())
	if err != nil {
		return fail(err)
	}
	log := config.NewLogger(cfg.Log, stdout)

	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	mainDB, err := db.Open(ctx, filepath.Join(cfg.DataDir, "hogwatch.db"))
	if err != nil {
		return fail(err)
	}
	defer func() { _ = mainDB.Close() }()
	if err := db.EnsureMigrated(ctx, mainDB); err != nil {
		return fail(err)
	}

	o := service.DefaultOptions(cfg, mainDB, log)
	o.Version = version
	if allowWritable != nil {
		o.AllowWritable = *allowWritable
	}
	log.Info("scanner started", "data_dir", cfg.DataDir, "workers", o.Workers, "listing_workers", o.ListingWorkers, "allow_writable_mounts", o.AllowWritable)
	if err := service.Run(ctx, o); err != nil {
		return fail(err)
	}
	log.Info("scanner stopped")
	return 0
}
