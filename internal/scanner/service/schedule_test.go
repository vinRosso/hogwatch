//go:build linux

package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/vinRosso/hogwatch/internal/schedule"
)

var t0 = time.Date(2026, 1, 10, 12, 0, 0, 0, time.UTC)

func scanJobs(t *testing.T, d *sql.DB, root int64) int {
	t.Helper()
	return count(t, d, `SELECT count(*) FROM jobs WHERE kind = 'scan' AND root_id = ?`, root)
}

func addScan(t *testing.T, d *sql.DB, root int64, started time.Time) {
	t.Helper()
	exec(t, d, `INSERT INTO scans (root_id, file, status, trigger, started_at) VALUES (?, 'x.db', 'done', 'schedule', ?)`, root, started.Unix())
}

func tick(t *testing.T, s *Scheduler, now time.Time) {
	t.Helper()
	if err := s.Tick(context.Background(), now); err != nil {
		t.Fatal(err)
	}
}

func TestScheduler_NeverScanned(t *testing.T) {
	d, _ := newDB(t)
	root := addRoot(t, d, "a", "@hourly", true)
	s := NewScheduler(d, quietLog())
	tick(t, s, t0)
	if n := scanJobs(t, d, root); n != 1 {
		t.Fatalf("first tick queued %d jobs, want 1", n)
	}
	var params string
	if err := d.QueryRow(`SELECT params FROM jobs WHERE root_id = ?`, root).Scan(&params); err != nil {
		t.Fatal(err)
	}
	var p struct{ Trigger string }
	if err := json.Unmarshal([]byte(params), &p); err != nil || p.Trigger != "schedule" {
		t.Errorf("params = %s (%v), want trigger schedule", params, err)
	}
	tick(t, s, t0)
	if n := scanJobs(t, d, root); n != 1 {
		t.Fatalf("second tick at the same time: %d jobs, want 1", n)
	}
}

func TestScheduler_MissedFires(t *testing.T) {
	d, _ := newDB(t)
	root := addRoot(t, d, "a", "@hourly", true)
	addScan(t, d, root, t0.Add(-3*time.Hour))
	s := NewScheduler(d, quietLog())
	tick(t, s, t0)
	tick(t, s, t0.Add(time.Second))
	if n := scanJobs(t, d, root); n != 1 {
		t.Fatalf("%d jobs, want exactly 1", n)
	}
}

func TestScheduler_NotDueYet(t *testing.T) {
	d, _ := newDB(t)
	root := addRoot(t, d, "a", "@hourly", true)
	addScan(t, d, root, t0.Add(-10*time.Minute)) // 11:50, next fire 12:00
	s := NewScheduler(d, quietLog())
	tick(t, s, t0.Add(-time.Second))
	if n := scanJobs(t, d, root); n != 0 {
		t.Fatalf("%d jobs before the fire time, want 0", n)
	}
	tick(t, s, t0)
	if n := scanJobs(t, d, root); n != 1 {
		t.Fatalf("%d jobs at the fire time, want 1", n)
	}
}

func issues(t *testing.T, d *sql.DB, root int64) (n int, details string) {
	t.Helper()
	n = count(t, d, `SELECT count(*) FROM config_issues WHERE root_id = ? AND kind = 'invalid_schedule'`, root)
	if n > 0 {
		if err := d.QueryRow(`SELECT details FROM config_issues WHERE root_id = ? AND kind = 'invalid_schedule' LIMIT 1`, root).Scan(&details); err != nil {
			t.Fatal(err)
		}
	}
	return n, details
}

func TestScheduler_Invalid(t *testing.T) {
	d, _ := newDB(t)
	bad := addRoot(t, d, "bad", "bogus", true)
	good := addRoot(t, d, "good", "@daily", true)
	s := NewScheduler(d, quietLog())
	tick(t, s, t0)
	tick(t, s, t0.Add(time.Second)) // re-evaluation must not duplicate the issue
	if n := scanJobs(t, d, good); n != 1 {
		t.Errorf("good root: %d jobs, want 1", n)
	}
	if n := scanJobs(t, d, bad); n != 0 {
		t.Errorf("bad root: %d jobs, want 0", n)
	}
	n, details := issues(t, d, bad)
	if n != 1 {
		t.Fatalf("invalid_schedule issues = %d, want 1", n)
	}
	var det struct{ Schedule, Error string }
	if err := json.Unmarshal([]byte(details), &det); err != nil || det.Schedule != "bogus" || det.Error == "" {
		t.Errorf("details = %s (%v)", details, err)
	}
	if n, _ := issues(t, d, good); n != 0 {
		t.Error("good root has an invalid_schedule issue")
	}

	exec(t, d, `UPDATE scan_roots SET schedule = '@daily' WHERE id = ?`, bad)
	tick(t, s, t0.Add(2*time.Second))
	if n, _ := issues(t, d, bad); n != 0 {
		t.Error("issue not removed after the schedule was fixed")
	}
	if n := scanJobs(t, d, bad); n != 1 {
		t.Errorf("fixed root: %d jobs, want 1", n)
	}

	// A changed invalid schedule replaces the issue.
	exec(t, d, `UPDATE scan_roots SET schedule = 'nope' WHERE id = ?`, bad)
	tick(t, s, t0.Add(3*time.Second))
	exec(t, d, `UPDATE scan_roots SET schedule = 'nope2' WHERE id = ?`, bad)
	tick(t, s, t0.Add(4*time.Second))
	n, details = issues(t, d, bad)
	if n != 1 {
		t.Fatalf("issues = %d, want 1", n)
	}
	if err := json.Unmarshal([]byte(details), &det); err != nil || det.Schedule != "nope2" {
		t.Errorf("details = %s", details)
	}
}

