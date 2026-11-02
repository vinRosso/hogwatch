package gen_test

import (
	"bytes"
	"fmt"
	"maps"
	"math"
	"slices"
	"testing"

	"github.com/vinRosso/hogwatch/internal/cube"
	"github.com/vinRosso/hogwatch/internal/gen"
)

// dirD is dir with an explicit descendant-directory count.
func dirD(id, parent int64, name string, dirs int64) gen.Dir {
	d := dir(id, parent, name)
	d.Dirs = dirs
	return d
}

func mustDo(t *testing.T, errs ...error) {
	t.Helper()
	for _, err := range errs {
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	}
}

func TestNumberPrePost(t *testing.T) {
	ctx := t.Context()
	w := mustCreate(t, newPath(t))
	// ids are deliberately not in name order. Name-sorted DFS:
	//   r(1) > a(3) > m(6) > q(7) ; b(2) > y(5), z(4)
	for _, d := range []gen.Dir{
		dirD(1, 0, "r", 6), dirD(2, 1, "b", 2), dirD(3, 1, "a", 2),
		dirD(4, 2, "z", 0), dirD(5, 2, "y", 0), dirD(6, 3, "m", 1), dirD(7, 6, "q", 0),
	} {
		mustDo(t, w.AddDir(ctx, d))
	}
	mustDo(t, w.BuildIndexes(ctx), w.NumberPrePost(ctx))

	want := map[int64][2]int64{1: {1, 7}, 3: {2, 4}, 6: {3, 4}, 7: {4, 4}, 2: {5, 7}, 5: {6, 6}, 4: {7, 7}}
	rows, err := gen.WriterDB(w).QueryContext(ctx, `SELECT id, pre, post FROM dirs`)
	mustDo(t, err)
	defer func() { _ = rows.Close() }()
	n := 0
	for rows.Next() {
		var id, pre, post int64
		mustDo(t, rows.Scan(&id, &pre, &post))
		if got := [2]int64{pre, post}; got != want[id] {
			t.Errorf("dir %d: pre/post = %v, want %v", id, got, want[id])
		}
		n++
	}
	mustDo(t, rows.Err())
	if n != len(want) {
		t.Fatalf("rows = %d, want %d", n, len(want))
	}
	_ = rows.Close()

	var bad int64
	mustDo(t, gen.WriterDB(w).QueryRowContext(ctx, `SELECT count(*) FROM dirs c JOIN dirs p ON p.id = c.parent_id
		WHERE NOT (c.pre > p.pre AND c.post <= p.post)`).Scan(&bad))
	if bad != 0 {
		t.Errorf("%d children do not nest in their parent's range", bad)
	}
}

func TestNumberPrePost_RejectsSparseIDs(t *testing.T) {
	ctx := t.Context()
	w := mustCreate(t, newPath(t))
	for _, d := range []gen.Dir{dirD(1, 0, "r", 2), dirD(2, 1, "a", 0), dirD(4, 1, "b", 0)} {
		mustDo(t, w.AddDir(ctx, d))
	}
	mustDo(t, w.BuildIndexes(ctx))
	if err := w.NumberPrePost(ctx); err == nil {
		t.Fatal("NumberPrePost accepted ids {1,2,4}")
	}
}

func TestNumberPrePost_RejectsBadParents(t *testing.T) {
	cases := map[string][]gen.Dir{
		"parent after child": {dirD(1, 0, "r", 2), dirD(2, 3, "a", 0), dirD(3, 1, "b", 0)},
		"second root":        {dirD(1, 0, "r", 1), dirD(2, 0, "a", 0)},
		"root has a parent":  {dirD(1, 1, "r", 0)},
	}
	for name, dirs := range cases {
		t.Run(name, func(t *testing.T) {
			ctx := t.Context()
			w := mustCreate(t, newPath(t))
			for _, d := range dirs {
				mustDo(t, w.AddDir(ctx, d))
			}
			mustDo(t, w.BuildIndexes(ctx))
			if err := w.NumberPrePost(ctx); err == nil {
				t.Fatal("NumberPrePost accepted the tree")
			}
		})
	}
}

