package zfs_test

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vinRosso/hogwatch/internal/zfs"
)

// The sidecar scripts run against a fake `zfs` and `zpool` put first on PATH,
// so no ZFS is needed. sh is POSIX here, as in the sidecar image.

const readerDir = "../../contrib/zfs-reader"

const fakeZFS = `#!/bin/sh
case "$1" in
list)
	if [ "$6" = filesystem ]; then
		[ -n "${FAKE_FS_FAIL:-}" ] && { echo "cannot open /dev/zfs" >&2; exit 1; }
		cat "$FAKE_FS_FILE"
	else
		[ -n "${FAKE_SNAP_FAIL:-}" ] && { echo "snapshot listing failed" >&2; exit 1; }
		cat "$FAKE_SNAP_FILE"
	fi ;;
allow) cat "$FAKE_ALLOW_DIR/$(printf %s "$2" | tr / -)" 2>/dev/null ;;
*) exit 2 ;;
esac
`

const fakeZpool = `#!/bin/sh
for p in ${FAKE_POOLS:-}; do echo "$p"; done
`

const datasets = "tank\t/tank\t1000\t9000\t0\t0\t50\n" +
	"tank/media\t/tank/media\t700\t300\t1000\t0\t10\n"

const snapshots = "tank@daily\t-\t5\t-\t-\t-\t-\n"

type env struct {
	t    *testing.T
	dir  string
	vars []string
}

func newEnv(t *testing.T) *env {
	t.Helper()
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not available")
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "bin")
	if err := os.Mkdir(bin, 0o700); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{"zfs": fakeZFS, "zpool": fakeZpool} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte(body), 0o700); err != nil { //nolint:gosec // a test fixture that must be executable
			t.Fatal(err)
		}
	}
	e := &env{t: t, dir: dir}
	e.write("fs.tsv", datasets)
	e.write("snap.tsv", snapshots)
	if err := os.Mkdir(filepath.Join(dir, "allow"), 0o700); err != nil {
		t.Fatal(err)
	}
	e.vars = []string{
		"PATH=" + bin + string(os.PathListSeparator) + os.Getenv("PATH"),
		"FAKE_FS_FILE=" + filepath.Join(dir, "fs.tsv"),
		"FAKE_SNAP_FILE=" + filepath.Join(dir, "snap.tsv"),
		"FAKE_ALLOW_DIR=" + filepath.Join(dir, "allow"),
	}
	return e
}

func (e *env) write(name, body string) {
	e.t.Helper()
	if err := os.WriteFile(filepath.Join(e.dir, name), []byte(body), 0o600); err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) out() string { return filepath.Join(e.dir, "zfs.tsv") }

// run executes a reader script with extra environment, returning the error and
// the combined output.
func (e *env) run(script string, extra []string, args ...string) (string, error) {
	e.t.Helper()
	cmd := exec.Command("sh", append([]string{filepath.Join(readerDir, script)}, args...)...) //nolint:gosec // fixed test scripts
	cmd.Env = append(append(os.Environ(), e.vars...), extra...)
	var buf bytes.Buffer
	cmd.Stdout, cmd.Stderr = &buf, &buf
	err := cmd.Run()
	return buf.String(), err
}

func TestSidecar_BadFieldCountKeepsPrevious(t *testing.T) {
	e := newEnv(t)
	if out, err := e.run("write-zfs-tsv.sh", nil, e.out()); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	before, _ := os.ReadFile(e.out())
	e.write("fs.tsv", "tank\t/tank\t1\t2\t0\t0\t0\textra\n")
	if _, err := e.run("write-zfs-tsv.sh", nil, e.out()); err == nil {
		t.Fatal("an 8-field line was accepted")
	}
	if after, _ := os.ReadFile(e.out()); !bytes.Equal(before, after) {
		t.Error("an unreadable file replaced the previous one")
	}
}

func (e *env) entries() []string {
	e.t.Helper()
	ents, err := os.ReadDir(e.dir)
	if err != nil {
		e.t.Fatal(err)
	}
	var names []string
	for _, en := range ents {
		names = append(names, en.Name())
	}
	return names
}

func TestSidecar_OutputParses(t *testing.T) {
	e := newEnv(t)
	if out, err := e.run("write-zfs-tsv.sh", nil, e.out()); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	ds, err := zfs.Load(e.out())
	if err != nil {
		t.Fatal(err)
	}
	if len(ds) != 2 || ds[1].Name != "tank/media" || ds[1].Used != 700 || ds[1].Avail != 300 || ds[1].Quota != 1000 || ds[0].UsedSnapshots != 50 {
		t.Fatalf("parsed %+v", ds)
	}
	raw, _ := os.ReadFile(e.out())
	if !strings.Contains(string(raw), "tank@daily") {
		t.Error("snapshot lines are missing from the file")
	}
	fi, _ := os.Stat(e.out())
	if fi.Mode().Perm() != 0o644 {
		t.Errorf("mode = %v, want 0644", fi.Mode().Perm())
	}
}

func TestSidecar_ZeroSnapshotsIsValid(t *testing.T) {
	e := newEnv(t)
	e.write("snap.tsv", "")
	if out, err := e.run("write-zfs-tsv.sh", nil, e.out()); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	if ds, err := zfs.Load(e.out()); err != nil || len(ds) != 2 {
		t.Fatalf("Load = %v, %v", ds, err)
	}
}

