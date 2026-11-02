package main

import (
	"context"
	"fmt"
	"io"
	"os"
)

// webUID and webGID own the state directories: the web runs as this user and
// the root scanner joins its group.
const webUID, webGID = 10001, 10001

// initDirs prepares the bind-mounted state directories: each is created when
// missing, handed to uid:gid and set to mode 2775 (setgid, so files made by
// the group-10001 scanner stay shared). Only the directory itself changes,
// never its children, and a symlink or a plain file is refused.
func initDirs(dirs []string, uid, gid int) error {
	for _, d := range dirs {
		fi, err := os.Lstat(d)
		if os.IsNotExist(err) {
			if err = os.Mkdir(d, 0o700); err == nil {
				fi, err = os.Lstat(d)
			}
		}
		if err != nil {
			return fmt.Errorf("%s: %w", d, err)
		}
		if !fi.IsDir() {
			return fmt.Errorf("%s: not a directory (or a symlink)", d)
		}
		if err := os.Lchown(d, uid, gid); err != nil {
			return fmt.Errorf("%s: set owner %d:%d: %w", d, uid, gid, err)
		}
		if err := os.Chmod(d, os.ModeSetgid|0o775); err != nil {
			return fmt.Errorf("%s: set mode 2775: %w", d, err)
		}
	}
	return nil
}

// initCmd is the one-shot first-start step of the compose file: it replaces
// the manual mkdir and chown of the state directories.
func initCmd(_ context.Context, _ []string, stdout, stderr io.Writer) int {
	dirs := []string{"/data", "/zfs"}
	if err := initDirs(dirs, webUID, webGID); err != nil {
		_, _ = fmt.Fprintf(stderr, "hogwatch init: %v\n", err)
		return 1
	}
	_, _ = fmt.Fprintf(stdout, "hogwatch init: %v are ready (owner %d:%d, mode 2775)\n", dirs, webUID, webGID)
	return 0
}