func TestNumberPrePost_RejectsWrongDirsCount(t *testing.T) {
	for name, bad := range map[string]int64{"too small": 0, "too large": 2} {
		t.Run(name, func(t *testing.T) {
			ctx := t.Context()
			w := mustCreate(t, newPath(t))
			// r > a > c, r > b; the true count for a is 1.
			for _, d := range []gen.Dir{dirD(1, 0, "r", 3), dirD(2, 1, "a", bad), dirD(3, 2, "c", 0), dirD(4, 1, "b", 0)} {
				mustDo(t, w.AddDir(ctx, d))
			}
			mustDo(t, w.BuildIndexes(ctx))
			if err := w.NumberPrePost(ctx); err == nil {
				t.Fatal("NumberPrePost accepted a wrong dirs count")
			}
			var n int64
			mustDo(t, gen.WriterDB(w).QueryRowContext(ctx, `SELECT count(*) FROM dirs WHERE pre IS NOT NULL OR post IS NOT NULL`).Scan(&n))
			if n != 0 {
				t.Errorf("%d dirs numbered despite the error", n)
			}
			if err := w.Flush(ctx); err == nil {
				t.Error("Writer still usable after the error")
			}
		})
	}
}

func TestNumberPrePost_CrossesBatches(t *testing.T) {
	ctx := t.Context()
	w := mustCreate(t, newPath(t))
	const n = 25_000 // a root with n-1 leaf children: three UPDATE batches
	mustDo(t, w.AddDir(ctx, dirD(1, 0, "r", n-1)))
	for i := int64(2); i <= n; i++ {
		mustDo(t, w.AddDir(ctx, dirD(i, 1, fmt.Sprintf("d%06d", i), 0)))
	}
	mustDo(t, w.BuildIndexes(ctx), w.NumberPrePost(ctx))
	var maxPost, nulls int64
	mustDo(t, gen.WriterDB(w).QueryRowContext(ctx, `SELECT max(post), sum(pre IS NULL) FROM dirs`).Scan(&maxPost, &nulls))
	if maxPost != n || nulls != 0 {
		t.Errorf("max post = %d, null pre = %d; want %d, 0", maxPost, nulls, n)
	}
}

func TestLinkGroupsAfter_Paging(t *testing.T) {
	ctx := t.Context()
	w := mustCreate(t, newPath(t))
	inodes := []gen.SharedInode{
		{Dev: 2, Ino: 1, Size: 10}, {Dev: 1, Ino: 9, Size: 20}, {Dev: 1, Ino: 3, Size: 30},
		{Dev: 3, Ino: 0, Size: 40}, {Dev: 2, Ino: 7, Size: 50},
	}
	for _, s := range inodes {
		mustDo(t, w.AddSharedInode(ctx, s))
		// links added out of order to prove the ORDER BY
		l := func(dir int64, name string) gen.SharedLink {
			return gen.SharedLink{Dev: s.Dev, Ino: s.Ino, DirID: dir, Name: []byte(name), Size: s.Size + dir, Alloc: 4096, Mtime: 7}
		}
		mustDo(t, w.AddSharedLink(ctx, l(5, "b")), w.AddSharedLink(ctx, l(5, "a")), w.AddSharedLink(ctx, l(2, "z")))
	}
	mustDo(t, w.BuildIndexes(ctx))

	var pages [][]gen.LinkGroup
	dev, ino := int64(math.MinInt64), int64(math.MinInt64)
	for {
		g, err := w.LinkGroupsAfter(ctx, dev, ino, 2)
		mustDo(t, err)
		if len(g) == 0 {
			break
		}
		pages = append(pages, g)
		dev, ino = g[len(g)-1].Dev, g[len(g)-1].Ino
		if len(g) < 2 {
			break
		}
	}
	if len(pages) != 3 || len(pages[0]) != 2 || len(pages[1]) != 2 || len(pages[2]) != 1 {
		t.Fatalf("pages = %d, want 3 of sizes 2,2,1", len(pages))
	}
	var got [][2]int64
	for _, p := range pages {
		for _, g := range p {
			got = append(got, [2]int64{g.Dev, g.Ino})
			if len(g.Links) != 3 {
				t.Fatalf("group (%d,%d): %d links, want 3", g.Dev, g.Ino, len(g.Links))
			}
			if g.Links[0].DirID != 2 || !bytes.Equal(g.Links[0].Name, []byte("z")) ||
				!bytes.Equal(g.Links[1].Name, []byte("a")) || !bytes.Equal(g.Links[2].Name, []byte("b")) {
				t.Errorf("group (%d,%d): links out of order: %+v", g.Dev, g.Ino, g.Links)
			}
			for _, l := range g.Links {
				if l.Size < 10 || l.Alloc != 4096 || l.Mtime != 7 {
					t.Errorf("group (%d,%d): link values not loaded: %+v", g.Dev, g.Ino, l)
				}
			}
			if g.Links[0].Size%10 != 2 || g.Links[1].Size%10 != 5 { // each link keeps its own size (base + dir)
				t.Errorf("group (%d,%d): link sizes %d, %d", g.Dev, g.Ino, g.Links[0].Size, g.Links[1].Size)
			}
		}
	}
	want := [][2]int64{{1, 3}, {1, 9}, {2, 1}, {2, 7}, {3, 0}}
	if !slices.Equal(got, want) {
		t.Errorf("order = %v, want %v", got, want)
	}
}

