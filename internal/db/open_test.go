package db

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// A relative path would render as file://data/hogwatch.db, whose first
// segment SQLite reads as a URI authority.
func TestOpen_RelativePath(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	d, err := Open(context.Background(), filepath.Join("data", "..", "hogwatch.db"))
	if err != nil {
		t.Fatalf("Open(relative): %v", err)
	}
	if err := Migrate(context.Background(), d); err != nil {
		t.Fatal(err)
	}
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "hogwatch.db")); err != nil {
		t.Fatalf("DB not created in the working directory: %v", err)
	}
}

func TestFileURI_Relative(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	got, err := FileURI("rel dir/x.db", "mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	want := "file://" + strings.ReplaceAll(filepath.ToSlash(wd), " ", "%20") + "/rel%20dir/x.db?mode=ro"
	if runtime.GOOS != "windows" && got != want {
		t.Fatalf("FileURI = %q, want %q", got, want)
	}
	if !strings.HasPrefix(got, "file:///") {
		t.Fatalf("FileURI = %q, want an absolute file:/// URI", got)
	}
}

// The web (uid 10001) and the root scanner (gid 10001, no
// DAC_OVERRIDE or FOWNER) both write hogwatch.db and its WAL/SHM files, so
// they are group-writable whatever the umask of the process that created them.
func TestOpen_FileModeGroupWritable(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix permission bits do not apply on Windows")
	}
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "hogwatch.db")
	d, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = d.Close() }()
	if err := Migrate(ctx, d); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{path, path + "-wal", path + "-shm"} {
		fi, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		if got := fi.Mode().Perm(); got != 0o660 {
			t.Errorf("%s mode = %o, want 660", filepath.Base(p), got)
		}
	}
}

// Open must not chmod a file it did not create: a root scanner without
// FOWNER cannot chmod the web's file.
func TestOpen_KeepsExistingMode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix permission bits do not apply on Windows")
	}
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "hogwatch.db")
	d, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	if d, err = Open(ctx, path); err != nil {
		t.Fatal(err)
	}
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := fi.Mode().Perm(); got != 0o600 {
		t.Fatalf("mode = %o, want the existing 600", got)
	}
}
