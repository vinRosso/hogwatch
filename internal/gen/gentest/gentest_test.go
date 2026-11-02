package gentest_test

import (
	"database/sql"
	"fmt"
	"slices"
	"testing"

	"github.com/vinRosso/hogwatch/internal/cube"
	"github.com/vinRosso/hogwatch/internal/gen"
	"github.com/vinRosso/hogwatch/internal/gen/gentest"
)

func open(t *testing.T, path string) *sql.DB {
	t.Helper()
	d, _, err := gen.OpenReadOnly(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return d
}

func sumCube(t *testing.T, d *sql.DB, dirID int64, f cube.Filter) (bytes, files int64) {
	t.Helper()
	var blob []byte
	if err := d.QueryRowContext(t.Context(), `SELECT blob FROM cube WHERE dir_id = ?`, dirID).Scan(&blob); err != nil {
		t.Fatalf("cube of dir %d: %v", dirID, err)
	}
	b, n, err := f.Apply(cube.Blob(blob))
	if err != nil {
		t.Fatal(err)
	}
	return b, n
}

var (
	video = cube.Cell{Category: uint8(cube.Video), Age: 0, Size: 1}
	image = cube.Cell{Category: uint8(cube.Image), Age: 2, Size: 0}
)

func TestBuildWith_Cube(t *testing.T) {
	dirs := []gentest.Dir{
		{Rel: "a", Cells: map[cube.Cell][2]int64{video: {100, 1}}},
		{Rel: "a/b", Cells: map[cube.Cell][2]int64{video: {50, 2}, image: {7, 1}}},
		{Rel: "c", Cells: map[cube.Cell][2]int64{image: {3, 3}}},
	}
	d := open(t, gentest.BuildWith(t, t.TempDir(), 1, 1, dirs, gentest.BuildOpts{}))
	// ids in sorted path order: 1 root, 2 a, 3 a/b, 4 c
	if b, n := sumCube(t, d, 1, cube.Filter{}); b != 160 || n != 7 {
		t.Errorf("root cube = %d bytes, %d files; want 160, 7", b, n)
	}
	if b, n := sumCube(t, d, 2, cube.Filter{}); b != 157 || n != 4 {
		t.Errorf("a cube = %d, %d; want 157, 4", b, n)
	}
	vf := cube.Filter{Categories: 1 << cube.Video}
	if b, n := sumCube(t, d, 1, vf); b != 150 || n != 3 {
		t.Errorf("root video cube = %d, %d; want 150, 3", b, n)
	}
}

func TestBuildWith_TopErrorsExts(t *testing.T) {
	dirs := []gentest.Dir{
		{Rel: "a", Top: []gentest.TopFile{
			{Kind: "largest", Rank: 1, Name: "big.bin", Size: 9, Alloc: 12, Mtime: 5},
			{Kind: "oldest", Rank: 1, Name: "old.txt", Size: 1, Alloc: 4, Mtime: 1},
		}, Exts: []gen.TopExt{{Ext: "bin", Bytes: 9, Files: 1}},
			Errors: []gen.ErrorRow{{Name: []byte("x"), Op: "lstat", Errno: 13}}},
		{Rel: "a/b"},
	}
	d := open(t, gentest.BuildWith(t, t.TempDir(), 1, 1, dirs, gentest.BuildOpts{TopAnchors: []string{"", "a"}}))
	var n int
	if err := d.QueryRowContext(t.Context(),
		`SELECT count(*) FROM top_files WHERE anchor_id IN (1, 2) AND parent_dir_id = 2`).Scan(&n); err != nil || n != 4 {
		t.Errorf("top_files for anchors 1 and 2 = %d (%v), want 4", n, err)
	}
	if err := d.QueryRowContext(t.Context(), `SELECT count(*) FROM top_files WHERE anchor_id = 3`).Scan(&n); err != nil || n != 0 {
		t.Errorf("top_files for a non-anchor = %d (%v), want 0", n, err)
	}
	var dirID int64
	if err := d.QueryRowContext(t.Context(), `SELECT dir_id FROM top_exts WHERE ext = 'bin'`).Scan(&dirID); err != nil || dirID != 2 {
		t.Errorf("top_exts dir = %d (%v), want 2", dirID, err)
	}
	if err := d.QueryRowContext(t.Context(), `SELECT dir_id FROM errors WHERE errno = 13`).Scan(&dirID); err != nil || dirID != 2 {
		t.Errorf("errors dir = %d (%v), want 2", dirID, err)
	}
}

func TestBuildWith_ExtAnchors(t *testing.T) {
	dirs := []gentest.Dir{
		{Rel: "a", Exts: []gen.TopExt{{Ext: "bin", Bytes: 9, Files: 1}}},
		{Rel: "a/b", Exts: []gen.TopExt{{Ext: "bin", Bytes: 1, Files: 2}, {Ext: "txt", Bytes: 30, Files: 3}}},
		{Rel: "c", Exts: []gen.TopExt{{Ext: "jpg", Bytes: 5, Files: 1}}},
	}
	// Ids in sorted path order: "" 1, a 2, a/b 3, c 4.
	d := open(t, gentest.BuildWith(t, t.TempDir(), 1, 1, dirs, gentest.BuildOpts{ExtAnchors: []string{"", "a"}}))
	rows := func(dir int64) (out []string) {
		rs, err := d.QueryContext(t.Context(), `SELECT ext, bytes, files FROM top_exts WHERE dir_id = ? ORDER BY bytes DESC, ext`, dir)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = rs.Close() }()
		for rs.Next() {
			var e string
			var b, f int64
			if err := rs.Scan(&e, &b, &f); err != nil {
				t.Fatal(err)
			}
			out = append(out, fmt.Sprintf("%s %d %d", e, b, f))
		}
		return out
	}
	for _, tc := range []struct {
		dir  int64
		want []string
	}{
		{1, []string{"txt 30 3", "bin 10 3", "jpg 5 1"}}, // anchor: whole tree
		{2, []string{"txt 30 3", "bin 10 3"}},            // anchor: a and a/b
		{3, []string{"txt 30 3", "bin 1 2"}},             // not an anchor: its own rows
		{4, []string{"jpg 5 1"}},                         // not an anchor: its own rows
	} {
		if got := rows(tc.dir); !slices.Equal(got, tc.want) {
			t.Errorf("dir %d top_exts = %v, want %v", tc.dir, got, tc.want)
		}
	}
}
