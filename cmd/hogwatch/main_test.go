package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func runCmd(args ...string) (code int, stdout, stderr string) {
	var out, errb bytes.Buffer
	code = run(context.Background(), args, &out, &errb)
	return code, out.String(), errb.String()
}

func TestRun_NoArgs(t *testing.T) {
	code, stdout, stderr := runCmd()
	if code != 2 {
		t.Fatalf("exit = %d, want 2", code)
	}
	if !strings.Contains(stderr, "Usage: hogwatch <command>") {
		t.Errorf("stderr = %q, want usage", stderr)
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want empty", stdout)
	}
}

func TestRun_Help(t *testing.T) {
	for _, arg := range []string{"help", "-h", "--help"} {
		code, stdout, _ := runCmd(arg)
		if code != 0 {
			t.Fatalf("%s: exit = %d, want 0", arg, code)
		}
		for _, want := range []string{"Usage:", "serve", "scan", "bench", "healthcheck", "user"} {
			if !strings.Contains(stdout, want) {
				t.Errorf("%s: stdout missing %q: %q", arg, want, stdout)
			}
		}
	}
}

func TestRun_UnknownCommand(t *testing.T) {
	code, _, stderr := runCmd("frob")
	if code != 2 {
		t.Fatalf("exit = %d, want 2", code)
	}
	if !strings.Contains(stderr, `unknown command "frob"`) {
		t.Errorf("stderr = %q", stderr)
	}
}

func TestRun_ServeBadFlag(t *testing.T) {
	code, _, _ := runCmd("serve", "-nope")
	if code != 2 {
		t.Fatalf("exit = %d, want 2", code)
	}
}

// cleanEnv makes serve's config hermetic: every ambient HOGWATCH_* variable
// is blanked (config treats empty as unset) and HOGWATCH_CONFIG points at an
// empty file, so neither the host's environment nor /config/hogwatch.yaml
// can change the result.
func cleanEnv(t *testing.T) {
	t.Helper()
	for _, kv := range os.Environ() {
		if k, _, _ := strings.Cut(kv, "="); strings.HasPrefix(k, "HOGWATCH_") {
			t.Setenv(k, "")
		}
	}
	empty := filepath.Join(t.TempDir(), "hogwatch.yaml")
	if err := os.WriteFile(empty, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOGWATCH_CONFIG", empty)
}

func TestRun_ServeConfigError(t *testing.T) {
	const secretValue = "sentinel-s3cret-value"
	rows := []struct {
		name    string
		env     map[string]string
		setting string // named in the error
		value   string // invalid value, never echoed
	}{
		{"bad log level", map[string]string{
			"HOGWATCH_LOG_LEVEL":               "bogus",
			"HOGWATCH_AUTH_OIDC_CLIENT_SECRET": secretValue,
		}, "log.level", "bogus"},
		// The failing check is in the OIDC block that holds the secret.
		{"oidc issuer without client_id", map[string]string{
			"HOGWATCH_AUTH_OIDC_ISSUER":        "https://idp.example",
			"HOGWATCH_AUTH_OIDC_CLIENT_SECRET": secretValue,
		}, "client_id", ""},
	}
	for _, r := range rows {
		t.Run(r.name, func(t *testing.T) {
			cleanEnv(t)
			for k, v := range r.env {
				t.Setenv(k, v)
			}
			code, stdout, stderr := runCmd("serve")
			if code != 1 {
				t.Fatalf("exit = %d, want 1", code)
			}
			if !strings.HasPrefix(stderr, "hogwatch serve: ") || !strings.Contains(stderr, r.setting) {
				t.Errorf("stderr = %q, want prefix and %q named", stderr, r.setting)
			}
			if strings.Contains(stderr, secretValue) || strings.Contains(stdout, secretValue) {
				t.Errorf("secret leaked: stdout=%q stderr=%q", stdout, stderr)
			}
			if r.value != "" && strings.Contains(stderr, r.value) {
				t.Errorf("stderr echoes the invalid value: %q", stderr)
			}
		})
	}
}

func TestRun_ServeDBOpenError(t *testing.T) {
	cleanEnv(t)
	t.Setenv("HOGWATCH_DATA_DIR", filepath.Join(t.TempDir(), "missing"))
	t.Setenv("HOGWATCH_LISTEN", "127.0.0.1:0")
	// The timeout bounds the run if serve wrongly gets as far as listening.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var out, errb bytes.Buffer
	code := run(ctx, []string{"serve"}, &out, &errb)
	stdout, stderr := out.String(), errb.String()
	if code != 1 {
		t.Fatalf("exit = %d, want 1 (stdout=%q)", code, stdout)
	}
	if !strings.HasPrefix(stderr, "hogwatch serve: ") || !strings.Contains(stderr, "hogwatch.db") {
		t.Errorf("stderr = %q, want prefix and the DB path", stderr)
	}
	if strings.Contains(stdout, `"listening"`) || strings.Contains(stdout, "msg=listening") {
		t.Errorf("serve listened despite the DB error: %q", stdout)
	}
}
