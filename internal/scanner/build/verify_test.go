//go:build linux

package build_test

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vinRosso/hogwatch/internal/cube"
	"github.com/vinRosso/hogwatch/internal/gen"
	"github.com/vinRosso/hogwatch/internal/scanner/agg"
	"github.com/vinRosso/hogwatch/internal/scanner/build"
	_ "modernc.org/sqlite" // the plain read-write connection the controls corrupt through
)

func TestVerify_EmptyIsValid(t *testing.T) {
	t.Run("empty root", func(t *testing.T) {
		root := filepath.Join(t.TempDir(), "root")
		mkdirs(t, root)
		res := runOK(t, opts(t.TempDir(), root, 1, testLimits))
		if err := build.Verify(context.Background(), res.Path); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("no qualifying top-N", func(t *testing.T) {
		// The default 1 GiB floor is far above the fixture: no top lists.
		res := runOK(t, opts(t.TempDir(), makeFixture(t), 2, agg.DefaultLimits()))
		if err := build.Verify(context.Background(), res.Path); err != nil {
			t.Fatal(err)
		}
		d, _, err := gen.OpenReadOnly(context.Background(), res.Path)
		must(t, err)
		defer func() { _ = d.Close() }()
		for _, tbl := range []string{"top_files", "top_exts"} {
			if n := countRows(t, d, tbl); n != 0 {
				t.Errorf("%s has %d rows, want 0", tbl, n)
			}
		}
		if countRows(t, d, "shared_inodes") == 0 {
			t.Error("fixture has no shared inodes")
		}
	})
	t.Run("only an unreadable subdir", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("root reads a 000 directory")
		}
		root := filepath.Join(t.TempDir(), "root")
		mkdirs(t, filepath.Join(root, "locked"))
		must(t, os.Chmod(filepath.Join(root, "locked"), 0))
		t.Cleanup(func() { _ = os.Chmod(filepath.Join(root, "locked"), 0o700) }) //nolint:gosec // a directory must be searchable to be removed
		res := runOK(t, opts(t.TempDir(), root, 1, testLimits))
		if err := build.Verify(context.Background(), res.Path); err != nil {
			t.Fatal(err)
		}
	})
}

func TestVerify_MissingFile(t *testing.T) {
	err := build.Verify(context.Background(), filepath.Join(t.TempDir(), "nope.db"))
	if !errors.Is(err, gen.ErrNotExist) || errors.Is(err, build.ErrInvariant) {
		t.Fatalf("Verify of a missing file = %v, want ErrNotExist and not ErrInvariant", err)
	}
}

// control is one corruption that must make Verify fail on invariant inv, with
// want in the message. fix returns the SQL to run (it may look rows up first);
// nil statements mean the control does not apply to this fixture.
type control struct {
	name string
	inv  string
	want string
	do   func(t *testing.T, db *sql.DB)
}

func exec(t *testing.T, db *sql.DB, q string, args ...any) {
	t.Helper()
	res, err := db.Exec(q, args...)
	must(t, err)
	if n, _ := res.RowsAffected(); n == 0 {
		t.Fatalf("corruption changed no row: %s", q)
	}
}

// firstChild is the lowest id among the root's children.
const firstChild = `(SELECT min(id) FROM dirs WHERE parent_id = 1)`

