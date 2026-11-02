//go:build e2e

// Package e2e seeds a data directory for the Playwright smoke test and for
// manual testing against the dev server:
//
//	go test -tags e2e ./internal/e2e -run TestSeed -count=1 -args -out "$DIR"
//
// This file is also the project's record of the e2e accounts. The passwords
// below are test values for a throwaway directory and are never used elsewhere.
package e2e

import (
	"context"
	"flag"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/vinRosso/hogwatch/internal/auth/local"
	"github.com/vinRosso/hogwatch/internal/cube"
	"github.com/vinRosso/hogwatch/internal/db"
	"github.com/vinRosso/hogwatch/internal/gen"
	"github.com/vinRosso/hogwatch/internal/gen/gentest"
	"github.com/vinRosso/hogwatch/internal/scanner/agg"
	"github.com/vinRosso/hogwatch/internal/totals"
)

var out = flag.String("out", "", "data directory to seed")

const (
	alicePassword = "e2e-password-1"
	adminPassword = "e2e-password-2"
	otherPassword = "e2e-password-3" // bob, carol, dave and ops
	day           = int64(24 * 60 * 60)
)

// The names are the Review Focus 1 inputs: a newline, a right-to-left override,
// markup, a non-UTF-8 byte and a spreadsheet formula.
var hostile = []string{"a\nb", "x‮y", "<img src=x onerror=alert(1)>", "\xff", "=cmd"}

// The users. Ids are assigned in this order; paths name the root (root id,
// rel path) each user can browse. Scan id equals root id in this seed.
type seedUser struct {
	id       int64
	name     string
	display  string
	password string
	joined   int64 // days ago
	admin    bool
	canList  bool
	disabled bool
	quota    any // nil or bytes
	paths    []seedPath
}

type seedPath struct {
	root int64
	rel  string
}

var users = []seedUser{
	{id: 1, name: "alice", display: "Alice", password: alicePassword, joined: 100, canList: true, quota: int64(20_000_000_000),
		paths: []seedPath{{1, "alice"}}},
	{id: 2, name: "admin", display: "Admin", password: adminPassword, joined: 100, admin: true,
		paths: nil},
	{id: 3, name: "bob", display: "Bob", password: otherPassword, joined: 80, quota: int64(10_000_000_000),
		paths: []seedPath{{1, "bob"}}},
	// Carol is over quota: root 1 plus 8 GB on root 2.
	{id: 4, name: "carol", display: "Carol", password: otherPassword, joined: 60, canList: true, quota: int64(10_000_000_000),
		paths: []seedPath{{1, "carol"}, {2, "weekly"}}},
	{id: 5, name: "dave", display: "Dave", password: otherPassword, joined: 30, disabled: true,
		paths: []seedPath{{1, "dave"}}},
	{id: 6, name: "ops", display: "Ops", password: otherPassword, joined: 10, admin: true,
		paths: nil},
}

