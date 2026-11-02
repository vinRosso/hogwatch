package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInitDirs(t *testing.T) {
	root := t.TempDir()
	data := filepath.Join(root, "data")
	if err := os.Mkdir(data, 0o700); err != nil {
		t.Fatal(err)
	}
	child := filepath.Join(data, "hogwatch.db")
	if err := os.WriteFile(child, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	zfsDir := filepath.Join(root, "zfs") // missing: created

	uid, gid := os.Getuid(), os.Getgid()
	for range 2 { // the second pass proves it is idempotent
		if err := initDirs([]string{data, zfsDir}, uid, gid); err != nil {
			t.Fatal(err)
		}
	}
	for _, d := range []string{data, zfsDir} {
		fi, err := os.Stat(d)
		if err != nil {
			t.Fatal(err)
		}
		if got := fi.Mode(); got&os.ModeSetgid == 0 || got.Perm() != 0o775 {
			t.Errorf("%s: mode %v, want setgid 0775", d, got)
		}
	}
	fi, err := os.Stat(child)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("a child was touched: mode %v", fi.Mode())
	}
}

func TestInitDirs_RefusesSymlinkAndFile(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "target")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Skip("symlinks unavailable:", err)
	}
	file := filepath.Join(root, "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{link, file} {
		err := initDirs([]string{p}, os.Getuid(), os.Getgid())
		if err == nil || !strings.Contains(err.Error(), p) {
			t.Errorf("initDirs(%s) = %v, want an error naming the path", p, err)
		}
	}
	fi, _ := os.Stat(target)
	if fi.Mode().Perm() != 0o700 {
		t.Errorf("the symlink target was changed: %v", fi.Mode())
	}
}

func TestInitDirs_ChownFailureNamesPath(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root can chown anywhere")
	}
	d := filepath.Join(t.TempDir(), "data")
	if err := os.Mkdir(d, 0o700); err != nil {
		t.Fatal(err)
	}
	err := initDirs([]string{d}, 10001, 10001)
	if err == nil || !strings.Contains(err.Error(), d) {
		t.Fatalf("initDirs = %v, want an error naming %s", err, d)
	}
}
