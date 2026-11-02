package walk

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/vinRosso/hogwatch/internal/gen"
	"github.com/vinRosso/hogwatch/internal/scanner/agg"
	"github.com/vinRosso/hogwatch/internal/scanner/fsx"
	"golang.org/x/sys/unix"
)

func TestSkipZFSCtl(t *testing.T) {
	tests := []struct {
		name     string
		fsType   int64
		isFSRoot bool
		entry    string
		want     bool
	}{
		{"zfs root, .zfs", fsx.MagicZFS, true, ".zfs", true},
		{"zfs non-root, .zfs", fsx.MagicZFS, false, ".zfs", false},
		{"ext4 root, .zfs", unix.EXT4_SUPER_MAGIC, true, ".zfs", false},
		{"zfs root, zfs", fsx.MagicZFS, true, "zfs", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := skipZFSCtl(tc.fsType, tc.isFSRoot, tc.entry); got != tc.want {
				t.Fatalf("skipZFSCtl(%#x, %v, %q) = %v, want %v", tc.fsType, tc.isFSRoot, tc.entry, got, tc.want)
			}
		})
	}
}

// crossFixture opens a temp directory holding the subdirectories names and
// returns it, its Lstat and one subdir value per name, in order. The test owns
// the returned Dir and closes it.
func crossFixture(t *testing.T, names ...string) (*fsx.Dir, fsx.Stat, []subdir) {
	t.Helper()
	root := t.TempDir()
	for _, n := range names {
		if err := os.Mkdir(filepath.Join(root, n), 0o750); err != nil {
			t.Fatal(err)
		}
	}
	d, err := fsx.OpenRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	parent, err := d.Stat()
	if err != nil {
		t.Fatal(err)
	}
	subs := make([]subdir, 0, len(names))
	for _, n := range names {
		st, err := d.Lstat(n)
		if err != nil {
			t.Fatal(err)
		}
		subs = append(subs, subdir{name: n, st: st})
	}
	return d, parent, subs
}

func TestCrossSkipsZFSCtlAtZFSRoot(t *testing.T) {
	d, parent, subs := crossFixture(t, ".zfs")
	w := &walker{ctx: context.Background()}
	n := &node{dir: d, st: parent, at: where{fsType: fsx.MagicZFS, fsRoot: true}}

	child, _, _, skip, _ := w.cross(n, subs[0])
	if child != nil {
		_ = child.Close()
		t.Fatal(".zfs at a ZFS filesystem root was opened, want it skipped")
	}
	if skip != skipDrop {
		t.Fatalf("skip = %d, want skipDrop (no row)", skip)
	}
	if got := w.zfsCtlSkipped.Load(); got != 1 {
		t.Fatalf("zfsCtlSkipped = %d, want 1", got)
	}
	if got := w.errs.Load(); got != 0 {
		t.Fatalf("errors = %d, want 0", got)
	}
}

func TestCrossKeepsZFSCtlWhereNotSkippable(t *testing.T) {
	tests := []struct {
		name string
		at   where
	}{
		{"zfs, not a filesystem root", where{fsType: fsx.MagicZFS, fsRoot: false}},
		{"ext4 filesystem root", where{fsType: unix.EXT4_SUPER_MAGIC, fsRoot: true}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			d, parent, subs := crossFixture(t, ".zfs")
			w := &walker{ctx: context.Background()}
			n := &node{dir: d, st: parent, at: tc.at}

			child, cat, flag, skip, _ := w.cross(n, subs[0])
			if child == nil || skip != skipNone || flag != 0 {
				t.Fatal("cross returned no child, want .zfs walked")
			}
			if err := child.Close(); err != nil { // closed exactly once, here
				t.Fatal(err)
			}
			if cat.fsType != tc.at.fsType || cat.fsRoot {
				t.Fatalf("child position = %+v, want fsType %#x and fsRoot false", cat, tc.at.fsType)
			}
			if got := w.zfsCtlSkipped.Load(); got != 0 {
				t.Fatalf("zfsCtlSkipped = %d, want 0", got)
			}
		})
	}
}

