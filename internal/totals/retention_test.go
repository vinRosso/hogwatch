//go:build linux

package totals_test

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/vinRosso/hogwatch/internal/db"
	"github.com/vinRosso/hogwatch/internal/totals"
)

var now = time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)

func mainDB(t *testing.T) *sql.DB {
	t.Helper()
	ctx := context.Background()
	d, err := db.Open(ctx, filepath.Join(t.TempDir(), "hogwatch.db"))
	must(t, err)
	t.Cleanup(func() { _ = d.Close() })
	must(t, db.Migrate(ctx, d))
	for _, id := range []int64{1, 2} {
		_, err = d.Exec(`INSERT INTO scan_roots (id, name, container_path, host_path) VALUES (?, 'r' || ?, '/h', '/h')`, id, id)
		must(t, err)
	}
	return d
}

func addTotal(t *testing.T, d *sql.DB, ts time.Time, userID, rootID int64, tier string) {
	t.Helper()
	_, err := d.Exec(`INSERT INTO history_totals (ts, scope, user_id, root_id, bytes_logical, bytes_alloc, files, tier)
		VALUES (?, 'user_mount', ?, ?, 1, 1, 1, ?)`, ts.Unix(), userID, rootID, tier)
	must(t, err)
}

func retain(t *testing.T, d *sql.DB, at time.Time) totals.RetainStats {
	t.Helper()
	tx, err := d.BeginTx(context.Background(), nil)
	must(t, err)
	st, err := totals.Retain(context.Background(), tx, at)
	if err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	must(t, tx.Commit())
	return st
}

type pt struct {
	ts   int64
	tier string
}

func points(t *testing.T, d *sql.DB, userID int64) []pt {
	t.Helper()
	rs, err := d.Query(`SELECT ts, tier FROM history_totals WHERE user_id = ? ORDER BY ts`, userID)
	must(t, err)
	defer func() { _ = rs.Close() }()
	var out []pt
	for rs.Next() {
		var p pt
		must(t, rs.Scan(&p.ts, &p.tier))
		out = append(out, p)
	}
	must(t, rs.Err())
	return out
}

func TestRetention_Tiers(t *testing.T) {
	d := mainDB(t)
	for i := 0; i < 400; i++ {
		addTotal(t, d, now.AddDate(0, 0, -i), 1, 1, "raw")
		addTotal(t, d, now.AddDate(0, 0, -i), 2, 1, "raw")
	}
	retain(t, d, now)
	got := points(t, d, 1)
	if len(points(t, d, 2)) != len(got) {
		t.Fatal("keys differ in outcome")
	}

	cut30, cut365 := now.AddDate(0, 0, -30), now.AddDate(0, 0, -365)
	raw, weeks, months := 0, map[[2]int]pt{}, map[[2]int]pt{}
	for _, p := range got {
		ts := time.Unix(p.ts, 0).UTC()
		switch {
		case !ts.Before(cut30):
			if p.tier != "raw" {
				t.Errorf("%v: tier %s, want raw", ts, p.tier)
			}
			raw++
		case !ts.Before(cut365):
			if p.tier != "weekly" {
				t.Errorf("%v: tier %s, want weekly", ts, p.tier)
			}
			y, w := ts.ISOWeek()
			if _, dup := weeks[[2]int{y, w}]; dup {
				t.Errorf("two weekly rows in %d-W%d", y, w)
			}
			weeks[[2]int{y, w}] = p
		default:
			if p.tier != "monthly" {
				t.Errorf("%v: tier %s, want monthly", ts, p.tier)
			}
			k := [2]int{ts.Year(), int(ts.Month())}
			if _, dup := months[k]; dup {
				t.Errorf("two monthly rows in %v", k)
			}
			months[k] = p
		}
	}
	if raw != 31 { // days 0..30 inclusive: now-30d is still raw
		t.Errorf("raw = %d, want 31", raw)
	}
	// Each survivor is the latest original point of its bucket.
	for k, p := range weeks {
		var latest int64
		for i := 0; i < 400; i++ {
			ts := now.AddDate(0, 0, -i)
			y, w := ts.ISOWeek()
			if ts.Before(cut30) && !ts.Before(cut365) && [2]int{y, w} == k && ts.Unix() > latest {
				latest = ts.Unix()
			}
		}
		if p.ts != latest {
			t.Errorf("week %v kept %d, want %d", k, p.ts, latest)
		}
	}
	for k, p := range months {
		var latest int64
		for i := 0; i < 400; i++ {
			ts := now.AddDate(0, 0, -i)
			if ts.Before(cut365) && [2]int{ts.Year(), int(ts.Month())} == k && ts.Unix() > latest {
				latest = ts.Unix()
			}
		}
		if p.ts != latest {
			t.Errorf("month %v kept %d, want %d", k, p.ts, latest)
		}
	}
	if len(weeks) == 0 || len(months) == 0 {
		t.Fatalf("weeks=%d months=%d", len(weeks), len(months))
	}

	st := retain(t, d, now)
	if st != (totals.RetainStats{}) {
		t.Errorf("second pass stats = %+v, want zero", st)
	}
	again := points(t, d, 1)
	if len(again) != len(got) {
		t.Fatalf("second pass changed row count %d -> %d", len(got), len(again))
	}
	for i := range got {
		if got[i] != again[i] {
			t.Fatalf("second pass changed row %d: %+v -> %+v", i, got[i], again[i])
		}
	}
}

