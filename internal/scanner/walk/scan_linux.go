package walk

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/vinRosso/hogwatch/internal/cube"
	"github.com/vinRosso/hogwatch/internal/pathx"
	"github.com/vinRosso/hogwatch/internal/scanner/agg"
	"github.com/vinRosso/hogwatch/internal/scanner/fsx"
)

// ErrWritableRoot is returned by Scan when the root's filesystem is not mounted
// read-only and AllowWritable is not set (security invariant 5).
var ErrWritableRoot = errors.New("walk: root filesystem is writable")

// Options configures Scan.
type Options struct {
	Workers       int  // >= 1, else Scan returns an error
	CrossFS       bool // descend into other filesystems (pseudo ones are still skipped)
	AllowWritable bool // do not refuse a writable root or a writable mount
	// NestedRoots are the (dev, ino) of other roots' top directories, which are
	// not entered (FlagNestedRootSkipped).
	NestedRoots []DevIno
	// Anchors are pathx-normalised paths below the root whose directories
	// restart depth_anchor at 0. The root itself is always an anchor.
	Anchors []string
	// Excludes are pathx-normalised, non-empty paths below the root that are
	// not entered: each gets a zero-count FlagExcluded row.
	Excludes []string
	// Gate, if set, bounds how many directories are listed at once across
	// everything sharing it: a token is held while one directory is read, never
	// while a child is walked. Nil means no gate.
	Gate Gate
	// Mounts is a /proc/self/mountinfo snapshot; a crossing uses it to tell
	// devtmpfs from tmpfs.
	Mounts        []fsx.Mount
	ScanStart     time.Time                // age buckets are relative to it; zero means now
	Categories    map[string]cube.Category // nil means cube.DefaultCategories()
	Limits        agg.Limits               // top-N rule
	ProgressEvery time.Duration            // 0 = no progress callbacks
	Progress      func(Stats)              // called from one goroutine with a snapshot

	forceInline bool                                     // tests: never queue a subdirectory, always walk it inline
	onDir       func()                                   // tests: called once for each directory entered
	readNames   func(*fsx.Dir, func(string) error) error // tests: replaces the directory read
	statChild   func(*fsx.Dir) (fsx.Stat, error)         // tests: replaces the stat of an opened child
	statfsChild func(*fsx.Dir) (fsx.FSInfo, error)       // tests: replaces the statfs of a crossing child
}

// Stats are the totals of a walk, taken before hardlink subtraction. Dirs
// counts directories opened, the root included; Files counts every
// non-directory entry, Symlinks and HardlinkFiles (nlink > 1) are subsets of
// those. Bytes are summed over non-directory entries.
type Stats struct {
	Dirs            int64 `json:"dirs"`
	Files           int64 `json:"files"`
	Symlinks        int64 `json:"symlinks"`
	BytesLogical    int64 `json:"bytes_logical"`
	BytesAlloc      int64 `json:"bytes_alloc"`
	HardlinkFiles   int64 `json:"hardlink_files"`
	MountsSkipped   int64 `json:"mounts_skipped"`
	PseudoSkipped   int64 `json:"pseudo_skipped"`
	ZFSCtlSkipped   int64 `json:"zfs_ctl_skipped"`
	NestedSkipped   int64 `json:"nested_skipped"`
	WritableRefused int64 `json:"writable_refused"`
	Excluded        int64 `json:"excluded"`
	// DupNames counts subdirectory names dropped because the same name came
	// twice in one listing (getdents racing a rename). A duplicate file name is
	// still counted twice: a known race.
	DupNames int64 `json:"dup_names"`
	// MaxExtKeys is the largest extension map seen in one accumulator, the
	// memory driver of the top-extension totals.
	MaxExtKeys int64            `json:"max_ext_keys"`
	Changed    int64            `json:"changed"`
	Loops      int64            `json:"loops"`
	Errors     int64            `json:"errors"`
	Errnos     map[string]int64 `json:"errnos,omitempty"` // unix.ErrnoName → count; "other" for non-errno; nil when Errors == 0
}

// Result is what Scan returns: the totals, the root's identity and statfs, and
// the number of dirs rows handed to the sink.
type Result struct {
	Stats            Stats
	RootDev, RootIno uint64
	FSType           int64
	ReadOnly         bool
	// FSTotal and FSAvail are the root filesystem's size and the space available
	// to unprivileged users, in bytes.
	FSTotal, FSAvail int64
	DirRows          int64
}