func TestRootWhereSameDeviceAsParent(t *testing.T) {
	d, st, _ := crossFixture(t)
	info, err := d.Statfs()
	if err != nil {
		t.Fatal(err)
	}
	got := rootWhere(d, st, info.Type)
	if got.fsRoot {
		t.Fatal("fsRoot = true for a temp directory on the same device as its parent, want false")
	}
	if got.fsType != info.Type {
		t.Fatalf("fsType = %#x, want the statfs type %#x", got.fsType, info.Type)
	}
}

// crossCase is one row of the decision table. The fixture directory "sub" is
// real; what cross sees of it (lstat, the stat of the opened descriptor,
// statfs) is faked, so every row is reachable on any filesystem.
type crossCase struct {
	name string
	// setup edits the walker, the lstat of "sub" and the parent before the call.
	setup func(w *walker, sub *fsx.Stat, parent *node)
	// opened maps the lstat to the stat of the opened child; nil means unchanged.
	opened func(lstat fsx.Stat) fsx.Stat
	fs     fsx.FSInfo // what statfs reports for a crossing child
	entry  string     // name of the subdirectory; "sub" when empty

	wantSkip  skipKind
	wantFlag  int64
	wantAt    where // checked for skipNone only
	wantErrno unix.Errno
	wantMsg   string
	counters  map[string]int64 // counter name -> want; every other counter must be 0
}

func counterValues(w *walker) map[string]int64 {
	return map[string]int64{
		"mounts": w.mountsSkipped.Load(), "pseudo": w.pseudoSkipped.Load(),
		"zfsctl": w.zfsCtlSkipped.Load(), "nested": w.nestedSkipped.Load(),
		"writable": w.writableRefused.Load(), "changed": w.changed.Load(),
		"loops": w.loops.Load(), "errs": w.errs.Load(),
	}
}