// root1 is the storage root (scan 1); root2 is the backup root (scan 2).
// Directories not owned by a user (shared/) count toward the mount only.
func rootDirs() (root1, root2 []gentest.Dir) {
	root1 = []gentest.Dir{
		{Rel: "alice/media/movies", Files: 3, BytesLogical: 6_000_000_000, BytesAlloc: 6_000_000_000,
			Cells: cells(share{cube.Video, 6_000_000_000, 3}),
			Exts:  []gen.TopExt{{Ext: "mkv", Bytes: 6_000_000_000, Files: 3}}},
		{Rel: "alice/media/music", Files: 40, BytesLogical: 1_500_000_000, BytesAlloc: 1_500_000_000,
			Cells: cells(share{cube.Audio, 1_450_000_000, 36}, share{cube.Image, 50_000_000, 4}),
			Exts:  []gen.TopExt{{Ext: "flac", Bytes: 1_450_000_000, Files: 36}, {Ext: "jpg", Bytes: 50_000_000, Files: 4}}},
		{Rel: "alice/docs", Files: 12, BytesLogical: 200_000_000, BytesAlloc: 200_000_000,
			Cells: cells(share{cube.Document, 200_000_000, 12}),
			Exts:  []gen.TopExt{{Ext: "pdf", Bytes: 200_000_000, Files: 12}}},
		{Rel: "alice/photos/2023", Files: 120, BytesLogical: 900_000_000, BytesAlloc: 900_000_000,
			Cells: cells(share{cube.Image, 900_000_000, 120}),
			Exts:  []gen.TopExt{{Ext: "jpg", Bytes: 700_000_000, Files: 100}, {Ext: "png", Bytes: 200_000_000, Files: 20}}},
		{Rel: "alice/photos/2024", Files: 80, BytesLogical: 600_000_000, BytesAlloc: 600_000_000,
			Cells:  cells(share{cube.Image, 350_000_000, 60}, share{cube.Video, 230_000_000, 15}, share{cube.Other, 20_000_000, 5}),
			Exts:   []gen.TopExt{{Ext: "jpg", Bytes: 350_000_000, Files: 60}, {Ext: "mp4", Bytes: 230_000_000, Files: 15}, {Ext: "", Bytes: 20_000_000, Files: 5}},
			Errors: []gen.ErrorRow{{Name: []byte("locked"), Op: "readdir", Errno: 13, Message: "permission denied"}}},
		{Rel: "bob/archive/old", Files: 200, BytesLogical: 2_500_000_000, BytesAlloc: 2_500_000_000,
			Exts: []gen.TopExt{{Ext: "zip", Bytes: 2_000_000_000, Files: 30}, {Ext: "tar", Bytes: 500_000_000, Files: 170}}},
		{Rel: "bob/projects/app/src", Files: 30, BytesLogical: 300_000_000, BytesAlloc: 300_000_000},
		{Rel: "bob/projects/app/build", Files: 500, BytesLogical: 1_200_000_000, BytesAlloc: 1_200_000_000},
		{Rel: "bob/notes", Files: 5, BytesLogical: 10_000_000, BytesAlloc: 10_000_000},
		{Rel: "carol/vm", Files: 2, BytesLogical: 4_000_000_000, BytesAlloc: 4_000_000_000,
			Exts: []gen.TopExt{{Ext: "qcow2", Bytes: 4_000_000_000, Files: 2}}},
		{Rel: "carol/isos", Files: 3, BytesLogical: 2_500_000_000, BytesAlloc: 2_500_000_000,
			Exts: []gen.TopExt{{Ext: "iso", Bytes: 2_500_000_000, Files: 3}}},
		{Rel: "dave/old", Files: 10, BytesLogical: 100_000_000, BytesAlloc: 100_000_000},
		{Rel: "shared/team/handbook", Files: 60, BytesLogical: 400_000_000, BytesAlloc: 400_000_000},
		{Rel: "shared/team/builds", Files: 300, BytesLogical: 2_100_000_000, BytesAlloc: 2_100_000_000,
			Errors: []gen.ErrorRow{{Name: []byte("tmp"), Op: "lstat", Errno: 2, Message: "no such file"}}},
	}
	for _, n := range hostile {
		root1 = append(root1, gentest.Dir{Rel: "alice/" + n, Files: 1, BytesLogical: 1_000_000, BytesAlloc: 1_000_000})
	}
	root2 = []gentest.Dir{
		{Rel: "daily/2026-10-26", Files: 480, BytesLogical: 2_900_000_000, BytesAlloc: 2_900_000_000},
		{Rel: "daily/2026-10-27", Files: 500, BytesLogical: 3_000_000_000, BytesAlloc: 3_000_000_000},
		{Rel: "weekly/2026-42", Files: 40, BytesLogical: 8_000_000_000, BytesAlloc: 8_000_000_000},
	}
	return root1, root2
}

// share is one category's part of a directory's own files.
type share struct {
	cat          cube.Category
	bytes, files int64
}

// cells puts each share in one cube cell (under 3 months old, 10 to 100 MB).
func cells(shares ...share) map[cube.Cell][2]int64 {
	m := make(map[cube.Cell][2]int64, len(shares))
	for _, s := range shares {
		m[cube.Cell{Category: uint8(s.cat), Age: 2, Size: 2}] = [2]int64{s.bytes, s.files}
	}
	return m
}

// tally sums the files and allocation under rel ("" is the whole root).
// qualifying lists the directories that a scan with agg.DefaultLimits gives
// top lists and extension totals: at most 5 levels below the nearest anchor
// (the root or an assigned user path) and at least 1 GiB in total.
func qualifying(dirs []gentest.Dir, anchors []string) []string {
	rels := map[string]bool{"": true}
	for _, d := range dirs {
		for p := d.Rel; p != ""; p = parentRel(p) {
			rels[p] = true
		}
	}
	lim := agg.DefaultLimits()
	var out []string
	for r := range rels {
		depth := segments(r)
		for _, a := range anchors {
			if r == a || strings.HasPrefix(r, a+"/") {
				depth = min(depth, segments(r)-segments(a))
			}
		}
		if _, alloc := tally(dirs, r); lim.Qualifies(depth, alloc) {
			out = append(out, r)
		}
	}
	slices.Sort(out)
	return out
}