func TestDirInfo(t *testing.T) {
	ctx := t.Context()
	w := mustCreate(t, newPath(t))
	mustDo(t, w.AddDir(ctx, dirD(1, 0, "r", 1)), w.AddDir(ctx, dirD(2, 1, "\xffx", 0)))
	parent, name, err := w.DirInfo(ctx, 2) // flushes first
	mustDo(t, err)
	if parent != 1 || !bytes.Equal(name, []byte("\xffx")) {
		t.Errorf("DirInfo(2) = %d, %q", parent, name)
	}
	if _, _, err := w.DirInfo(ctx, 99); err == nil {
		t.Error("DirInfo(99) succeeded")
	}
}

func cubeBlob(t *testing.T, cells map[cube.Cell][2]int64) cube.Blob {
	t.Helper()
	var a cube.Acc
	for c, v := range cells {
		a.Add(c, v[0], v[1])
	}
	b, err := a.Encode()
	mustDo(t, err)
	return b
}

// deltaFixture builds dir 1 with known columns, a cube and two top_exts rows.
func deltaFixture(t *testing.T) (*gen.Writer, cube.Cell) {
	t.Helper()
	ctx := t.Context()
	w := mustCreate(t, newPath(t))
	cell := cube.Cell{Category: uint8(cube.Code), Age: 1, Size: 2}
	d := dirD(1, 0, "r", 0)
	d.OwnFiles, d.OwnAlloc, d.Files, d.BytesLogical, d.BytesAlloc = 5, 100, 10, 1000, 2000
	mustDo(t, w.AddDir(ctx, d),
		w.AddCube(ctx, 1, cubeBlob(t, map[cube.Cell][2]int64{cell: {500, 3}})),
		w.AddTopExt(ctx, gen.TopExt{DirID: 1, Ext: "go", Bytes: 300, Files: 2}),
		w.AddTopExt(ctx, gen.TopExt{DirID: 1, Ext: "txt", Bytes: 7, Files: 1}))
	return w, cell
}

