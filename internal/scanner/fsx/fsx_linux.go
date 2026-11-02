package fsx

import (
	"errors"
	"io/fs"
	"strings"

	"golang.org/x/sys/unix"
)

const (
	dirFlags   = unix.O_RDONLY | unix.O_DIRECTORY | unix.O_NOFOLLOW | unix.O_CLOEXEC
	direntSize = 32 << 10

	// statxFlags are the flags common to every statx call: never trigger an
	// automount, and stat as plain stat(2) would.
	statxFlags = unix.AT_NO_AUTOMOUNT | unix.AT_STATX_SYNC_AS_STAT
)

// Test seams for syscalls that old kernels or seccomp profiles may refuse.
var (
	openat2 = unix.Openat2
	statx   = unix.Statx
)

// Dir is an open directory descriptor. Every operation is relative to it and
// never follows a symlink. A Dir is not safe for concurrent use.
type Dir struct {
	fd  int
	buf []byte // getdents64 buffer, allocated on first ReadNames
}

// Stat is the subset of struct stat the scanner records. Blocks counts
// 512-byte units; Mtime is Unix seconds.
type Stat struct {
	Dev, Ino, Nlink uint64
	Mode            uint32
	Size, Blocks    int64
	Mtime           int64
	UID, GID        uint32
	// MountRoot is true iff statx reported STATX_ATTR_MOUNT_ROOT in both the
	// attribute mask and the attributes; false on kernels without it.
	MountRoot bool
}

// IsDir reports whether the entry is a directory.
func (s Stat) IsDir() bool { return s.Mode&unix.S_IFMT == unix.S_IFDIR }

// IsSymlink reports whether the entry is a symbolic link.
func (s Stat) IsSymlink() bool { return s.Mode&unix.S_IFMT == unix.S_IFLNK }

// FSInfo describes the filesystem a Dir lives on.
type FSInfo struct {
	Type     int64 // statfs f_type
	ReadOnly bool  // ST_RDONLY in f_flags
	// Total and Avail are f_blocks and f_bavail times f_frsize, in bytes.
	Total, Avail int64
}

// OpenRoot opens path as a directory without following a final symlink. It is
// the only path-based open; everything below it goes through OpenChild.
func OpenRoot(path string) (*Dir, error) {
	fd, err := unix.Open(path, dirFlags, 0)
	if err != nil {
		return nil, &fs.PathError{Op: "open", Path: path, Err: err}
	}
	return &Dir{fd: fd}, nil
}

// OpenChild opens the subdirectory name of d with openat, refusing symlinks.
func (d *Dir) OpenChild(name string) (*Dir, error) {
	if !validName(name) {
		return nil, &fs.PathError{Op: "openat", Path: name, Err: unix.EINVAL}
	}
	fd, err := unix.Openat(d.fd, name, dirFlags, 0)
	if err != nil {
		return nil, &fs.PathError{Op: "openat", Path: name, Err: err}
	}
	return &Dir{fd: fd}, nil
}

// OpenBeneath opens the subdirectory rel of d, where rel is "" (d itself) or
// "/"-joined segments, none empty, "." or ".." or containing NUL; anything else
// is EINVAL. It uses openat2 with RESOLVE_BENEATH|NO_SYMLINKS|NO_XDEV|
// NO_MAGICLINKS, so no symlink is followed and no mount is crossed. When
// openat2 is unavailable (ENOSYS, or EPERM from a seccomp profile) it walks the
// segments with OpenChild and checks after each that the device is unchanged
// and the directory is not a mount root, else EXDEV.
func (d *Dir) OpenBeneath(rel string) (*Dir, error) {
	if !validRel(rel) {
		return nil, &fs.PathError{Op: "openat2", Path: rel, Err: unix.EINVAL}
	}
	path := rel
	if path == "" {
		path = "."
	}
	const resolve = unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS |
		unix.RESOLVE_NO_XDEV | unix.RESOLVE_NO_MAGICLINKS
	fd, err := openat2(d.fd, path, &unix.OpenHow{Flags: dirFlags, Resolve: resolve})
	if err == nil {
		return &Dir{fd: fd}, nil
	}
	if err == unix.ENOTDIR {
		// O_DIRECTORY reports a trailing symlink as ENOTDIR. Probe with
		// O_PATH|O_NOFOLLOW and fstat the result; the probe descriptor is
		// closed at once and never reaches the caller.
		pfd, perr := openat2(d.fd, path, &unix.OpenHow{
			Flags: unix.O_PATH | unix.O_NOFOLLOW | unix.O_CLOEXEC, Resolve: resolve,
		})
		switch perr {
		case nil:
			if st, serr := (&Dir{fd: pfd}).Stat(); serr == nil && st.IsSymlink() {
				err = unix.ELOOP
			}
			_ = unix.Close(pfd)
		case unix.ELOOP:
			err = unix.ELOOP
		}
	}
	if err != unix.ENOSYS && err != unix.EPERM {
		return nil, &fs.PathError{Op: "openat2", Path: rel, Err: err}
	}
	return d.openBeneathFallback(rel)
}