// userPaths returns the paths assigned to users on root.
func userPaths(root int64) (out []string) {
	for _, u := range users {
		for _, p := range u.paths {
			if p.root == root {
				out = append(out, p.rel)
			}
		}
	}
	return out
}

func segments(rel string) int64 {
	if rel == "" {
		return 0
	}
	return int64(strings.Count(rel, "/") + 1)
}

func parentRel(rel string) string {
	if i := strings.LastIndexByte(rel, '/'); i >= 0 {
		return rel[:i]
	}
	return ""
}

func tally(dirs []gentest.Dir, rel string) (files, alloc int64) {
	for _, d := range dirs {
		if rel == "" || d.Rel == rel || strings.HasPrefix(d.Rel, rel+"/") {
			files += d.Files
			alloc += d.BytesAlloc
		}
	}
	return files, alloc
}

func boolInt(b bool) int64 {
	if b {
		return 1
	}
	return 0
}

func TestSeed(t *testing.T) {
	if *out == "" {
		t.Skip("pass -args -out DIR")
	}
	ctx := context.Background()
	d, err := db.Open(ctx, filepath.Join(*out, "hogwatch.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = d.Close() }()
	if err := db.Migrate(ctx, d); err != nil {
		t.Fatal(err)
	}
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := d.ExecContext(ctx, q, args...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}

	root1, root2 := rootDirs()
	dirs := map[int64][]gentest.Dir{1: root1, 2: root2}
	files := map[int64]string{}
	for root := int64(1); root <= 2; root++ {
		// Top-N lists and extension totals go where a real scan puts them.
		q := qualifying(dirs[root], userPaths(root))
		opts := gentest.BuildOpts{TopAnchors: q, ExtAnchors: q}
		files[root] = gentest.BuildWith(t, *out, root, root, dirs[root], opts)
	}

	now := time.Now().Unix()
	exec(`INSERT INTO scan_roots (id, name, container_path, host_path) VALUES (1, 'storage', '/mnt/storage', '/srv/storage')`)
	exec(`INSERT INTO scan_roots (id, name, container_path, host_path) VALUES (2, 'backup', '/mnt/backup', '/srv/backup')`)
	for root := int64(1); root <= 2; root++ {
		f, alloc := tally(dirs[root], "")
		logical := alloc
		exec(`INSERT INTO scans (id, root_id, file, status, trigger, started_at, finished_at, dirs, files, bytes_logical, bytes_alloc, error_count)
		      VALUES (?, ?, ?, 'done', ?, ?, ?, ?, ?, ?, ?, ?)`,
			root, root, filepath.Base(files[root]), map[int64]string{1: "manual", 2: "schedule"}[root],
			now-60, now-30, len(dirs[root]), f, logical, alloc, errorCount(dirs[root]))
		exec(`UPDATE scan_roots SET current_scan_id = ? WHERE id = ?`, root, root)
		exec(`INSERT INTO mount_stats (scan_id, root_id, fs_total, fs_free) VALUES (?, ?, ?, ?)`,
			root, root, map[int64]int64{1: 1_000_000_000_000, 2: 2_000_000_000_000}[root], map[int64]int64{1: 500_000_000_000, 2: 1_200_000_000_000}[root])
	}
	exec(`INSERT INTO scanner_status (id, heartbeat_at, version) VALUES (1, ?, 'e2e')`, now)

	aliceHash, err := local.Hash(alicePassword)
	if err != nil {
		t.Fatal(err)
	}
	adminHash, err := local.Hash(adminPassword)
	if err != nil {
		t.Fatal(err)
	}
	otherHash, err := local.Hash(otherPassword)
	if err != nil {
		t.Fatal(err)
	}
	hashes := map[string]string{alicePassword: aliceHash, adminPassword: adminHash, otherPassword: otherHash}
	for _, u := range users {
		exec(`INSERT INTO users (id, username, display_name, password_hash, can_list_files, quota_bytes, admin_local, disabled, created_at)
		      VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			u.id, u.name, u.display, hashes[u.password], boolInt(u.canList), u.quota, boolInt(u.admin), boolInt(u.disabled), now-u.joined*day)
	}

	// Per user path: a user_paths row, its usage on that root and its path stats.
	pathID := int64(0)
	for _, u := range users {
		for _, p := range u.paths {
			pathID++
			exec(`INSERT INTO user_paths (id, user_id, root_id, rel_path, created_at) VALUES (?, ?, ?, CAST(? AS BLOB), ?)`,
				pathID, u.id, p.root, p.rel, now-u.joined*day)
			f, alloc := tally(dirs[p.root], p.rel)
			exec(`INSERT INTO usage (scan_id, user_id, root_id, bytes_logical, bytes_alloc, files) VALUES (?, ?, ?, ?, ?, ?)`,
				p.root, u.id, p.root, alloc, alloc, f)
			exec(`INSERT INTO path_stats (scan_id, user_path_id, found, redundant, bytes_logical, bytes_alloc, files, shared_hardlink_bytes)
			      VALUES (?, ?, 1, 0, ?, ?, ?, 0)`, p.root, pathID, alloc, alloc, f)
		}
	}

	// Older points per scope and per directory, so the 7 d and 30 d growth
	// figures and the history chart all have a base. Older points hold a
	// smaller share of the current totals.
	for _, ago := range historyAgos {
		ts := now - ago*day
		for root := int64(1); root <= 2; root++ {
			f, alloc := tally(dirs[root], "")
			exec(`INSERT INTO history_totals (ts, scope, user_id, root_id, bytes_logical, bytes_alloc, files, tier) VALUES (?, 'mount', NULL, ?, ?, ?, ?, 'raw')`,
				ts, root, shrink(alloc, ago), shrink(alloc, ago), shrink(f, ago))
		}
		for _, u := range users {
			var uf, ua int64
			for _, p := range u.paths {
				f, alloc := tally(dirs[p.root], p.rel)
				uf += f
				ua += alloc
				exec(`INSERT INTO history_totals (ts, scope, user_id, root_id, bytes_logical, bytes_alloc, files, tier) VALUES (?, 'user_mount', ?, ?, ?, ?, ?, 'raw')`,
					ts, u.id, p.root, shrink(alloc, ago), shrink(alloc, ago), shrink(f, ago))
			}
			if len(u.paths) > 0 {
				exec(`INSERT INTO history_totals (ts, scope, user_id, root_id, bytes_logical, bytes_alloc, files, tier) VALUES (?, 'user', ?, NULL, ?, ?, ?, 'raw')`,
					ts, u.id, shrink(ua, ago), shrink(ua, ago), shrink(uf, ago))
			}
		}
	}

	// Every directory of every root, ancestors included, gets its own history.
	for root := int64(1); root <= 2; root++ {
		for _, rel := range relsWithAncestors(dirs[root]) {
			f, alloc := tally(dirs[root], rel)
			hash := totals.PathHash(root, []byte(rel))
			exec(`INSERT OR IGNORE INTO history_paths (path_hash, root_id, rel_path) VALUES (?, ?, CAST(? AS BLOB))`, hash, root, rel)
			for _, ago := range historyAgos {
				exec(`INSERT INTO history_dirs (ts, root_id, path_hash, bytes_alloc, files, tier) VALUES (?, ?, ?, ?, ?, 'raw')`,
					now-ago*day, root, hash, shrink(alloc, ago), shrink(f, ago))
			}
		}
	}
}

// historyAgos are the days ago of each fake history point.
var historyAgos = []int64{100, 70, 40, 20, 10, 3}

// relsWithAncestors lists every directory rel in dirs, its ancestors and the root ("").
func relsWithAncestors(dirs []gentest.Dir) []string {
	seen := map[string]bool{"": true}
	var rels []string
	for _, d := range dirs {
		for rel := d.Rel; ; {
			if !seen[rel] {
				seen[rel] = true
				rels = append(rels, rel)
			}
			i := strings.LastIndex(rel, "/")
			if i < 0 {
				break
			}
			rel = rel[:i]
		}
	}
	rels = append(rels, "")
	return rels
}

// shrink scales a total back by how long ago the point was (100 days is half).
func shrink(v, ago int64) int64 {
	return v - v*ago/200
}

// errorCount counts the scan errors a directory list carries.
func errorCount(dirs []gentest.Dir) int64 {
	var n int64
	for _, d := range dirs {
		n += int64(len(d.Errors))
	}
	return n
}