func TestSidecar_FailureKeepsPrevious(t *testing.T) {
	for name, extra := range map[string]string{
		"snapshot listing fails": "FAKE_SNAP_FAIL=1",
		"dataset listing fails":  "FAKE_FS_FAIL=1",
	} {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t)
			if out, err := e.run("write-zfs-tsv.sh", nil, e.out()); err != nil {
				t.Fatalf("first write: %v: %s", err, out)
			}
			before, _ := os.ReadFile(e.out())
			names := e.entries()
			e.write("fs.tsv", "other\t/other\t1\t2\t0\t0\t0\n")
			if _, err := e.run("write-zfs-tsv.sh", []string{extra}, e.out()); err == nil {
				t.Fatal("the failing run exited 0")
			}
			after, _ := os.ReadFile(e.out())
			if !bytes.Equal(before, after) {
				t.Errorf("the previous file changed:\n%s\n->\n%s", before, after)
			}
			if got := e.entries(); strings.Join(got, ",") != strings.Join(names, ",") {
				t.Errorf("files left behind: %v, want %v", got, names)
			}
		})
	}
}

func TestSidecar_EmptyDatasetsKeepsPrevious(t *testing.T) {
	e := newEnv(t)
	if out, err := e.run("write-zfs-tsv.sh", nil, e.out()); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	before, _ := os.ReadFile(e.out())
	e.write("fs.tsv", "")
	if _, err := e.run("write-zfs-tsv.sh", nil, e.out()); err == nil {
		t.Fatal("an empty dataset list exited 0")
	}
	if after, _ := os.ReadFile(e.out()); !bytes.Equal(before, after) {
		t.Error("an empty dataset list replaced the previous file")
	}
	for _, n := range e.entries() {
		if strings.HasPrefix(n, ".zfs.tsv.") {
			t.Errorf("temp file %s left behind", n)
		}
	}
}

func TestSidecar_AllowWarning(t *testing.T) {
	cases := []struct {
		name, allow string
		warn        bool
	}{
		{"everyone", "---- Permissions on tank ----\nLocal+Descendent permissions:\n\teveryone send\n", true},
		{"root", "---- Permissions on tank ----\nLocal+Descendent permissions:\n\tuser root snapshot\n", true},
		{"ordinary user", "---- Permissions on tank ----\nLocal+Descendent permissions:\n\tuser alice send\n", false},
		{"no delegation", "", false},
	}
	t.Run("child dataset", func(t *testing.T) {
		e := newEnv(t)
		e.write("fs.tsv", datasets)
		if err := os.WriteFile(filepath.Join(e.dir, "allow", "tank-media"), nil, 0o600); err != nil {
			t.Fatal(err)
		}
		// The fake answers `zfs allow tank/media` from a file named after the
		// dataset with "/" replaced by "-".
		if err := os.WriteFile(filepath.Join(e.dir, "allow", "tank-media"), []byte("\tuser root snapshot\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		out, err := e.run("entrypoint.sh", []string{"ZFS_ONCE=1", "ZFS_OUT=" + e.out()})
		if err != nil {
			t.Fatalf("%v: %s", err, out)
		}
		if !strings.Contains(out, "WARNING") || !strings.Contains(out, "tank/media") {
			t.Errorf("a delegation on a child dataset was not reported: %s", out)
		}
	})
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			if err := os.WriteFile(filepath.Join(e.dir, "allow", "tank"), []byte(tc.allow), 0o600); err != nil {
				t.Fatal(err)
			}
			out, err := e.run("entrypoint.sh", []string{"ZFS_ONCE=1", "ZFS_OUT=" + e.out(), "FAKE_POOLS=tank"})
			if err != nil {
				t.Fatalf("%v: %s", err, out)
			}
			if got := strings.Contains(out, "WARNING"); got != tc.warn {
				t.Errorf("warning = %v, want %v; output: %s", got, tc.warn, out)
			}
			if _, err := zfs.Load(e.out()); err != nil {
				t.Errorf("the one cycle did not write a parsable file: %v", err)
			}
		})
	}
}

func TestSidecar_EntrypointSurvivesFailedCycle(t *testing.T) {
	e := newEnv(t)
	out, err := e.run("entrypoint.sh", []string{"ZFS_ONCE=1", "ZFS_OUT=" + e.out(), "FAKE_FS_FAIL=1"})
	if err != nil {
		t.Fatalf("a failed cycle must not exit non-zero in the loop: %v: %s", err, out)
	}
	if !strings.Contains(out, "refresh failed") {
		t.Errorf("no failure message: %s", out)
	}
}

func TestSidecar_Healthcheck(t *testing.T) {
	e := newEnv(t)
	extra := []string{"ZFS_OUT=" + e.out(), "ZFS_INTERVAL=300"}
	if _, err := e.run("entrypoint.sh", extra, "healthcheck"); err == nil {
		t.Error("healthcheck passed with no file")
	}
	e.write("zfs.tsv", datasets)
	if out, err := e.run("entrypoint.sh", extra, "healthcheck"); err != nil {
		t.Errorf("fresh file: %v: %s", err, out)
	}
	old := filepath.Join(e.dir, "zfs.tsv")
	for _, c := range []struct {
		minutes int
		ok      bool
	}{{14, true}, {16, false}, {40, false}} { // the limit is 3 x 300 s = 15 min
		past := fileTimeAgo(t, c.minutes*60)
		if err := os.Chtimes(old, past, past); err != nil {
			t.Fatal(err)
		}
		_, err := e.run("entrypoint.sh", extra, "healthcheck")
		if (err == nil) != c.ok {
			t.Errorf("a %d minute old file: healthy = %v, want %v", c.minutes, err == nil, c.ok)
		}
	}
}

func fileTimeAgo(t *testing.T, seconds int) time.Time {
	t.Helper()
	return time.Now().Add(-time.Duration(seconds) * time.Second)
}