// openBeneathFallback is the component-wise walk used without openat2.
func (d *Dir) openBeneathFallback(rel string) (*Dir, error) {
	fail := func(err error) (*Dir, error) {
		return nil, &fs.PathError{Op: "openat2", Path: rel, Err: err}
	}
	base, err := d.Stat()
	if err != nil {
		return fail(unwrapErrno(err))
	}
	if rel == "" {
		fd, err := unix.Openat(d.fd, ".", dirFlags, 0)
		if err != nil {
			return fail(err)
		}
		return &Dir{fd: fd}, nil
	}
	cur := d
	for seg := range strings.SplitSeq(rel, "/") {
		next, err := cur.OpenChild(seg)
		if err != nil {
			err = unwrapErrno(err)
			// O_DIRECTORY|O_NOFOLLOW on a symlink reports ENOTDIR on some
			// kernels; openat2 reports ELOOP. Normalise.
			if err == unix.ENOTDIR {
				if st, serr := cur.Lstat(seg); serr == nil && st.IsSymlink() {
					err = unix.ELOOP
				}
			}
			if cur != d {
				_ = cur.Close()
			}
			return fail(err)
		}
		if cur != d {
			_ = cur.Close()
		}
		cur = next
		st, err := cur.Stat()
		if err != nil {
			_ = cur.Close()
			return fail(unwrapErrno(err))
		}
		if st.Dev != base.Dev || st.MountRoot {
			_ = cur.Close()
			return fail(unix.EXDEV)
		}
	}
	return cur, nil
}

// unwrapErrno returns the error inside a *fs.PathError.
func unwrapErrno(err error) error {
	var pe *fs.PathError
	if errors.As(err, &pe) {
		return pe.Err
	}
	return err
}

// validRel reports whether rel is "" or "/"-joined plain segments.
func validRel(rel string) bool {
	if rel == "" {
		return true
	}
	if strings.ContainsRune(rel, 0) {
		return false
	}
	for seg := range strings.SplitSeq(rel, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return false
		}
	}
	return true
}

// ReadNames streams the entry names of d (without "." and "..") to fn through
// a 32 KiB getdents64 buffer owned by d, and stops at the first error from fn.
// It reads from the descriptor's current offset, so call it once per Dir.
// Names are raw bytes and may be invalid UTF-8.
func (d *Dir) ReadNames(fn func(name string) error) error {
	if d.buf == nil {
		d.buf = make([]byte, direntSize)
	}
	for {
		n, err := unix.Getdents(d.fd, d.buf)
		if err != nil {
			return &fs.PathError{Op: "getdents", Path: ".", Err: err}
		}
		if n <= 0 {
			return nil
		}
		_, _, names := unix.ParseDirent(d.buf[:n], -1, nil)
		for _, name := range names {
			if err := fn(name); err != nil {
				return err
			}
		}
	}
}

