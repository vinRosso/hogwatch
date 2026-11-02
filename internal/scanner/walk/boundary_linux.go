package walk

import (
	"github.com/vinRosso/hogwatch/internal/gen"
	"github.com/vinRosso/hogwatch/internal/scanner/fsx"
	"golang.org/x/sys/unix"
)

// zfsCtlName is the ZFS snapshot control directory at a dataset root.
const zfsCtlName = ".zfs"

// skipZFSCtl reports whether the entry name of a directory must be skipped
// because it is the .zfs control directory at the root of a ZFS filesystem.
// Walking it would mount snapshots and count their data again.
func skipZFSCtl(parentFSType int64, parentIsFSRoot bool, name string) bool {
	return parentFSType == fsx.MagicZFS && parentIsFSRoot && name == zfsCtlName
}

// where tells a directory which filesystem it is on and whether it is that
// filesystem's root, which decides how its children are treated.
type where struct {
	fsType int64
	fsRoot bool
	// st is the stat of the opened directory itself, taken after the open. cross
	// returns it for a walked child, whose node must use it rather than the
	// listing-time lstat: a mount may have appeared in between.
	st fsx.Stat
}

// rootWhere classifies the walk root: see classifyRoot.
func rootWhere(d *fsx.Dir, st fsx.Stat, fsType int64) where {
	up, err := d.Lstat("..")
	return classifyRoot(st, up, err, fsType)
}

// classifyRoot decides from the root's stat st and the lstat of its ".." (up,
// or err) whether the root is a filesystem root. It is when ".." lies on
// another device, when ".." is the root itself (the root is "/"), or when the
// kernel says it is a mount root. When ".." cannot be examined the root is
// assumed to be one: that only ever skips more (.zfs), never less.
func classifyRoot(st, up fsx.Stat, err error, fsType int64) where {
	fsRoot := err != nil || up.Dev != st.Dev || up.Ino == st.Ino || st.MountRoot
	return where{fsType: fsType, fsRoot: fsRoot}
}

// skipKind says what the walk does with a subdirectory.
type skipKind int

const (
	skipNone  skipKind = iota // walk it: the opened child is returned
	skipDrop                  // not walked and no row (.zfs at a dataset root)
	skipRow                   // not walked; a zero-count row carrying the returned flag
	skipError                 // could not be opened; an error row was sent, and the row has FlagUnreadable
)

// changedErr is the error of a directory entry that was replaced between the
// listing and the open. It reports ENOENT: the entry we listed is gone.
type changedErr struct{}

func (changedErr) Error() string { return "changed during scan" }
func (changedErr) Unwrap() error { return unix.ENOENT }

// isNested reports whether st is the top directory of another root.
func (w *walker) isNested(st fsx.Stat) bool {
	_, ok := w.nested[DevIno{Dev: st.Dev, Ino: st.Ino}]
	return ok
}

// stat and statfs run the test seams, or the descriptor's own calls.
func (w *walker) stat(d *fsx.Dir) (fsx.Stat, error) {
	if w.statChild != nil {
		return w.statChild(d)
	}
	return d.Stat()
}

func (w *walker) statfs(d *fsx.Dir) (fsx.FSInfo, error) {
	if w.statfsChild != nil {
		return w.statfsChild(d)
	}
	return d.Statfs()
}

