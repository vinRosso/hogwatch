package fsx

import "golang.org/x/sys/unix"

// MagicZFS is the statfs f_type of ZFS (not in x/sys/unix).
const MagicZFS int64 = 0x2fc12fc1

// IsPseudo reports whether fsType is a kernel pseudo or stacked filesystem
// whose contents are not real disk usage.
//
// devtmpfs is deliberately absent: it reports TMPFS_MAGIC, so f_type cannot
// tell it from a real tmpfs. Classifying it needs the fstype column of
// /proc/self/mountinfo (M3).
func IsPseudo(fsType int64) bool {
	switch fsType {
	case unix.PROC_SUPER_MAGIC,
		unix.SYSFS_MAGIC,
		unix.CGROUP_SUPER_MAGIC,
		unix.CGROUP2_SUPER_MAGIC,
		unix.DEBUGFS_MAGIC,
		unix.TRACEFS_MAGIC,
		unix.SECURITYFS_MAGIC,
		unix.BPF_FS_MAGIC,
		unix.PSTOREFS_MAGIC,
		unix.OVERLAYFS_SUPER_MAGIC:
		return true
	}
	return false
}

// IsPseudoType is the fstype-name (mountinfo) counterpart of IsPseudo. Unlike
// the magic-number check it can tell devtmpfs from a real tmpfs.
func IsPseudoType(fstype string) bool {
	switch fstype {
	case "proc", "sysfs", "devtmpfs", "cgroup", "cgroup2", "debugfs",
		"tracefs", "securityfs", "bpf", "pstore", "overlay":
		return true
	}
	return false
}
