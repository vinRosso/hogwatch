package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"os"
	"os/signal"
	"runtime"
	"sort"
	"strconv"
	"syscall"
	"time"

	"github.com/vinRosso/hogwatch/internal/scanner/agg"
	"github.com/vinRosso/hogwatch/internal/scanner/walk"
)

// benchReport is the result of one bench run; it is also the -json output.
type benchReport struct {
	Root          string           `json:"root"`
	FSType        string           `json:"fs_type"`
	ReadOnly      bool             `json:"read_only"`
	Workers       int              `json:"workers"`
	GOMAXPROCS    int              `json:"gomaxprocs"`
	CrossFS       bool             `json:"cross_fs"`
	ElapsedSec    float64          `json:"elapsed_seconds"`
	Dirs          int64            `json:"dirs"`
	Files         int64            `json:"files"`
	Symlinks      int64            `json:"symlinks"`
	BytesLogical  int64            `json:"bytes_logical"`
	BytesAlloc    int64            `json:"bytes_alloc"`
	MountsSkipped int64            `json:"mounts_skipped"`
	PseudoSkipped int64            `json:"pseudo_skipped"`
	ZFSCtlSkipped int64            `json:"zfs_ctl_skipped"`
	Errors        int64            `json:"errors"`
	Errnos        map[string]int64 `json:"errnos,omitempty"`
	DirsPerSec    float64          `json:"dirs_per_sec"`
	FilesPerSec   float64          `json:"files_per_sec"`
	PeakRSSBytes  int64            `json:"peak_rss_bytes"`
	Interrupted   bool             `json:"interrupted"`
}

// bench walks one directory with walk.Scan, discarding the rows, and reports
// the throughput. It exits 0 when done, 1 when the root failed, 2 on bad usage
// and 130 when interrupted (the report is still printed).
func bench(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fl := flag.NewFlagSet("bench", flag.ContinueOnError)
	fl.SetOutput(stderr)
	fl.Usage = func() {
		_, _ = fmt.Fprintf(stderr, "Usage: hogwatch bench [flags] <path>\n\nFlags:\n")
		fl.PrintDefaults()
	}
	workers := fl.Int("workers", max(1, runtime.GOMAXPROCS(0)/2), "number of walk workers (>= 1)")
	crossFS := fl.Bool("cross-fs", false, "descend into other filesystems (pseudo ones are still skipped)")
	allowWritable := fl.Bool("allow-writable-mounts", true, "walk writable filesystems too; bench only reads, but the scanner refuses them, so set false to measure that rule")
	asJSON := fl.Bool("json", false, "print the report as one JSON object")
	progress := fl.Duration("progress", 10*time.Second, "interval between progress lines on stderr; 0 turns it off")
	if err := fl.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if fl.NArg() != 1 {
		fl.Usage()
		return 2
	}
	if *workers < 1 {
		_, _ = fmt.Fprintf(stderr, "hogwatch bench: -workers must be >= 1, got %d\n", *workers)
		return 2
	}
	if *progress < 0 {
		_, _ = fmt.Fprintf(stderr, "hogwatch bench: -progress must be >= 0, got %s\n", *progress)
		return 2
	}
	root := fl.Arg(0)

	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	start := time.Now()
	opts := walk.Options{
		Workers: *workers, CrossFS: *crossFS, AllowWritable: *allowWritable,
		Limits: agg.DefaultLimits(), // so the walk does the top-N work a real scan does
	}
	if *progress > 0 {
		opts.ProgressEvery = *progress
		opts.Progress = func(s walk.Stats) {
			el := time.Since(start)
			_, _ = fmt.Fprintf(stderr, "progress elapsed=%s dirs=%d files=%d dirs/s=%.0f files/s=%.0f errors=%d\n",
				el.Round(time.Second), s.Dirs, s.Files, rate(s.Dirs, el), rate(s.Files, el), s.Errors)
		}
	}
	res, err := walk.Scan(ctx, root, opts, walk.Discard)
	elapsed := time.Since(start)
	interrupted := errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
	if err != nil && !interrupted {
		_, _ = fmt.Fprintf(stderr, "hogwatch bench: %v\n", err)
		return 1
	}

	rep := benchReport{
		Root:          root,
		FSType:        fsTypeName(res.FSType),
		ReadOnly:      res.ReadOnly,
		Workers:       *workers,
		GOMAXPROCS:    runtime.GOMAXPROCS(0),
		CrossFS:       *crossFS,
		ElapsedSec:    elapsed.Seconds(),
		Dirs:          res.Stats.Dirs,
		Files:         res.Stats.Files,
		Symlinks:      res.Stats.Symlinks,
		BytesLogical:  res.Stats.BytesLogical,
		BytesAlloc:    res.Stats.BytesAlloc,
		MountsSkipped: res.Stats.MountsSkipped,
		PseudoSkipped: res.Stats.PseudoSkipped,
		ZFSCtlSkipped: res.Stats.ZFSCtlSkipped,
		Errors:        res.Stats.Errors,
		Errnos:        res.Stats.Errnos,
		DirsPerSec:    rate(res.Stats.Dirs, elapsed),
		FilesPerSec:   rate(res.Stats.Files, elapsed),
		PeakRSSBytes:  peakRSS(),
		Interrupted:   interrupted,
	}
	if *asJSON {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(rep); err != nil {
			_, _ = fmt.Fprintf(stderr, "hogwatch bench: %v\n", err)
			return 1
		}
	} else {
		writeText(stdout, rep)
	}
	if interrupted {
		return 130
	}
	return 0
}