func TestCross_DecisionTable(t *testing.T) {
	const otherDev = 0xABCD00
	tmpfs := fsx.FSInfo{Type: unix.TMPFS_MAGIC, ReadOnly: true}
	toOtherDev := func(s fsx.Stat) fsx.Stat {
		s.Dev = otherDev
		return s
	}
	tests := []crossCase{
		{
			name: "1 .zfs at a zfs root is dropped", entry: ".zfs",
			setup: func(w *walker, sub *fsx.Stat, p *node) {
				p.at = where{fsType: fsx.MagicZFS, fsRoot: true}
			},
			wantSkip: skipDrop, counters: map[string]int64{"zfsctl": 1},
		},
		{
			name: "2 nested root wins over a mount crossing",
			setup: func(w *walker, sub *fsx.Stat, p *node) {
				sub.Dev = otherDev
				w.nested = map[DevIno]struct{}{{Dev: otherDev, Ino: sub.Ino}: {}}
			},
			wantSkip: skipRow, wantFlag: gen.FlagNestedRootSkipped, counters: map[string]int64{"nested": 1},
		},
		{
			name:     "3 other device without CrossFS",
			setup:    func(w *walker, sub *fsx.Stat, p *node) { sub.Dev = otherDev },
			wantSkip: skipRow, wantFlag: gen.FlagMountpointSkipped, counters: map[string]int64{"mounts": 1},
		},
		{
			name:     "3 bind mount root on the same device without CrossFS",
			setup:    func(w *walker, sub *fsx.Stat, p *node) { sub.MountRoot = true },
			wantSkip: skipRow, wantFlag: gen.FlagMountpointSkipped, counters: map[string]int64{"mounts": 1},
		},
		{
			name:  "4 same device, different inode: vanished",
			setup: func(w *walker, sub *fsx.Stat, p *node) {},
			opened: func(s fsx.Stat) fsx.Stat {
				s.Ino++
				return s
			},
			wantSkip: skipError, wantFlag: gen.FlagUnreadable, wantErrno: unix.ENOENT, wantMsg: "changed during scan",
			counters: map[string]int64{"changed": 1, "errs": 1},
		},
		{
			name:     "4 mount appeared, without CrossFS",
			setup:    func(w *walker, sub *fsx.Stat, p *node) {},
			opened:   toOtherDev,
			wantSkip: skipRow, wantFlag: gen.FlagMountpointSkipped, counters: map[string]int64{"mounts": 1},
		},
		{
			name:     "4 mount appeared with CrossFS is re-evaluated as a crossing",
			setup:    func(w *walker, sub *fsx.Stat, p *node) { w.crossFS = true },
			opened:   toOtherDev,
			fs:       tmpfs,
			wantSkip: skipNone, wantAt: where{fsType: unix.TMPFS_MAGIC, fsRoot: true},
		},
		{
			name:   "4 mount appeared, pseudo by the new device's mountinfo type",
			opened: toOtherDev,
			setup: func(w *walker, sub *fsx.Stat, p *node) {
				w.crossFS = true
				w.mountTypes = map[uint64]string{otherDev: "devtmpfs"}
			},
			fs:       tmpfs,
			wantSkip: skipRow, wantFlag: gen.FlagMountpointSkipped, counters: map[string]int64{"pseudo": 1},
		},
		{
			name: "5 pseudo by statfs magic",
			setup: func(w *walker, sub *fsx.Stat, p *node) {
				sub.Dev = otherDev
				w.crossFS = true
			},
			fs:       fsx.FSInfo{Type: unix.PROC_SUPER_MAGIC, ReadOnly: true},
			wantSkip: skipRow, wantFlag: gen.FlagMountpointSkipped, counters: map[string]int64{"pseudo": 1},
		},
		{
			name: "5 pseudo by mountinfo fstype (devtmpfs reports tmpfs)",
			setup: func(w *walker, sub *fsx.Stat, p *node) {
				sub.Dev = otherDev
				w.crossFS = true
				w.mountTypes = map[uint64]string{otherDev: "devtmpfs"}
			},
			fs:       tmpfs,
			wantSkip: skipRow, wantFlag: gen.FlagMountpointSkipped, counters: map[string]int64{"pseudo": 1},
		},
		{
			name: "5 tmpfs without a mountinfo entry is entered",
			setup: func(w *walker, sub *fsx.Stat, p *node) {
				sub.Dev = otherDev
				w.crossFS = true
				w.mountTypes = map[uint64]string{otherDev + 1: "devtmpfs"}
			},
			fs:       tmpfs,
			wantSkip: skipNone, wantAt: where{fsType: unix.TMPFS_MAGIC, fsRoot: true},
		},
		{
			name: "5 writable crossing refused",
			setup: func(w *walker, sub *fsx.Stat, p *node) {
				sub.Dev = otherDev
				w.crossFS = true
			},
			fs:       fsx.FSInfo{Type: unix.TMPFS_MAGIC},
			wantSkip: skipRow, wantFlag: gen.FlagWritableMountRefused, counters: map[string]int64{"writable": 1},
		},
		{
			name: "5 writable crossing allowed",
			setup: func(w *walker, sub *fsx.Stat, p *node) {
				sub.Dev = otherDev
				w.crossFS = true
				w.allowWritable = true
			},
			fs:       fsx.FSInfo{Type: unix.TMPFS_MAGIC},
			wantSkip: skipNone, wantAt: where{fsType: unix.TMPFS_MAGIC, fsRoot: true},
		},
		{
			name: "5 pseudo is checked before writable",
			setup: func(w *walker, sub *fsx.Stat, p *node) {
				sub.Dev = otherDev
				w.crossFS = true
			},
			fs:       fsx.FSInfo{Type: unix.SYSFS_MAGIC},
			wantSkip: skipRow, wantFlag: gen.FlagMountpointSkipped, counters: map[string]int64{"pseudo": 1},
		},
		{
			name: "6 same (dev, ino) as an ancestor: loop",
			setup: func(w *walker, sub *fsx.Stat, p *node) {
				sub.MountRoot = true
				w.crossFS = true
				w.allowWritable = true
				p.parent = &node{st: fsx.Stat{Dev: sub.Dev, Ino: sub.Ino}}
			},
			fs:       tmpfs,
			wantSkip: skipError, wantFlag: gen.FlagUnreadable, wantErrno: unix.ELOOP,
			counters: map[string]int64{"loops": 1, "errs": 1},
		},
		{
			name:     "6 the parent itself counts as an ancestor",
			setup:    func(w *walker, sub *fsx.Stat, p *node) { p.st.Dev, p.st.Ino = sub.Dev, sub.Ino },
			wantSkip: skipError, wantFlag: gen.FlagUnreadable, wantErrno: unix.ELOOP,
			counters: map[string]int64{"loops": 1, "errs": 1},
		},
		{
			name:     "plain subdirectory is entered, inheriting the fs type",
			setup:    func(w *walker, sub *fsx.Stat, p *node) { p.at = where{fsType: unix.EXT4_SUPER_MAGIC} },
			wantSkip: skipNone, wantAt: where{fsType: unix.EXT4_SUPER_MAGIC},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if tc.entry == "" {
				tc.entry = "sub"
			}
			d, parentSt, subs := crossFixture(t, tc.entry)
			sub := subs[0]
			p := &node{dir: d, st: parentSt}
			w := &walker{ctx: context.Background()}
			tc.setup(w, &sub.st, p)
			w.statChild = func(*fsx.Dir) (fsx.Stat, error) {
				if tc.opened != nil {
					return tc.opened(sub.st), nil
				}
				return sub.st, nil
			}
			w.statfsChild = func(*fsx.Dir) (fsx.FSInfo, error) { return tc.fs, nil }

			child, at, flag, skip, cerr := w.cross(p, sub)
			if child != nil {
				defer func() { _ = child.Close() }()
			}
			if skip != tc.wantSkip {
				t.Fatalf("skip = %d, want %d (err %v)", skip, tc.wantSkip, cerr)
			}
			if (child != nil) != (tc.wantSkip == skipNone) {
				t.Fatalf("child open = %v, want %v", child != nil, tc.wantSkip == skipNone)
			}
			if flag != tc.wantFlag {
				t.Fatalf("flag = %#x, want %#x", flag, tc.wantFlag)
			}
			wantSt := sub.st
			if tc.opened != nil {
				wantSt = tc.opened(sub.st)
			}
			if tc.wantSkip == skipNone && at.st != wantSt {
				t.Fatalf("returned stat = %+v, want the post-open stat %+v", at.st, wantSt)
			}
			at.st = fsx.Stat{}
			if tc.wantSkip == skipNone && at != tc.wantAt {
				t.Fatalf("at = %+v, want %+v", at, tc.wantAt)
			}
			if tc.wantErrno != 0 {
				if errnoOf(cerr) != tc.wantErrno {
					t.Fatalf("errno = %v, want %v (err %v)", errnoOf(cerr), tc.wantErrno, cerr)
				}
				if row := errorRow(7, "", "open", cerr); tc.wantMsg != "" && row.Message != tc.wantMsg {
					t.Fatalf("message = %q, want %q", row.Message, tc.wantMsg)
				}
			} else if cerr != nil {
				t.Fatalf("unexpected error %v", cerr)
			}
			for name, got := range counterValues(w) {
				if want := tc.counters[name]; got != want {
					t.Errorf("counter %s = %d, want %d", name, got, want)
				}
			}
		})
	}
}