func TestScheduler_DisabledAndEmpty(t *testing.T) {
	d, _ := newDB(t)
	off := addRoot(t, d, "off", "@hourly", false)
	empty := addRoot(t, d, "empty", "", true)
	s := NewScheduler(d, quietLog())
	tick(t, s, t0)
	if scanJobs(t, d, off) != 0 || scanJobs(t, d, empty) != 0 {
		t.Fatal("disabled or empty-schedule root got a job")
	}
}

func TestScheduler_NoDuplicate(t *testing.T) {
	d, _ := newDB(t)
	root := addRoot(t, d, "a", "@hourly", true)
	addJob(t, d, "scan", root, "queued", `{"trigger":"manual"}`)
	s := NewScheduler(d, quietLog())
	tick(t, s, t0)
	if n := scanJobs(t, d, root); n != 1 {
		t.Fatalf("%d scan jobs, want only the manual one", n)
	}
	// The fire was consumed: finishing the manual job does not trigger an
	// immediate scheduled one for the same fire time.
	exec(t, d, `UPDATE jobs SET status = 'done'`)
	tick(t, s, t0)
	if n := scanJobs(t, d, root); n != 1 {
		t.Fatalf("%d scan jobs after the manual one finished, want 1", n)
	}
}

func TestScheduler_RunningJobBlocks(t *testing.T) {
	d, _ := newDB(t)
	root := addRoot(t, d, "a", "@hourly", true)
	addJob(t, d, "scan", root, "running", `{"trigger":"manual"}`)
	s := NewScheduler(d, quietLog())
	tick(t, s, t0)
	if n := scanJobs(t, d, root); n != 1 {
		t.Fatalf("%d scan jobs, want 1", n)
	}
}

func TestScheduler_CronTZ(t *testing.T) {
	d, _ := newDB(t)
	root := addRoot(t, d, "a", "CRON_TZ=Europe/Rome 0 3 * * *", true)
	addScan(t, d, root, time.Date(2026, 1, 10, 12, 0, 0, 0, time.UTC)) // after the Jan 10 fire
	s := NewScheduler(d, quietLog())
	tick(t, s, time.Date(2026, 1, 11, 1, 59, 59, 0, time.UTC))
	if n := scanJobs(t, d, root); n != 0 {
		t.Fatalf("%d jobs at 01:59:59 UTC, want 0", n)
	}
	tick(t, s, time.Date(2026, 1, 11, 2, 0, 0, 0, time.UTC))
	if n := scanJobs(t, d, root); n != 1 {
		t.Fatalf("%d jobs at 02:00 UTC (03:00 Rome), want 1", n)
	}
}

// TestScheduler_UTCNotLocal pins that a schedule without CRON_TZ runs in UTC
// whatever the zone of now or of the process.
func TestScheduler_UTCNotLocal(t *testing.T) {
	plus5 := time.FixedZone("plus5", 5*3600)
	old := time.Local
	time.Local = time.FixedZone("local-minus7", -7*3600)
	t.Cleanup(func() { time.Local = old })

	for _, tc := range []struct {
		schedule string
		last     time.Time // UTC
		before   time.Time // UTC, must not fire
		at       time.Time // UTC, must fire
	}{
		{"@daily", time.Date(2026, 1, 10, 12, 0, 0, 0, time.UTC),
			time.Date(2026, 1, 10, 23, 59, 59, 0, time.UTC), time.Date(2026, 1, 11, 0, 0, 0, 0, time.UTC)},
		{"0 3 * * *", time.Date(2026, 1, 10, 12, 0, 0, 0, time.UTC),
			time.Date(2026, 1, 11, 2, 59, 59, 0, time.UTC), time.Date(2026, 1, 11, 3, 0, 0, 0, time.UTC)},
	} {
		for zname, zone := range map[string]*time.Location{"fixed+5": plus5, "local-7": time.Local} {
			d, _ := newDB(t)
			root := addRoot(t, d, "a", tc.schedule, true)
			addScan(t, d, root, tc.last)
			s := NewScheduler(d, quietLog())
			tick(t, s, tc.before.In(zone))
			if n := scanJobs(t, d, root); n != 0 {
				t.Errorf("%s in %s: %d jobs before the UTC fire time", tc.schedule, zname, n)
			}
			tick(t, s, tc.at.In(zone))
			if n := scanJobs(t, d, root); n != 1 {
				t.Errorf("%s in %s: %d jobs at the UTC fire time, want 1", tc.schedule, zname, n)
			}
		}
	}
}

func TestParseSchedule_ErrorKeepsOriginal(t *testing.T) {
	_, err := schedule.Parse("bogus")
	if err == nil || strings.Contains(err.Error(), "CRON_TZ") {
		t.Fatalf("err = %v, want an error without the CRON_TZ prefix", err)
	}
}

// TestParseSchedule_UTCForAnyInputZone pins that a spec without CRON_TZ
// evaluates in UTC even when Next is given a time in another zone.
func TestParseSchedule_UTCForAnyInputZone(t *testing.T) {
	plus5 := time.FixedZone("plus5", 5*3600)
	sched, err := schedule.Parse("0 3 * * *")
	if err != nil {
		t.Fatal(err)
	}
	got := sched.Next(time.Date(2026, 1, 10, 12, 0, 0, 0, time.UTC).In(plus5))
	if want := time.Date(2026, 1, 11, 3, 0, 0, 0, time.UTC); !got.Equal(want) {
		t.Errorf("Next = %v, want %v", got.UTC(), want)
	}
}
