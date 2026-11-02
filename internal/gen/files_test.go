package gen_test

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/vinRosso/hogwatch/internal/gen"
)

// skipNoModeBits skips where Unix permission bits do not apply.
func skipNoModeBits(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("Unix permission bits do not apply on Windows")
	}
}

// Generation files are readable by the web through group 10001.
func TestCreate_FileModeGroupReadable(t *testing.T) {
	skipNoModeBits(t)
	path := newPath(t)
	w := mustCreate(t, path)
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := fi.Mode().Perm(); got != 0o640 {
		t.Fatalf("mode while writing = %o, want 640", got)
	}
	mustFinish(t, w)
	if fi, err = os.Stat(path); err != nil {
		t.Fatal(err)
	}
	if got := fi.Mode().Perm(); got != 0o640 {
		t.Fatalf("mode after Finish = %o, want 640", got)
	}
}

func TestCreate_MakesScansDir(t *testing.T) {
	skipNoModeBits(t)
	path := gen.Path(t.TempDir(), 3, 42)
	w := mustCreate(t, path)
	mustFinish(t, w)
	fi, err := os.Stat(filepath.Dir(path))
	if err != nil {
		t.Fatalf("scans dir: %v", err)
	}
	if !fi.IsDir() {
		t.Fatalf("%s is not a directory", filepath.Dir(path))
	}
	// Group needs r-x so the web can open files the scanner wrote.
	if fi.Mode().Perm()&0o050 != 0o050 {
		t.Fatalf("scans dir mode = %o, want group r-x", fi.Mode().Perm())
	}
	if _, m := mustOpenRO(t, path); m.ScanID != 42 {
		t.Fatalf("meta scan_id = %d", m.ScanID)
	}
}

func openROErr(t *testing.T, path string) error {
	t.Helper()
	d, _, err := gen.OpenReadOnly(t.Context(), path)
	if d != nil {
		_ = d.Close()
	}
	return err
}

// A scanner killed before Finish leaves user_version set but no meta row.
func TestOpenReadOnly_CrashLeftover(t *testing.T) {
	path := newPath(t)
	w, err := gen.Create(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.AddDir(t.Context(), dir(1, 0, "r")); err != nil {
		t.Fatal(err)
	}
	if err := w.Flush(t.Context()); err != nil {
		t.Fatal(err)
	}
	// Abandon: close the handle without Finish or Abort.
	if err := gen.WriterDB(w).Close(); err != nil {
		t.Fatal(err)
	}
	if err := openROErr(t, path); !errors.Is(err, gen.ErrSchemaVersion) {
		t.Fatalf("err = %v, want ErrSchemaVersion", err)
	}
}

func TestOpenReadOnly_NotADatabase(t *testing.T) {
	path := newPath(t)
	junk := make([]byte, 4096)
	copy(junk, "this is not a sqlite database at all")
	if err := os.WriteFile(path, junk, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := openROErr(t, path); !errors.Is(err, gen.ErrSchemaVersion) {
		t.Fatalf("err = %v, want ErrSchemaVersion", err)
	}
}

// A SQLite file with the right user_version but no meta table.
func TestOpenReadOnly_NoMetaTable(t *testing.T) {
	path := newPath(t)
	w := mustCreate(t, path)
	if _, err := gen.WriterDB(w).ExecContext(t.Context(), "DROP TABLE meta"); err != nil {
		t.Fatal(err)
	}
	if err := gen.WriterDB(w).Close(); err != nil {
		t.Fatal(err)
	}
	if err := openROErr(t, path); !errors.Is(err, gen.ErrSchemaVersion) {
		t.Fatalf("err = %v, want ErrSchemaVersion", err)
	}
}

// The file is deleted after the existence check but before SQLite opens it
// (the previous generation is deleted a few minutes after a switch).
func TestOpenReadOnly_DeletedAfterStat(t *testing.T) {
	path := newPath(t)
	w := mustCreate(t, path)
	mustFinish(t, w)
	restore := gen.SetAfterStat(func() {
		if err := os.Remove(path); err != nil {
			t.Error(err)
		}
	})
	defer restore()
	if err := openROErr(t, path); !errors.Is(err, gen.ErrNotExist) {
		t.Fatalf("err = %v, want ErrNotExist", err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("OpenReadOnly recreated %s (stat err %v)", path, err)
	}
}