func TestScan_NestedRoot(t *testing.T) {
	root := t.TempDir()
	for _, d := range []string{"keep/a", "other/b"} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o750); err != nil {
			t.Fatal(err)
		}
	}
	writeFile(t, filepath.Join(root, "other", "b", "f"), 10)
	writeFile(t, filepath.Join(root, "keep", "a", "g"), 10)
	var st unix.Stat_t
	if err := unix.Lstat(filepath.Join(root, "other"), &st); err != nil {
		t.Fatal(err)
	}

	opts := testOpts(2)
	opts.NestedRoots = []DevIno{{Dev: uint64(st.Dev), Ino: st.Ino}} //nolint:unconvert // Dev is not uint64 on every arch
	rec := newRec()
	res, err := Scan(context.Background(), root, opts, rec)
	if err != nil {
		t.Fatal(err)
	}
	rec.checkIDs(t)
	rows := rec.byPath(t)
	o := rows["other"]
	if o.Flags&gen.FlagNestedRootSkipped == 0 || o.Files != 0 || o.Dirs != 0 {
		t.Errorf("other row = %+v, want FlagNestedRootSkipped and no counts", o)
	}
	if _, entered := rows["other/b"]; entered {
		t.Error("other/b has a row, the nested root was entered")
	}
	if s := res.Stats; s.NestedSkipped != 1 || s.Files != 1 || s.Dirs != 3 || s.Errors != 0 {
		t.Errorf("stats = %+v, want nested 1, files 1, dirs 3 (root, keep, keep/a), no errors", s)
	}
}