func TestApplyDeltas(t *testing.T) {
	t.Run("subtracts", func(t *testing.T) {
		ctx := t.Context()
		w, cell := deltaFixture(t)
		mustDo(t, w.ApplyDeltas(ctx, []gen.DirDelta{{
			DirID: 1, OwnFiles: -2, OwnAlloc: -30, Files: -3, BytesLogical: -100, BytesAlloc: -200,
			Cells: []gen.CellDelta{{Cell: cell, Bytes: -200, Files: -1}, {Cell: cube.Cell{Category: uint8(cube.Audio)}, Bytes: 9, Files: 1}},
			Exts:  []gen.ExtDelta{{Ext: "go", Bytes: -100, Files: -1}, {Ext: "absent", Bytes: -5, Files: -1}},
		}}))
		var of, oa, f, bl, ba int64
		mustDo(t, gen.WriterDB(w).QueryRowContext(ctx,
			`SELECT own_files, own_alloc, files, bytes_logical, bytes_alloc FROM dirs WHERE id = 1`).Scan(&of, &oa, &f, &bl, &ba))
		if of != 3 || oa != 70 || f != 7 || bl != 900 || ba != 1800 {
			t.Errorf("columns = %d %d %d %d %d", of, oa, f, bl, ba)
		}
		var blob []byte
		mustDo(t, gen.WriterDB(w).QueryRowContext(ctx, `SELECT blob FROM cube WHERE dir_id = 1`).Scan(&blob))
		a, err := cube.Decode(blob)
		mustDo(t, err)
		if c := a[cell.Index()]; c.Bytes != 300 || c.Files != 2 {
			t.Errorf("cell = %+v, want {300 2}", c)
		}
		if c := a[cube.Cell{Category: uint8(cube.Audio)}.Index()]; c.Bytes != 9 || c.Files != 1 {
			t.Errorf("new cell = %+v, want {9 1}", c)
		}
		var gb, gf, n int64
		mustDo(t, gen.WriterDB(w).QueryRowContext(ctx, `SELECT bytes, files FROM top_exts WHERE ext = 'go'`).Scan(&gb, &gf))
		mustDo(t, gen.WriterDB(w).QueryRowContext(ctx, `SELECT count(*) FROM top_exts`).Scan(&n))
		if gb != 200 || gf != 1 || n != 2 {
			t.Errorf("top_exts go = %d/%d, rows = %d; want 200/1, 2", gb, gf, n)
		}
	})
	t.Run("deletes a fully subtracted ext and keeps a partial one", func(t *testing.T) {
		ctx := t.Context()
		w, _ := deltaFixture(t)
		mustDo(t, w.ApplyDeltas(ctx, []gen.DirDelta{{DirID: 1,
			Exts: []gen.ExtDelta{{Ext: "txt", Bytes: -7, Files: -1}, {Ext: "go", Bytes: -100, Files: -1}}}}))
		rows := map[string][2]int64{}
		rs, err := gen.WriterDB(w).QueryContext(ctx, `SELECT ext, bytes, files FROM top_exts`)
		mustDo(t, err)
		defer func() { _ = rs.Close() }()
		for rs.Next() {
			var e string
			var b, f int64
			mustDo(t, rs.Scan(&e, &b, &f))
			rows[e] = [2]int64{b, f}
		}
		mustDo(t, rs.Err())
		if want := map[string][2]int64{"go": {200, 1}}; !maps.Equal(rows, want) {
			t.Errorf("top_exts = %v, want %v", rows, want)
		}
	})
	for name, delta := range map[string]gen.DirDelta{
		"negative column": {DirID: 1, Files: -11},
		"negative cell":   {DirID: 1, Cells: []gen.CellDelta{{Cell: cube.Cell{Category: uint8(cube.Code), Age: 1, Size: 2}, Bytes: -501}}},
		"negative ext":    {DirID: 1, Exts: []gen.ExtDelta{{Ext: "go", Files: -3}}},
		"unknown dir":     {DirID: 9, Files: 1},
	} {
		t.Run(name, func(t *testing.T) {
			ctx := t.Context()
			w, _ := deltaFixture(t)
			if err := w.ApplyDeltas(ctx, []gen.DirDelta{{DirID: 1, OwnFiles: 1}, delta}); err == nil {
				t.Fatal("ApplyDeltas succeeded")
			}
			if err := w.AddDir(ctx, dirD(2, 1, "x", 0)); err == nil {
				t.Error("Writer still usable after a failed ApplyDeltas")
			}
			if err := w.Flush(ctx); err == nil {
				t.Error("Flush succeeded on a failed Writer")
			}
		})
	}
}

func TestApplyDeltas_RollsBack(t *testing.T) {
	ctx := t.Context()
	w, _ := deltaFixture(t)
	if err := w.ApplyDeltas(ctx, []gen.DirDelta{{DirID: 1, OwnFiles: 1}, {DirID: 1, Files: -11}}); err == nil {
		t.Fatal("ApplyDeltas succeeded")
	}
	var of int64
	mustDo(t, gen.WriterDB(w).QueryRowContext(ctx, `SELECT own_files FROM dirs WHERE id = 1`).Scan(&of))
	if of != 5 {
		t.Errorf("own_files = %d after a failed call, want 5 (rolled back)", of)
	}
}