// cross decides what to do with subdirectory s of parent, whose own Lstat is
// parent.st, whose position is parent.at and whose descriptor is parent.dir.
// For skipNone it returns the opened child and its position. For skipRow and
// skipError it returns the flag of the row to store. It counts every skip; an
// open, stat or statfs failure is counted as an error and returned as cerr; the
// caller stores its error row (op open, keyed by the child, like a readdir
// failure).
//
// The checks run in this order:
//  1. the .zfs name at a ZFS filesystem root: dropped, no row;
//     1a. the anchor trie marks the directory excluded: FlagExcluded, not opened;
//  2. (dev, ino) of another root's top directory: FlagNestedRootSkipped;
//  3. a mount crossing (other device, or a mount root) without CrossFS:
//     FlagMountpointSkipped;
//  4. open the child and stat the descriptor. The lstat was taken at listing
//     time, so a different inode on the same device means the entry was
//     replaced (error, "changed during scan"); a different device means a mount
//     appeared, and the child is judged as a crossing on that device;
//  5. a crossing's filesystem: pseudo by statfs magic or by the mountinfo type
//     of its device is FlagMountpointSkipped; writable without AllowWritable is
//     FlagWritableMountRefused;
//  6. (dev, ino) equal to an ancestor (a bind loop): ELOOP, not entered.
func (w *walker) cross(parent *node, s subdir) (child *fsx.Dir, at where, flag int64, skip skipKind, cerr error) {
	if skipZFSCtl(parent.at.fsType, parent.at.fsRoot, s.name) {
		w.zfsCtlSkipped.Add(1)
		return nil, where{}, 0, skipDrop, nil
	}
	if parent.anchor != nil {
		if t := parent.anchor.children[s.name]; t != nil && t.excluded {
			w.excluded.Add(1)
			return nil, where{}, gen.FlagExcluded, skipRow, nil
		}
	}
	if w.isNested(s.st) {
		w.nestedSkipped.Add(1)
		return nil, where{}, gen.FlagNestedRootSkipped, skipRow, nil
	}
	crossing := s.st.Dev != parent.st.Dev || s.st.MountRoot
	if crossing && !w.crossFS {
		w.mountsSkipped.Add(1)
		return nil, where{}, gen.FlagMountpointSkipped, skipRow, nil
	}

	child, err := parent.dir.OpenChild(s.name)
	if err != nil {
		return nil, where{}, gen.FlagUnreadable, w.openFailed(err), err
	}
	cs, err := w.stat(child)
	if err != nil {
		_ = child.Close()
		return nil, where{}, gen.FlagUnreadable, w.openFailed(err), err
	}
	switch {
	case cs.Dev != s.st.Dev: // a mount appeared (automount, or mounted late)
		if w.isNested(cs) {
			_ = child.Close()
			w.nestedSkipped.Add(1)
			return nil, where{}, gen.FlagNestedRootSkipped, skipRow, nil
		}
		crossing = cs.Dev != parent.st.Dev || cs.MountRoot
		if crossing && !w.crossFS {
			_ = child.Close()
			w.mountsSkipped.Add(1)
			return nil, where{}, gen.FlagMountpointSkipped, skipRow, nil
		}
	case cs.Ino != s.st.Ino: // the entry was replaced
		_ = child.Close()
		w.changed.Add(1)
		err := changedErr{}
		return nil, where{}, gen.FlagUnreadable, w.openFailed(err), err
	}

	at = where{fsType: parent.at.fsType}
	if crossing {
		info, err := w.statfs(child)
		if err != nil {
			_ = child.Close()
			return nil, where{}, gen.FlagUnreadable, w.openFailed(err), err
		}
		if fsx.IsPseudo(info.Type) || fsx.IsPseudoType(w.mountTypes[cs.Dev]) {
			_ = child.Close()
			w.pseudoSkipped.Add(1)
			return nil, where{}, gen.FlagMountpointSkipped, skipRow, nil
		}
		if !info.ReadOnly && !w.allowWritable {
			_ = child.Close()
			w.writableRefused.Add(1)
			return nil, where{}, gen.FlagWritableMountRefused, skipRow, nil
		}
		at = where{fsType: info.Type, fsRoot: true}
	}

	for a := parent; a != nil; a = a.parent {
		if a.st.Dev == cs.Dev && a.st.Ino == cs.Ino {
			_ = child.Close()
			w.loops.Add(1)
			return nil, where{}, gen.FlagUnreadable, w.openFailed(unix.ELOOP), unix.ELOOP
		}
	}
	at.st = cs
	return child, at, 0, skipNone, nil
}

// openFailed counts err and returns skipError. The caller that owns the child's
// id sends the error row (visit), keyed by the child itself.
func (w *walker) openFailed(err error) skipKind {
	w.fail(err)
	return skipError
}