// rate is n per second over el, or 0 when no time has passed.
func rate(n int64, el time.Duration) float64 {
	if el <= 0 {
		return 0
	}
	return float64(n) / el.Seconds()
}

// peakRSS is this process's peak resident set size in bytes (0 if unknown).
func peakRSS() int64 {
	var ru syscall.Rusage
	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &ru); err != nil {
		return 0
	}
	return int64(ru.Maxrss) * 1024 // Linux reports kilobytes
}

// fsTypeName names the statfs magics worth telling apart and spells the rest
// in hex.
func fsTypeName(magic int64) string {
	switch magic {
	case 0x2fc12fc1:
		return "zfs"
	case 0xef53:
		return "ext4"
	case 0x58465342:
		return "xfs"
	case 0x9123683e:
		return "btrfs"
	case 0x01021994:
		return "tmpfs"
	case 0x794c7630:
		return "overlay"
	}
	return "0x" + strconv.FormatInt(magic, 16)
}

// group formats n with thousands separators.
func group(n int64) string {
	s := strconv.FormatInt(n, 10)
	sign := ""
	if n < 0 {
		sign, s = "-", s[1:]
	}
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return sign + s
}

// writeText prints the report, one labelled field per line.
func writeText(w io.Writer, r benchReport) {
	line := func(label, value string) { _, _ = fmt.Fprintf(w, "%s %s\n", label, value) }
	line("root", r.Root)
	line("fs type", r.FSType)
	line("read only", strconv.FormatBool(r.ReadOnly))
	line("workers", group(int64(r.Workers)))
	line("gomaxprocs", group(int64(r.GOMAXPROCS)))
	line("cross-fs", strconv.FormatBool(r.CrossFS))
	line("elapsed", strconv.FormatFloat(r.ElapsedSec, 'f', 3, 64)+"s")
	line("dirs", group(r.Dirs))
	line("files", group(r.Files))
	line("symlinks", group(r.Symlinks))
	line("bytes logical", group(r.BytesLogical))
	line("bytes alloc", group(r.BytesAlloc))
	line("mounts skipped", group(r.MountsSkipped))
	line("pseudo skipped", group(r.PseudoSkipped))
	line("zfs ctl skipped", group(r.ZFSCtlSkipped))
	line("errors", group(r.Errors))
	names := make([]string, 0, len(r.Errnos))
	for name := range r.Errnos {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		line("  "+name, group(r.Errnos[name]))
	}
	line("dirs/s", group(int64(math.Round(r.DirsPerSec))))
	line("files/s", group(int64(math.Round(r.FilesPerSec))))
	line("peak RSS", group(r.PeakRSSBytes)+" bytes")
	line("interrupted", strconv.FormatBool(r.Interrupted))
}