// Lstat stats the entry name of d without following a symlink.
func (d *Dir) Lstat(name string) (Stat, error) {
	if !validName(name) {
		return Stat{}, &fs.PathError{Op: "statx", Path: name, Err: unix.EINVAL}
	}
	var sx unix.Statx_t
	err := statx(d.fd, name, unix.AT_SYMLINK_NOFOLLOW|statxFlags, unix.STATX_BASIC_STATS, &sx)
	if err == nil {
		return fromStatx(&sx), nil
	}
	if err != unix.ENOSYS && err != unix.EPERM { // EPERM: old seccomp profile
		return Stat{}, &fs.PathError{Op: "statx", Path: name, Err: err}
	}
	var st unix.Stat_t
	if err := unix.Fstatat(d.fd, name, &st, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return Stat{}, &fs.PathError{Op: "fstatat", Path: name, Err: err}
	}
	return toStat(&st), nil
}

// Stat stats d itself (statx with AT_EMPTY_PATH; fstat on ENOSYS or EPERM).
func (d *Dir) Stat() (Stat, error) {
	var sx unix.Statx_t
	err := statx(d.fd, "", unix.AT_EMPTY_PATH|statxFlags, unix.STATX_BASIC_STATS, &sx)
	if err == nil {
		return fromStatx(&sx), nil
	}
	if err != unix.ENOSYS && err != unix.EPERM { // EPERM: old seccomp profile
		return Stat{}, &fs.PathError{Op: "statx", Path: ".", Err: err}
	}
	var st unix.Stat_t
	if err := unix.Fstat(d.fd, &st); err != nil {
		return Stat{}, &fs.PathError{Op: "fstat", Path: ".", Err: err}
	}
	return toStat(&st), nil
}

// Statfs describes the filesystem d lives on (fstatfs).
func (d *Dir) Statfs() (FSInfo, error) {
	var sf unix.Statfs_t
	if err := unix.Fstatfs(d.fd, &sf); err != nil {
		return FSInfo{}, &fs.PathError{Op: "fstatfs", Path: ".", Err: err}
	}
	// The field types vary by architecture, hence the explicit conversions.
	return FSInfo{
		Type:     int64(sf.Type),
		ReadOnly: int64(sf.Flags)&unix.ST_RDONLY != 0,
		Total:    int64(sf.Blocks) * int64(sf.Frsize), //nolint:gosec // kernel values fit in int64
		Avail:    int64(sf.Bavail) * int64(sf.Frsize), //nolint:gosec // kernel values fit in int64
	}, nil
}

// Close releases the descriptor.
func (d *Dir) Close() error {
	if err := unix.Close(d.fd); err != nil {
		return &fs.PathError{Op: "close", Path: ".", Err: err}
	}
	return nil
}

// validName rejects anything that is not a single directory entry name, so a
// caller can never make the *at calls walk a path.
func validName(name string) bool {
	return name != "" && !strings.ContainsAny(name, "/\x00")
}

func toStat(st *unix.Stat_t) Stat {
	return Stat{
		Dev:    uint64(st.Dev),
		Ino:    uint64(st.Ino),
		Nlink:  uint64(st.Nlink),
		Mode:   uint32(st.Mode),
		Size:   int64(st.Size),
		Blocks: int64(st.Blocks),
		Mtime:  int64(st.Mtim.Sec),
		UID:    st.Uid,
		GID:    st.Gid,
	}
}

// fromStatx converts a statx result. Blocks is in 512-byte units as in
// struct stat.
func fromStatx(sx *unix.Statx_t) Stat {
	const mnt = unix.STATX_ATTR_MOUNT_ROOT
	return Stat{
		Dev:       unix.Mkdev(sx.Dev_major, sx.Dev_minor),
		Ino:       sx.Ino,
		Nlink:     uint64(sx.Nlink),
		Mode:      uint32(sx.Mode),
		Size:      int64(sx.Size),   //nolint:gosec // kernel values fit in int64
		Blocks:    int64(sx.Blocks), //nolint:gosec // kernel values fit in int64
		Mtime:     sx.Mtime.Sec,
		UID:       sx.Uid,
		GID:       sx.Gid,
		MountRoot: sx.Attributes_mask&mnt != 0 && sx.Attributes&mnt != 0,
	}
}