func controls() []control {
	q := func(name, inv, want, stmt string) control {
		return control{name, inv, want, func(t *testing.T, db *sql.DB) { exec(t, db, stmt) }}
	}
	return []control{
		// (a) rollup
		q("files", "a", "files", `UPDATE dirs SET files = files + 1 WHERE id = 1`),
		q("bytes_alloc", "a", "bytes_alloc", `UPDATE dirs SET bytes_alloc = bytes_alloc + 4096 WHERE id = 1`),
		q("own_alloc", "a", "bytes_alloc", `UPDATE dirs SET own_alloc = own_alloc + 512 WHERE id = 1`),
		q("own_files", "a", "files", `UPDATE dirs SET own_files = own_files + 1 WHERE id = 1`),
		q("dirs", "a", "dirs", `UPDATE dirs SET dirs = dirs + 1 WHERE id = 1`),
		q("bytes_logical below children", "a", "bytes_logical", `UPDATE dirs SET bytes_logical = 0 WHERE id = 1`),
		q("error_count below children", "a", "error_count", `UPDATE dirs SET error_count = 99 WHERE id = `+firstChild),

		// (b) cube
		{"cube holds an extra file", "b", "cube holds", func(t *testing.T, db *sql.DB) {
			var blob []byte
			must(t, db.QueryRow(`SELECT blob FROM cube WHERE dir_id = 1`).Scan(&blob))
			acc, err := cube.Decode(blob)
			must(t, err)
			acc.Add(cube.Cell{}, 0, 1)
			out, err := acc.Encode()
			must(t, err)
			exec(t, db, `UPDATE cube SET blob = ? WHERE dir_id = 1`, []byte(out))
		}},
		{"cube holds extra bytes", "b", "cube holds", func(t *testing.T, db *sql.DB) {
			var blob []byte
			must(t, db.QueryRow(`SELECT blob FROM cube WHERE dir_id = 1`).Scan(&blob))
			acc, err := cube.Decode(blob)
			must(t, err)
			acc.Add(cube.Cell{Category: 1}, 512, 0)
			out, err := acc.Encode()
			must(t, err)
			exec(t, db, `UPDATE cube SET blob = ? WHERE dir_id = 1`, []byte(out))
		}},
		q("cube row missing", "b", "no cube row", `DELETE FROM cube WHERE dir_id = `+firstChild),
		q("cube blob corrupt", "b", "corrupt", `UPDATE cube SET blob = x'ff' WHERE dir_id = 1`),
		q("cube row for no dir", "b", "unknown dir", `INSERT INTO cube (dir_id, blob) VALUES (99999, x'01')`),

		// (c) numbering
		q("root pre", "c", "root", `UPDATE dirs SET pre = 100 WHERE parent_id = 0`),
		q("root post", "c", "root", `UPDATE dirs SET post = post + 1 WHERE parent_id = 0`),
		q("post - pre != dirs", "c", "post - pre", `UPDATE dirs SET post = post + 1 WHERE id = `+firstChild),
		q("null pre", "c", "without pre/post", `UPDATE dirs SET pre = NULL WHERE id = `+firstChild),
		q("child outside its parent", "c", "inside its parent", `UPDATE dirs SET pre = 1, post = 1 + dirs WHERE id = `+firstChild),
		{"duplicate pre", "c", "more than one", func(t *testing.T, db *sql.DB) {
			// Two leaf siblings given the same range: every other check holds.
			var a, b int64
			must(t, db.QueryRow(`SELECT a.id, b.id FROM dirs a JOIN dirs b ON a.parent_id = b.parent_id AND a.id < b.id
				WHERE a.dirs = 0 AND b.dirs = 0 LIMIT 1`).Scan(&a, &b))
			exec(t, db, `UPDATE dirs SET pre = (SELECT pre FROM dirs WHERE id = ?1), post = (SELECT pre FROM dirs WHERE id = ?1)
				WHERE id = ?2`, a, b)
		}},

		// (d) shared
		q("inode in one dir only", "d", "fewer than 2", `DELETE FROM shared_links WHERE rowid =
			(SELECT min(rowid) FROM shared_links WHERE name = CAST('f.log' AS BLOB))`),
		q("links without inode", "d", "no shared_inodes row", `DELETE FROM shared_inodes WHERE rowid = (SELECT min(rowid) FROM shared_inodes)`),
		q("inode without links", "d", "has no links", `INSERT INTO shared_inodes (dev, ino, size, alloc, mtime, cube_cell) VALUES (1, 1, 1, 1, 1, 0)`),
		q("cube_cell out of range", "d", "cube_cell", `UPDATE shared_inodes SET cube_cell = 999`),
		q("link in unknown dir", "d", "unknown dir", `INSERT INTO shared_links (dev, ino, dir_id, name, size, alloc, mtime) SELECT dev, ino, 99999, x'0a', 0, 0, 0 FROM shared_inodes LIMIT 1`),

		// (g) hardlink winner
		q("winner is not a link dir", "g", "winner_dir_id", `UPDATE shared_inodes SET winner_dir_id =
			(SELECT id FROM dirs WHERE id NOT IN (SELECT dir_id FROM shared_links s
				WHERE s.dev = shared_inodes.dev AND s.ino = shared_inodes.ino) LIMIT 1)
			WHERE rowid = (SELECT min(rowid) FROM shared_inodes)`),
		q("winner unset", "g", "winner_dir_id", `UPDATE shared_inodes SET winner_dir_id = 0 WHERE rowid = (SELECT min(rowid) FROM shared_inodes)`),

		// (e) top lists
		q("rank gap", "e", "contiguous", `UPDATE top_files SET rank = 9 WHERE anchor_id = 1 AND kind = 'largest' AND rank = 2`),
		q("rank 1 missing", "e", "contiguous", `UPDATE top_files SET rank = 5 WHERE anchor_id = 1 AND kind = 'oldest' AND rank = 1`),
		q("rank above top_n", "e", "exceeds top_n", `INSERT INTO top_files SELECT anchor_id, kind, 4, parent_dir_id, name, size, alloc, mtime, uid
			FROM top_files WHERE anchor_id = 1 AND kind = 'largest' AND rank = 1`),
		q("largest and oldest differ", "e", "oldest rows", `DELETE FROM top_files WHERE anchor_id = 1 AND kind = 'oldest' AND rank = 3`),
		q("top file of unknown dir", "e", "unknown dir", `UPDATE top_files SET parent_dir_id = 99999 WHERE anchor_id = 1 AND kind = 'largest' AND rank = 1`),
		q("extension above its dir", "e", "exceeds its totals",
			`UPDATE top_exts SET bytes = bytes + 999999999 WHERE rowid = (SELECT min(rowid) FROM top_exts)`),

		// (f) negatives, in columns no earlier check reads
		q("negative depth_anchor", "f", "dirs.depth_anchor", `UPDATE dirs SET depth_anchor = -1 WHERE id = 1`),
		q("negative top_exts.bytes", "f", "top_exts.bytes", `UPDATE top_exts SET bytes = -1 WHERE rowid = (SELECT min(rowid) FROM top_exts)`),
		q("negative top_files.alloc", "f", "top_files.alloc", `UPDATE top_files SET alloc = -1 WHERE rowid = (SELECT min(rowid) FROM top_files)`),
		q("negative shared_links.size", "f", "shared_links.size", `UPDATE shared_links SET size = -1 WHERE rowid = (SELECT min(rowid) FROM shared_links)`),
		q("negative shared_links.alloc", "f", "shared_links.alloc", `UPDATE shared_links SET alloc = -1 WHERE rowid = (SELECT min(rowid) FROM shared_links)`),
		q("negative shared_inodes.size", "f", "shared_inodes.size", `UPDATE shared_inodes SET size = -1 WHERE rowid = (SELECT min(rowid) FROM shared_inodes)`),
	}
}