func TestSetSharedCells(t *testing.T) {
	ctx := t.Context()
	w := mustCreate(t, newPath(t))
	mustDo(t, w.AddSharedInode(ctx, gen.SharedInode{Dev: 1, Ino: 1, CubeCell: 5}),
		w.AddSharedInode(ctx, gen.SharedInode{Dev: 1, Ino: 2, CubeCell: 5}))
	mustDo(t, w.SetSharedCells(ctx, []gen.SharedInode{{Dev: 1, Ino: 2, Size: 8, Alloc: 4096, Mtime: 9, CubeCell: 77}}))
	var sz, al, mt int64
	mustDo(t, gen.WriterDB(w).QueryRowContext(ctx, `SELECT size, alloc, mtime FROM shared_inodes WHERE ino = 2`).Scan(&sz, &al, &mt))
	if sz != 8 || al != 4096 || mt != 9 {
		t.Errorf("size, alloc, mtime = %d, %d, %d; want 8, 4096, 9", sz, al, mt)
	}
	rows, err := gen.WriterDB(w).QueryContext(ctx, `SELECT ino, cube_cell FROM shared_inodes ORDER BY ino`)
	mustDo(t, err)
	defer func() { _ = rows.Close() }()
	var got [][2]int64
	for rows.Next() {
		var i, c int64
		mustDo(t, rows.Scan(&i, &c))
		got = append(got, [2]int64{i, c})
	}
	if !slices.Equal(got, [][2]int64{{1, 5}, {2, 77}}) {
		t.Errorf("cells = %v", got)
	}
	_ = rows.Close()
	if err := w.SetSharedCells(ctx, []gen.SharedInode{{Dev: 9, Ino: 9}}); err == nil {
		t.Error("SetSharedCells accepted an unknown inode")
	}
}

func TestPruneUnshared(t *testing.T) {
	ctx := t.Context()
	w := mustCreate(t, newPath(t))
	link := func(ino, dir int64, name string) gen.SharedLink {
		return gen.SharedLink{Dev: 1, Ino: ino, DirID: dir, Name: []byte(name)}
	}
	for _, ino := range []int64{1, 2, 3} {
		mustDo(t, w.AddSharedInode(ctx, gen.SharedInode{Dev: 1, Ino: ino}))
	}
	mustDo(t,
		w.AddSharedLink(ctx, link(1, 2, "a")), w.AddSharedLink(ctx, link(1, 2, "b")), // same dir
		w.AddSharedLink(ctx, link(2, 2, "c")),                                        // singleton
		w.AddSharedLink(ctx, link(3, 2, "d")), w.AddSharedLink(ctx, link(3, 3, "e"))) // two dirs
	mustDo(t, w.BuildIndexes(ctx))
	n, err := w.PruneUnshared(ctx)
	mustDo(t, err)
	if n != 2 {
		t.Errorf("pruned = %d, want 2", n)
	}
	var inodes, links, kept int64
	db := gen.WriterDB(w)
	mustDo(t, db.QueryRowContext(ctx, `SELECT count(*) FROM shared_inodes`).Scan(&inodes))
	mustDo(t, db.QueryRowContext(ctx, `SELECT count(*) FROM shared_links`).Scan(&links))
	mustDo(t, db.QueryRowContext(ctx, `SELECT count(*) FROM shared_links WHERE ino = 3`).Scan(&kept))
	if inodes != 1 || links != 2 || kept != 2 {
		t.Errorf("inodes = %d, links = %d (ino 3: %d); want 1, 2, 2", inodes, links, kept)
	}
}

func TestAddSharedInode_Duplicate(t *testing.T) {
	ctx := t.Context()
	w := mustCreate(t, newPath(t))
	mustDo(t, w.AddSharedInode(ctx, gen.SharedInode{Dev: 1, Ino: 2, Size: 111}))
	mustDo(t, w.Flush(ctx)) // across batches
	mustDo(t, w.AddSharedInode(ctx, gen.SharedInode{Dev: 1, Ino: 2, Size: 222}))
	mustDo(t, w.AddSharedInode(ctx, gen.SharedInode{Dev: 1, Ino: 2, Size: 333})) // same batch
	mustDo(t, w.Flush(ctx))
	var n, size int64
	mustDo(t, gen.WriterDB(w).QueryRowContext(ctx, `SELECT count(*), max(size) FROM shared_inodes`).Scan(&n, &size))
	if n != 1 || size != 111 {
		t.Errorf("rows = %d, size = %d; want 1, 111", n, size)
	}
}