func TestRetention_BucketEdges(t *testing.T) {
	d := mainDB(t)
	at := time.Date(2026, 10, 26, 0, 0, 0, 0, time.UTC)
	edge := at.AddDate(0, 0, -30) // exactly now-30d: still raw
	addTotal(t, d, edge, 1, 1, "raw")
	addTotal(t, d, edge.Add(-time.Second), 2, 1, "raw") // one second older: weekly

	// Monday 00:00 UTC starts a new ISO week; the second before it ends the previous one.
	mon := time.Date(2026, 3, 2, 0, 0, 0, 0, time.UTC)
	addTotal(t, d, mon, 3, 1, "raw")
	addTotal(t, d, mon.Add(-time.Second), 3, 1, "raw")
	// The first instant of a month starts a monthly bucket.
	m1 := time.Date(2025, 5, 1, 0, 0, 0, 0, time.UTC)
	addTotal(t, d, m1, 4, 1, "raw")
	addTotal(t, d, m1.Add(-time.Second), 4, 1, "raw")
	retain(t, d, at)

	if p := points(t, d, 1); len(p) != 1 || p[0].tier != "raw" {
		t.Errorf("now-30d: %+v", p)
	}
	if p := points(t, d, 2); len(p) != 1 || p[0].tier != "weekly" {
		t.Errorf("now-30d-1s: %+v", p)
	}
	if p := points(t, d, 3); len(p) != 2 || p[0].tier != "weekly" || p[1].tier != "weekly" {
		t.Errorf("monday edge should be two weekly rows: %+v", p)
	}
	if p := points(t, d, 4); len(p) != 2 || p[0].tier != "monthly" || p[1].tier != "monthly" {
		t.Errorf("month edge should be two monthly rows: %+v", p)
	}
}

func TestRetention_StraddlingWeek(t *testing.T) {
	d := mainDB(t)
	at := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	// Week Mon 2026-09-21 .. Sun 09-27; now-30d is 09-26 12:00, so Sep 21..26 06:00 are older.
	for day := 21; day <= 27; day++ {
		addTotal(t, d, time.Date(2026, 9, day, 18, 0, 0, 0, time.UTC), 1, 1, "raw")
	}
	retain(t, d, at)
	weekly := 0
	for _, p := range points(t, d, 1) {
		if p.tier == "weekly" {
			weekly++
		}
	}
	if weekly != 1 {
		t.Fatalf("first pass: %d weekly rows in %+v", weekly, points(t, d, 1))
	}
	retain(t, d, at.AddDate(0, 0, 7))
	final := points(t, d, 1)
	if len(final) != 1 || final[0].tier != "weekly" ||
		final[0].ts != time.Date(2026, 9, 27, 18, 0, 0, 0, time.UTC).Unix() {
		t.Fatalf("after second pass: %+v", final)
	}
}

func TestRetention_Empty(t *testing.T) {
	d := mainDB(t)
	st := retain(t, d, now)
	if st != (totals.RetainStats{}) {
		t.Fatalf("stats = %+v", st)
	}
}

func TestRetention_Dirs(t *testing.T) {
	d := mainDB(t)
	old := now.AddDate(0, 0, -60)
	ins := func(ts time.Time, root, hash int64) {
		_, err := d.Exec(`INSERT INTO history_dirs (ts, root_id, path_hash, bytes_alloc, files, tier) VALUES (?, ?, ?, 1, 1, 'raw')`,
			ts.Unix(), root, hash)
		must(t, err)
	}
	// Same week, same key: collapse. Another hash and another root stay apart.
	ins(old, 1, 100)
	ins(old.Add(time.Hour), 1, 100)
	ins(old, 1, 101)
	ins(old, 2, 100)
	st := retain(t, d, now)
	var n int
	must(t, d.QueryRow(`SELECT count(*) FROM history_dirs`).Scan(&n))
	if n != 3 || st.Deleted != 1 || st.Promoted != 3 {
		t.Fatalf("rows=%d stats=%+v", n, st)
	}
}

func TestRetention_OrphanPaths(t *testing.T) {
	d := mainDB(t)
	_, err := d.Exec(`INSERT INTO history_paths (path_hash, root_id, rel_path) VALUES (1, 1, x'ff'), (2, 1, CAST('keep' AS BLOB))`)
	must(t, err)
	_, err = d.Exec(`INSERT INTO history_dirs (ts, root_id, path_hash, bytes_alloc, files, tier) VALUES (?, 1, 2, 1, 1, 'raw')`, now.Unix())
	must(t, err)
	st := retain(t, d, now)
	if st.PathsDeleted != 1 {
		t.Fatalf("stats = %+v", st)
	}
	var n int
	must(t, d.QueryRow(`SELECT count(*) FROM history_paths WHERE path_hash = 2`).Scan(&n))
	if n != 1 {
		t.Fatal("referenced path removed")
	}
	must(t, d.QueryRow(`SELECT count(*) FROM history_paths`).Scan(&n))
	if n != 1 {
		t.Fatalf("paths = %d", n)
	}
}
