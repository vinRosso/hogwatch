package gen_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/vinRosso/hogwatch/internal/cube"
	"github.com/vinRosso/hogwatch/internal/gen"
)

// schemaGolden is the schema, column for column and in order. meta carries a
// leading id column (always 1) that keeps it to a single row.
var schemaGolden = map[string][]string{
	"dirs": {"id", "parent_id", "name", "pre", "post", "depth_anchor",
		"dev", "ino", "mtime", "uid", "gid",
		"own_files", "own_alloc", "files", "dirs", "bytes_logical", "bytes_alloc",
		"flags", "error_count"},
	"cube":          {"dir_id", "blob"},
	"top_files":     {"anchor_id", "kind", "rank", "parent_dir_id", "name", "size", "alloc", "mtime", "uid"},
	"top_exts":      {"dir_id", "ext", "bytes", "files"},
	"shared_inodes": {"dev", "ino", "size", "alloc", "mtime", "cube_cell", "winner_dir_id"},
	"shared_links":  {"dev", "ino", "dir_id", "name", "size", "alloc", "mtime"},
	"owners":        {"kind", "id", "name"},
	"errors":        {"dir_id", "name", "op", "errno", "message"},
	"meta":          {"id", "scan_id", "root_id", "scanner_version", "schema_version", "started_at", "finished_at", "stats"},
}

var hostileNames = [][]byte{
	[]byte("\xff"),
	[]byte("a\nb"),
	[]byte("\xe2\x80\xae"), // U+202E RIGHT-TO-LEFT OVERRIDE
	bytes.Repeat([]byte("a"), 255),
	[]byte("=cmd"),
	[]byte("\x00x"),
}

func newPath(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "1-1.db")
}

func mustCreate(t *testing.T, path string) *gen.Writer {
	t.Helper()
	w, err := gen.Create(t.Context(), path)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	t.Cleanup(func() { _ = w.Abort() })
	return w
}

func testMeta() gen.Meta {
	return gen.Meta{
		ScanID: 42, RootID: 3, ScannerVersion: "test",
		StartedAt:  time.Unix(1_700_000_000, 0).UTC(),
		FinishedAt: time.Unix(1_700_000_100, 0).UTC(),
		Stats:      json.RawMessage(`{"dirs":1}`),
	}
}

func mustFinish(t *testing.T, w *gen.Writer) {
	t.Helper()
	if err := w.Finish(t.Context(), testMeta()); err != nil {
		t.Fatalf("Finish: %v", err)
	}
}