// TestVerify_NegativeControls proves each check can fail: a valid file is
// copied, one value is corrupted through a plain read-write connection, and
// Verify must reject the copy naming the invariant (observation 0009: the
// gate asserts the property, so the gate must be shown able to fail).
func TestVerify_NegativeControls(t *testing.T) {
	root := makeFixture(t)
	res := runOK(t, opts(t.TempDir(), root, 4, testLimits))
	if err := build.Verify(context.Background(), res.Path); err != nil {
		t.Fatalf("the valid base file fails Verify: %v", err)
	}
	valid, err := os.ReadFile(res.Path)
	must(t, err)

	seen := map[string]int{}
	for _, c := range controls() {
		t.Run(c.inv+"/"+c.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "copy.db")
			must(t, os.WriteFile(path, valid, 0o600)) //nolint:gosec // a temp path
			db, err := sql.Open("sqlite", path)
			must(t, err)
			c.do(t, db)
			must(t, db.Close())

			err = build.Verify(context.Background(), path)
			if err == nil {
				t.Fatalf("Verify accepted a file corrupted by %q", c.name)
			}
			if !errors.Is(err, build.ErrInvariant) {
				t.Fatalf("Verify = %v, want an invariant error", err)
			}
			if !strings.Contains(err.Error(), "("+c.inv+")") || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("Verify = %q, want invariant (%s) mentioning %q", err, c.inv, c.want)
			}
		})
		seen[c.inv]++
	}
	for _, inv := range []string{"a", "b", "c", "d", "e", "f", "g"} {
		if seen[inv] == 0 {
			t.Errorf("no negative control for invariant (%s)", inv)
		}
	}
}

// TestVerify_WinnerNegativeControl: a winner_dir_id that is not a directory of
// any link of its inode must fail invariant (g).
func TestVerify_WinnerNegativeControl(t *testing.T) {
	res := runOK(t, opts(t.TempDir(), makeFixture(t), 4, testLimits))
	if err := build.Verify(context.Background(), res.Path); err != nil {
		t.Fatalf("the valid base file fails Verify: %v", err)
	}
	valid, err := os.ReadFile(res.Path)
	must(t, err)
	path := filepath.Join(t.TempDir(), "copy.db")
	must(t, os.WriteFile(path, valid, 0o600)) //nolint:gosec // a temp path
	db, err := sql.Open("sqlite", path)
	must(t, err)
	exec(t, db, `UPDATE shared_inodes SET winner_dir_id = 99999`)
	must(t, db.Close())
	err = build.Verify(context.Background(), path)
	if !errors.Is(err, build.ErrInvariant) || !strings.Contains(err.Error(), "(g)") {
		t.Fatalf("Verify = %v, want invariant (g)", err)
	}
}