// TestScan_SymlinkSwapRace flips root/d between a real directory and a symlink
// to outside/ (which holds a 1 TiB sparse file) while Scan runs. The scanner
// must never follow the symlink, so no scan can account for that file.
func TestScan_SymlinkSwapRace(t *testing.T) {
	const marker = int64(1) << 40
	base := t.TempDir()
	root, outside := filepath.Join(base, "root"), filepath.Join(base, "outside")
	for _, d := range []string{filepath.Join(root, "d", "sub"), filepath.Join(root, "other"), outside} {
		if err := os.MkdirAll(d, 0o750); err != nil {
			t.Fatal(err)
		}
	}
	big, err := os.Create(filepath.Join(outside, "big")) //nolint:gosec // a path under t.TempDir()
	if err != nil {
		t.Fatal(err)
	}
	if err := big.Truncate(marker); err != nil {
		_ = big.Close()
		t.Skipf("filesystem cannot hold a 1 TiB sparse file: %v", err)
	}
	_ = big.Close()
	if err := os.Mkdir(filepath.Join(outside, "deep"), 0o750); err != nil {
		t.Fatal(err)
	}
	// root/s is a symlink to outside/; exchanging d and s makes d the symlink.
	if err := os.Symlink(outside, filepath.Join(root, "s")); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(root, "d", "sub", "f"), 10)

	stop := make(chan struct{})
	swapped := make(chan int)
	go func() {
		n := 0
		for {
			select {
			case <-stop:
				swapped <- n
				return
			default:
			}
			if err := unix.Renameat2(unix.AT_FDCWD, filepath.Join(root, "d"), unix.AT_FDCWD, filepath.Join(root, "s"), unix.RENAME_EXCHANGE); err != nil {
				swapped <- -1
				return
			}
			n++
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	var bad error
	for i := 0; i < 200 && bad == nil; i++ {
		var res Result
		res, err = Scan(ctx, root, testOpts(4), Discard)
		switch {
		case err != nil:
			bad = fmt.Errorf("scan %d: %w", i, err)
		case res.Stats.BytesLogical >= marker:
			bad = fmt.Errorf("scan %d followed the symlink: BytesLogical = %d", i, res.Stats.BytesLogical)
		}
	}
	close(stop)
	n := <-swapped
	if bad != nil {
		t.Fatal(bad)
	}
	if n < 1 {
		t.Fatalf("swapper made %d exchanges, the race never ran", n)
	}
}

// TestCross_MountAppearedCarriesNewIdentity: the child is entered with the
// stat taken after the open, and everything below judges itself against that
// identity, not against the stale lstat of the covered mount point.
func TestCross_MountAppearedCarriesNewIdentity(t *testing.T) {
	const newDev = 0xBBB00
	d, parentSt, subs := crossFixture(t, "sub")
	sub := subs[0]
	sub.st.Dev = parentSt.Dev // lstat saw the covered directory on the parent's device
	newSt := sub.st
	newSt.Dev, newSt.Ino, newSt.Mtime, newSt.UID = newDev, sub.st.Ino+1000, 4242, 77

	statfsCalls := 0
	w := &walker{ctx: context.Background(), crossFS: true}
	w.statChild = func(*fsx.Dir) (fsx.Stat, error) { return newSt, nil }
	w.statfsChild = func(*fsx.Dir) (fsx.FSInfo, error) {
		statfsCalls++
		return fsx.FSInfo{Type: unix.TMPFS_MAGIC, ReadOnly: true}, nil
	}
	p := &node{dir: d, st: parentSt}
	child, at, _, skip, err := w.cross(p, sub)
	if skip != skipNone || err != nil {
		t.Fatalf("skip = %d, err = %v, want the child entered", skip, err)
	}
	defer func() { _ = child.Close() }()
	if at.st != newSt {
		t.Fatalf("returned stat = %+v, want %+v", at.st, newSt)
	}

	// The node visit builds takes the post-open stat.
	cn := &node{parent: p, st: at.st, at: at, dir: d}
	row := w.dirRow(cn, &agg.Node{})
	if row.Dev != int64(newDev) || row.Ino != int64(newSt.Ino) || row.Mtime != 4242 || row.UID != 77 { //nolint:gosec // test values
		t.Fatalf("dir row = %+v, want the mounted root's dev/ino/mtime/uid", row)
	}

	// A grandchild on the new device is not a crossing: no statfs, not a mount root.
	gc := subdir{name: "sub", st: fsx.Stat{Dev: newDev, Ino: 999}}
	w.statChild = func(*fsx.Dir) (fsx.Stat, error) { return gc.st, nil }
	g, gat, flag, skip, err := w.cross(cn, gc)
	if skip != skipNone || err != nil || flag != 0 {
		t.Fatalf("grandchild: skip = %d, flag = %#x, err = %v, want entered", skip, flag, err)
	}
	_ = g.Close()
	if statfsCalls != 1 {
		t.Fatalf("statfs calls = %d, want 1 (only the mount itself)", statfsCalls)
	}
	if gat.fsRoot || gat.fsType != unix.TMPFS_MAGIC {
		t.Fatalf("grandchild position = %+v, want inside the tmpfs, not its root", gat)
	}

	// The ancestor chain holds the new identity: a descendant that is the
	// mounted root again is a loop, one with the stale identity is not.
	loop := subdir{name: "sub", st: fsx.Stat{Dev: newSt.Dev, Ino: newSt.Ino}}
	w.statChild = func(*fsx.Dir) (fsx.Stat, error) { return loop.st, nil }
	if _, _, _, skip, _ = w.cross(cn, loop); skip != skipError || w.loops.Load() != 1 {
		t.Fatalf("new identity: skip = %d, loops = %d, want a loop", skip, w.loops.Load())
	}
	stale := subdir{name: "sub", st: fsx.Stat{Dev: parentSt.Dev, Ino: sub.st.Ino, MountRoot: false}}
	w.statChild = func(*fsx.Dir) (fsx.Stat, error) { return stale.st, nil }
	c2, _, _, skip, _ := w.cross(cn, stale)
	if skip == skipNone {
		_ = c2.Close()
	}
	if w.loops.Load() != 1 {
		t.Fatalf("stale identity matched the ancestor chain, loops = %d", w.loops.Load())
	}
}

// TestScan_MountAppearedRow: a mount that appears between listing and open is
// recorded with the mounted root's device.
func TestScan_MountAppearedRow(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "a", "b"), 0o750); err != nil {
		t.Fatal(err)
	}
	var aIno uint64
	var ast unix.Stat_t
	if err := unix.Stat(filepath.Join(root, "a"), &ast); err != nil {
		t.Fatal(err)
	}
	aIno = ast.Ino
	const bump = 1 << 20
	opts := testOpts(1)
	opts.CrossFS = true
	opts.forceInline = true
	opts.statChild = func(d *fsx.Dir) (fsx.Stat, error) {
		st, err := d.Stat()
		if st.Ino == aIno {
			st.Dev += bump
		}
		return st, err
	}
	opts.statfsChild = func(*fsx.Dir) (fsx.FSInfo, error) {
		return fsx.FSInfo{Type: unix.TMPFS_MAGIC, ReadOnly: true}, nil
	}
	rec := newRec()
	if _, err := Scan(context.Background(), root, opts, rec); err != nil {
		t.Fatal(err)
	}
	rows := rec.byPath(t)
	if got, want := rows["a"].Dev, int64(uint64(ast.Dev)+bump); got != want { //nolint:unconvert,gosec // Dev width varies by arch
		t.Fatalf("row a Dev = %d, want the post-open %d", got, want)
	}
}
