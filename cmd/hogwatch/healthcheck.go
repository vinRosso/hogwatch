package main

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/vinRosso/hogwatch/internal/config"
	"github.com/vinRosso/hogwatch/internal/db"
	"github.com/vinRosso/hogwatch/internal/db/dbq"
)

// procCmdline returns the command line of the container's main process; tests
// replace it.
var procCmdline = func() ([]byte, error) { return os.ReadFile("/proc/1/cmdline") }

// healthKind says which service a process command line (NUL-separated argv)
// runs: "serve" or "scan". Any other subcommand is an error, because the
// healthcheck only judges the long-running services.
func healthKind(cmdline []byte) (string, error) {
	argv := bytes.Split(bytes.TrimRight(cmdline, "\x00"), []byte{0})
	if len(argv) < 2 {
		return "", errors.New("the main process has no subcommand")
	}
	switch k := string(argv[1]); k {
	case "serve", "scan":
		return k, nil
	default:
		return "", fmt.Errorf("the main process runs %q, not serve or scan", k)
	}
}

// listenToURL turns a listen address into the base URL the healthcheck dials:
// a wildcard host becomes the loopback.
func listenToURL(listen string) (string, error) {
	host, port, err := net.SplitHostPort(listen)
	if err != nil {
		return "", fmt.Errorf("listen %q: %w", listen, err)
	}
	if ip := net.ParseIP(host); host == "" || (ip != nil && ip.IsUnspecified()) {
		host = "127.0.0.1"
	}
	return "http://" + net.JoinHostPort(host, port), nil
}

// checkWeb asks the web process for /healthz, which is liveness only: a
// stopped scanner must not make the web container unhealthy.
func checkWeb(ctx context.Context, listen string, c *http.Client) error {
	base, err := listenToURL(listen)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/healthz", nil)
	if err != nil {
		return err
	}
	resp, err := c.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<10))
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("/healthz answered %d", resp.StatusCode)
	}
	return nil
}

// clockSkew is how far in the future a heartbeat may be and still count.
const clockSkew = 5 * time.Second

// checkScanner reads the scanner's heartbeat and fails when it is missing or
// not younger than db.HeartbeatMaxAge.
func checkScanner(ctx context.Context, d *sql.DB, now time.Time) error {
	st, err := dbq.New(d).GetScannerStatus(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return errors.New("the scanner has not written a heartbeat yet")
	}
	if err != nil {
		return err
	}
	// A heartbeat from the future (the clock stepped back) is not proof of life.
	age := now.Sub(time.Unix(st.HeartbeatAt, 0))
	if age >= db.HeartbeatMaxAge || age < -clockSkew {
		return fmt.Errorf("the scanner heartbeat is %s old", age.Truncate(time.Second))
	}
	return nil
}

// healthcheck is the container HEALTHCHECK. It exits 0 when the process whose
// kind is read from PID 1's command line is working, and 1 otherwise.
func healthcheck(ctx context.Context, _ []string, _, stderr io.Writer) int {
	fail := func(err error) int {
		_, _ = fmt.Fprintf(stderr, "hogwatch healthcheck: %v\n", err)
		return 1
	}
	cmdline, err := procCmdline()
	if err != nil {
		return fail(err)
	}
	kind, err := healthKind(cmdline)
	if err != nil {
		return fail(err)
	}
	cfg, err := config.Load(config.FromOS())
	if err != nil {
		return fail(err)
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if kind == "serve" {
		if err := checkWeb(ctx, cfg.Listen, http.DefaultClient); err != nil {
			return fail(err)
		}
		return 0
	}
	d, err := db.OpenReadOnly(ctx, filepath.Join(cfg.DataDir, "hogwatch.db"))
	if err != nil {
		return fail(err)
	}
	defer func() { _ = d.Close() }()
	if err := checkScanner(ctx, d, time.Now()); err != nil {
		return fail(err)
	}
	return 0
}