// Scan walks root without crossing symlinks and hands the rows of a generation
// to sink: one dirs row and one cube row per directory (the root has id 1, ids
// are dense and a parent's is smaller than its children's), top lists and
// extension totals for the directories Limits qualifies, shared inode and link
// rows for every entry with nlink > 1, and error rows. Every sink call comes
// from one internal goroutine; the first sink error cancels the walk and is
// returned. A root that cannot be opened, stat'ed or statfs'ed is an error, as
// is a writable root without AllowWritable; errors below it are counted and
// stored as rows. When ctx is cancelled Scan returns the partial Result with
// ctx.Err(), and the rows already given to the sink are incomplete.
func Scan(ctx context.Context, root string, opts Options, sink Sink) (Result, error) {
	if opts.Workers < 1 {
		return Result{}, fmt.Errorf("walk: Workers must be >= 1, got %d", opts.Workers)
	}
	if sink == nil {
		sink = Discard
	}
	trie, err := buildTrie(opts.Anchors, opts.Excludes)
	if err != nil {
		return Result{}, err
	}
	rd, err := fsx.OpenRoot(root)
	if err != nil {
		return Result{}, fmt.Errorf("walk: %w", err)
	}
	st, err := rd.Stat()
	if err != nil {
		_ = rd.Close()
		return Result{}, fmt.Errorf("walk: %w", err)
	}
	info, err := rd.Statfs()
	if err != nil {
		_ = rd.Close()
		return Result{}, fmt.Errorf("walk: %w", err)
	}
	if !info.ReadOnly && !opts.AllowWritable {
		_ = rd.Close()
		return Result{}, ErrWritableRoot
	}

	wctx, cancel := context.WithCancel(ctx)
	defer cancel()
	w := &walker{
		ctx:           wctx,
		cancel:        cancel,
		ch:            make(chan item, opts.Workers),
		msgs:          make(chan *rows, 4*opts.Workers),
		crossFS:       opts.CrossFS,
		allowWritable: opts.AllowWritable,
		scanStart:     opts.ScanStart,
		cats:          opts.Categories,
		lim:           opts.Limits,
		forceInline:   opts.forceInline,
		onDir:         opts.onDir,
		readNames:     opts.readNames,
		gate:          opts.Gate,
		statChild:     opts.statChild,
		statfsChild:   opts.statfsChild,
		nested:        nestedSet(opts.NestedRoots),
		mountTypes:    mountTypeMap(opts.Mounts),
	}
	if w.scanStart.IsZero() {
		w.scanStart = time.Now()
	}
	if w.cats == nil {
		w.cats = cube.DefaultCategories()
	}

	writerDone := make(chan struct{})
	go w.writeLoop(sink, writerDone)

	var workers sync.WaitGroup
	for range opts.Workers {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for it := range w.ch {
				w.process(it.n, it.dir)
				w.wg.Done()
			}
		}()
	}

	stopProgress := w.startProgress(opts)

	w.nextID.Store(1)
	rootNode := &node{id: 1, st: st, anchor: trie, at: rootWhere(rd, st, info.Type)}
	w.wg.Add(1)
	w.ch <- item{n: rootNode, dir: rd} // capacity >= 1 and the channel is empty
	go func() {
		w.wg.Wait()
		close(w.ch)
	}()
	workers.Wait()
	close(w.msgs)
	<-writerDone
	stopProgress()

	res := Result{
		Stats:   w.snapshot(),
		RootDev: st.Dev, RootIno: st.Ino,
		FSType: info.Type, ReadOnly: info.ReadOnly,
		FSTotal: info.Total, FSAvail: info.Avail,
		DirRows: w.nextID.Load(),
	}
	return res, scanError(ctx, w.fatalErr())
}

// scanError picks what Scan reports: the caller's cancellation, unless a sink
// failure came first and is not merely that cancellation.
func scanError(ctx context.Context, fatal error) error {
	cerr := ctx.Err()
	if fatal != nil && (cerr == nil || !errors.Is(fatal, cerr)) {
		return fatal
	}
	return cerr
}

// buildTrie turns the anchor and exclude paths into a trie whose root is the
// scan root. An excluded path may also be an anchor; the walk never gets below
// it.
func buildTrie(anchors, excludes []string) (*trieNode, error) {
	root := &trieNode{anchor: true}
	insert := func(p string) *trieNode {
		t := root
		for _, seg := range pathx.Segments(p) {
			next := t.children[seg]
			if next == nil {
				if t.children == nil {
					t.children = make(map[string]*trieNode)
				}
				next = &trieNode{}
				t.children[seg] = next
			}
			t = next
		}
		return t
	}
	for _, a := range anchors {
		norm, err := pathx.Normalize(a)
		if err != nil || norm != a {
			return nil, fmt.Errorf("walk: anchor %q is not a normalised relative path", a)
		}
		insert(a).anchor = true
	}
	for _, e := range excludes {
		norm, err := pathx.Normalize(e)
		if err != nil || norm != e || e == "" {
			return nil, fmt.Errorf("walk: exclude %q is not a normalised relative path below the root", e)
		}
		insert(e).excluded = true
	}
	return root, nil
}

// startProgress runs the progress goroutine, if any, and returns a function
// that stops it after one final snapshot.
func (w *walker) startProgress(opts Options) (stop func()) {
	if opts.ProgressEvery <= 0 || opts.Progress == nil {
		return func() {}
	}
	done := make(chan struct{})
	exited := make(chan struct{})
	go func() {
		defer close(exited)
		t := time.NewTicker(opts.ProgressEvery)
		defer t.Stop()
		for {
			select {
			case <-t.C:
				opts.Progress(w.snapshot())
			case <-done:
				opts.Progress(w.snapshot())
				return
			}
		}
	}()
	return func() {
		close(done)
		<-exited
	}
}

func nestedSet(roots []DevIno) map[DevIno]struct{} {
	m := make(map[DevIno]struct{}, len(roots))
	for _, r := range roots {
		m[r] = struct{}{}
	}
	return m
}

// mountTypeMap maps a device to the fstype mountinfo gives it. Devices missing
// from the snapshot (mounted after it was read) have no entry.
func mountTypeMap(mounts []fsx.Mount) map[uint64]string {
	m := make(map[uint64]string, len(mounts))
	for _, mt := range mounts {
		m[mt.Dev] = mt.FSType
	}
	return m
}