func mustOpenRO(t *testing.T, path string) (*sql.DB, gen.Meta) {
	t.Helper()
	d, m, err := gen.OpenReadOnly(t.Context(), path)
	if err != nil {
		t.Fatalf("OpenReadOnly: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return d, m
}

func count(t *testing.T, d *sql.DB, table string) int64 {
	t.Helper()
	var n int64
	if err := d.QueryRowContext(t.Context(), "SELECT count(*) FROM "+table).Scan(&n); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	return n
}

func dir(id, parent int64, name string) gen.Dir {
	return gen.Dir{ID: id, ParentID: parent, Name: []byte(name), Files: 1}
}

func TestPath(t *testing.T) {
	got := gen.Path("/data", 3, 42)
	if want := filepath.Join("/data", "scans", "3-42.db"); got != want {
		t.Fatalf("Path = %q, want %q", got, want)
	}
	if filepath.Separator == '/' && got != "/data/scans/3-42.db" {
		t.Fatalf("Path = %q", got)
	}
}

func TestCreate_RefusesExisting(t *testing.T) {
	path := newPath(t)
	if err := os.WriteFile(path, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if w, err := gen.Create(t.Context(), path); err == nil {
		_ = w.Abort()
		t.Fatal("Create on an existing file succeeded")
	}
	b, err := os.ReadFile(path) //nolint:gosec // test temp dir
	if err != nil || string(b) != "keep" {
		t.Fatalf("existing file changed: %q, %v", b, err)
	}
}

func TestCreate_Schema(t *testing.T) {
	w := mustCreate(t, newPath(t))
	d := gen.WriterDB(w)
	ctx := t.Context()

	var tables []string
	rows, err := d.QueryContext(ctx, `SELECT name FROM sqlite_master WHERE type = 'table' ORDER BY name`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			t.Fatal(err)
		}
		tables = append(tables, n)
	}
	_ = rows.Close()
	want := make([]string, 0, len(schemaGolden))
	for k := range schemaGolden {
		want = append(want, k)
	}
	slices.Sort(want)
	if !slices.Equal(tables, want) {
		t.Fatalf("tables = %v, want %v", tables, want)
	}

	for table, cols := range schemaGolden {
		rows, err := d.QueryContext(ctx, "SELECT name FROM pragma_table_info(?)", table)
		if err != nil {
			t.Fatal(err)
		}
		var got []string
		for rows.Next() {
			var n string
			if err := rows.Scan(&n); err != nil {
				t.Fatal(err)
			}
			got = append(got, n)
		}
		_ = rows.Close()
		if !slices.Equal(got, cols) {
			t.Errorf("%s columns = %v, want %v", table, got, cols)
		}
		var sqlText string
		if err := d.QueryRowContext(ctx, `SELECT sql FROM sqlite_master WHERE name = ?`, table).Scan(&sqlText); err != nil {
			t.Fatal(err)
		}
		if !strings.HasSuffix(strings.TrimSpace(sqlText), "STRICT") {
			t.Errorf("%s is not STRICT: %s", table, sqlText)
		}
	}

	var idx int64
	if err := d.QueryRowContext(ctx,
		`SELECT count(*) FROM sqlite_master WHERE type = 'index' AND sql IS NOT NULL`).Scan(&idx); err != nil {
		t.Fatal(err)
	}
	if idx != 0 {
		t.Errorf("%d secondary indexes before BuildIndexes, want 0", idx)
	}

	var mode string
	if err := d.QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&mode); err != nil {
		t.Fatal(err)
	}
	if mode != "off" {
		t.Errorf("journal_mode = %q, want off", mode)
	}
	var syncMode, uv int64
	if err := d.QueryRowContext(ctx, "PRAGMA synchronous").Scan(&syncMode); err != nil {
		t.Fatal(err)
	}
	if syncMode != 0 {
		t.Errorf("synchronous = %d, want 0 (OFF)", syncMode)
	}
	if err := d.QueryRowContext(ctx, "PRAGMA user_version").Scan(&uv); err != nil {
		t.Fatal(err)
	}
	if uv != gen.SchemaVersion {
		t.Errorf("user_version = %d, want %d", uv, gen.SchemaVersion)
	}
}

func TestWriter_BatchBoundary(t *testing.T) {
	path := newPath(t)
	w := mustCreate(t, path)
	ctx := t.Context()

	var acc cube.Acc
	acc.Add(cube.Cell{Category: 1, Age: 2, Size: 3}, 4096, 2)
	blob, err := acc.Encode()
	if err != nil {
		t.Fatal(err)
	}
	const n = gen.BatchRows + 1
	for i := int64(1); i <= n; i++ {
		if err := w.AddDir(ctx, dir(i, i-1, "d")); err != nil {
			t.Fatalf("AddDir %d: %v", i, err)
		}
		if err := w.AddCube(ctx, i, blob); err != nil {
			t.Fatalf("AddCube %d: %v", i, err)
		}
		if i == gen.BatchRows/2 {
			// BatchRows buffered rows across tables trigger one write.
			if got := count(t, gen.WriterDB(w), "dirs"); got != gen.BatchRows/2 {
				t.Fatalf("after %d buffered rows, dirs holds %d rows, want %d", gen.BatchRows, got, gen.BatchRows/2)
			}
		}
	}
	for i := int64(1); i <= n; i++ {
		if err := w.SetPrePost(ctx, i, i, n); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.BuildIndexes(ctx); err != nil {
		t.Fatalf("BuildIndexes: %v", err)
	}
	mustFinish(t, w)

	d, m := mustOpenRO(t, path)
	if got := count(t, d, "dirs"); got != n {
		t.Errorf("dirs = %d, want %d", got, n)
	}
	if got := count(t, d, "cube"); got != n {
		t.Errorf("cube = %d, want %d", got, n)
	}
	var unset int64
	if err := d.QueryRowContext(ctx, `SELECT count(*) FROM dirs WHERE pre IS NULL OR post != ?`, n).Scan(&unset); err != nil {
		t.Fatal(err)
	}
	if unset != 0 {
		t.Errorf("%d dirs without pre/post", unset)
	}
	for _, id := range []int64{1, gen.BatchRows, n} {
		var b []byte
		if err := d.QueryRowContext(ctx, `SELECT blob FROM cube WHERE dir_id = ?`, id).Scan(&b); err != nil {
			t.Fatal(err)
		}
		got, err := cube.Decode(cube.Blob(b))
		if err != nil {
			t.Fatalf("Decode dir %d: %v", id, err)
		}
		if got != acc {
			t.Errorf("cube of dir %d differs", id)
		}
	}
	want := testMeta()
	if m.ScanID != want.ScanID || m.RootID != want.RootID || m.ScannerVersion != want.ScannerVersion ||
		!m.StartedAt.Equal(want.StartedAt) || !m.FinishedAt.Equal(want.FinishedAt) ||
		!bytes.Equal(m.Stats, want.Stats) {
		t.Errorf("meta = %+v, want %+v", m, want)
	}
}

func TestWriter_HostileNames(t *testing.T) {
	path := newPath(t)
	w := mustCreate(t, path)
	ctx := t.Context()
	if err := w.AddDir(ctx, dir(1, 0, "")); err != nil {
		t.Fatal(err)
	}
	for i, name := range hostileNames {
		id := int64(i + 2)
		steps := []error{
			w.AddDir(ctx, gen.Dir{ID: id, ParentID: 1, Name: name}),
			w.AddTopFile(ctx, gen.TopFile{AnchorID: 1, Kind: "largest", Rank: id, ParentDirID: id, Name: name}),
			w.AddSharedLink(ctx, gen.SharedLink{Dev: 1, Ino: 1, DirID: id, Name: name}),
			w.AddError(ctx, gen.ErrorRow{DirID: id, Name: name, Op: "lstat", Errno: 13, Message: "permission denied"}),
		}
		if err := errors.Join(steps...); err != nil {
			t.Fatal(err)
		}
	}
	mustFinish(t, w)
	d, _ := mustOpenRO(t, path)

	for _, q := range []string{
		`SELECT id, name FROM dirs WHERE id > 1`,
		`SELECT rank, name FROM top_files`,
		`SELECT dir_id, name FROM shared_links`,
		`SELECT dir_id, name FROM errors`,
	} {
		rows, err := d.QueryContext(ctx, q)
		if err != nil {
			t.Fatal(err)
		}
		seen := 0
		for rows.Next() {
			var id int64
			var name []byte
			if err := rows.Scan(&id, &name); err != nil {
				t.Fatal(err)
			}
			if want := hostileNames[id-2]; !bytes.Equal(name, want) {
				t.Errorf("%s: id %d name = %q, want %q", q, id, name, want)
			}
			seen++
		}
		_ = rows.Close()
		if seen != len(hostileNames) {
			t.Errorf("%s: %d rows, want %d", q, seen, len(hostileNames))
		}
	}
	var root []byte
	var typ string
	if err := d.QueryRowContext(ctx, `SELECT name, typeof(name) FROM dirs WHERE id = 1`).Scan(&root, &typ); err != nil {
		t.Fatal(err)
	}
	if len(root) != 0 || typ != "blob" {
		t.Errorf("empty root name read back as %q (%s)", root, typ)
	}
}

func TestWriter_ErrorCap(t *testing.T) {
	defer gen.SetMaxErrorRows(3)()
	path := newPath(t)
	w := mustCreate(t, path)
	for i := range 5 {
		if err := w.AddError(t.Context(), gen.ErrorRow{DirID: 1, Name: []byte{byte('a' + i)}, Op: "readdir", Errno: 5}); err != nil {
			t.Fatal(err)
		}
	}
	if stored, dropped := w.ErrorCounts(); stored != 3 || dropped != 2 {
		t.Fatalf("ErrorCounts = (%d, %d), want (3, 2)", stored, dropped)
	}
	mustFinish(t, w)
	d, _ := mustOpenRO(t, path)
	if got := count(t, d, "errors"); got != 3 {
		t.Fatalf("errors rows = %d, want 3", got)
	}
}

func TestWriter_RejectsBadEnums(t *testing.T) {
	w := mustCreate(t, newPath(t))
	ctx := t.Context()
	steps := []error{
		w.AddTopFile(ctx, gen.TopFile{AnchorID: 1, Kind: "biggest", Rank: 1, Name: []byte("x")}),
		w.AddError(ctx, gen.ErrorRow{DirID: 1, Name: []byte("x"), Op: "stat"}),
		w.AddOwner(ctx, gen.Owner{Kind: "user", ID: 1, Name: "x"}),
	}
	for i, err := range steps {
		if err == nil {
			t.Errorf("step %d accepted a bad enum value", i)
		}
	}
}

func TestWriter_Abort(t *testing.T) {
	path := newPath(t)
	w, err := gen.Create(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.AddDir(t.Context(), dir(1, 0, "r")); err != nil {
		t.Fatal(err)
	}
	if err := w.Abort(); err != nil {
		t.Fatalf("Abort: %v", err)
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("Abort left %v", entries)
	}
}

func TestFinish_LeavesOnlyDBFile(t *testing.T) {
	path := newPath(t)
	w := mustCreate(t, path)
	ctx := t.Context()
	steps := []error{
		w.AddDir(ctx, dir(1, 0, "r")),
		w.AddTopExt(ctx, gen.TopExt{DirID: 1, Ext: "mkv", Bytes: 10, Files: 1}),
		w.AddSharedInode(ctx, gen.SharedInode{Dev: 1, Ino: 2, Size: 3, Alloc: 4096, Mtime: 5, CubeCell: 6}),
		w.AddOwner(ctx, gen.Owner{Kind: "uid", ID: 1000, Name: "alice"}),
		w.BuildIndexes(ctx),
	}
	if err := errors.Join(steps...); err != nil {
		t.Fatal(err)
	}
	mustFinish(t, w)
	if err := w.Abort(); err != nil {
		t.Fatalf("Abort after Finish: %v", err)
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != filepath.Base(path) {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Fatalf("directory holds %v, want only %s", names, filepath.Base(path))
	}
	d, _ := mustOpenRO(t, path)
	for _, table := range []string{"dirs", "top_exts", "shared_inodes", "owners", "meta"} {
		if got := count(t, d, table); got != 1 {
			t.Errorf("%s rows = %d, want 1", table, got)
		}
	}
}

func TestBuildIndexes_DuplicateNameRejected(t *testing.T) {
	w := mustCreate(t, newPath(t))
	ctx := t.Context()
	if err := errors.Join(
		w.AddDir(ctx, dir(1, 0, "r")),
		w.AddDir(ctx, dir(2, 1, "same")),
		w.AddDir(ctx, dir(3, 1, "same")),
	); err != nil {
		t.Fatal(err)
	}
	if err := w.BuildIndexes(ctx); err == nil {
		t.Fatal("BuildIndexes accepted two dirs with the same parent and name")
	}
}

// The scanner container has no writable /tmp, so SQLite's sort temp files
// must go beside the generation file (error 6410 otherwise).
func TestWriter_TempDirBesideFile(t *testing.T) {
	path := newPath(t)
	w := mustCreate(t, path)
	var got string
	if err := gen.WriterDB(w).QueryRowContext(t.Context(), "PRAGMA temp_store_directory").Scan(&got); err != nil {
		t.Fatal(err)
	}
	if want := filepath.Dir(path); got != want {
		t.Fatalf("temp_store_directory = %q, want %q", got, want)
	}
}

func TestOpenReadOnly_Immutable(t *testing.T) {
	path := newPath(t)
	w := mustCreate(t, path)
	if err := w.AddDir(t.Context(), dir(1, 0, "r")); err != nil {
		t.Fatal(err)
	}
	mustFinish(t, w)
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(10 * time.Millisecond) // a write would move mtime past before's

	d, _ := mustOpenRO(t, path)
	if _, err := d.ExecContext(t.Context(), `INSERT INTO dirs (id, parent_id, name, depth_anchor, dev, ino, mtime, uid, gid,
		own_files, own_alloc, files, dirs, bytes_logical, bytes_alloc, flags, error_count)
		VALUES (2, 1, x'61', 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0)`); err == nil {
		t.Fatal("INSERT through the read-only handle succeeded")
	}
	_ = count(t, d, "dirs")
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !after.ModTime().Equal(before.ModTime()) || after.Size() != before.Size() {
		t.Fatalf("file changed: mtime %v -> %v, size %d -> %d",
			before.ModTime(), after.ModTime(), before.Size(), after.Size())
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("read-only open left extra files: %v", entries)
	}
}

func TestOpenReadOnly_Missing(t *testing.T) {
	path := newPath(t)
	d, _, err := gen.OpenReadOnly(t.Context(), path)
	if d != nil {
		_ = d.Close()
	}
	if !errors.Is(err, gen.ErrNotExist) {
		t.Fatalf("err = %v, want ErrNotExist", err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("OpenReadOnly created %s (stat err %v)", path, err)
	}
}

func TestOpenReadOnly_SchemaMismatch(t *testing.T) {
	path := newPath(t)
	w := mustCreate(t, path)
	mustFinish(t, w)

	rw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rw.ExecContext(context.Background(), "PRAGMA user_version = 99"); err != nil {
		t.Fatal(err)
	}
	if err := rw.Close(); err != nil {
		t.Fatal(err)
	}

	d, _, err := gen.OpenReadOnly(t.Context(), path)
	if d != nil {
		_ = d.Close()
	}
	if !errors.Is(err, gen.ErrSchemaVersion) {
		t.Fatalf("err = %v, want ErrSchemaVersion", err)
	}
}

func TestOpenReadOnly_PathSpecialChars(t *testing.T) {
	parent := filepath.Join(t.TempDir(), "a b#c?d%20e")
	if err := os.Mkdir(parent, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(parent, "1-1.db")
	w := mustCreate(t, path)
	if err := w.AddDir(t.Context(), dir(1, 0, "r")); err != nil {
		t.Fatal(err)
	}
	mustFinish(t, w)
	d, m := mustOpenRO(t, path)
	if got := count(t, d, "dirs"); got != 1 {
		t.Fatalf("dirs = %d, want 1", got)
	}
	if m.ScanID != 42 {
		t.Fatalf("meta scan_id = %d", m.ScanID)
	}
	entries, err := os.ReadDir(parent)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "1-1.db" {
		t.Fatalf("directory holds %v", entries)
	}
}
