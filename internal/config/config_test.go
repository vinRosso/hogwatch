package config

import (
	"bytes"
	"errors"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// src builds a map-backed Source. Tests never read the real environment.
func src(env map[string]string, files map[string]string, gmp int) Source {
	return Source{
		LookupEnv: func(k string) (string, bool) { v, ok := env[k]; return v, ok },
		ReadFile: func(p string) ([]byte, error) {
			if v, ok := files[p]; ok {
				return []byte(v), nil
			}
			return nil, &fs.PathError{Op: "open", Path: p, Err: fs.ErrNotExist}
		},
		GOMAXPROCS: gmp,
	}
}

func TestLoad_Defaults(t *testing.T) {
	c, err := Load(src(nil, nil, 8))
	if err != nil {
		t.Fatal(err)
	}
	want := Config{
		Listen:  ":8080",
		DataDir: "/data",
		Log:     Log{Level: "info", Format: "text"},
		Scan:    Scan{Workers: 4, ListingWorkers: 2, HostPrefix: "/host", ZFSFile: "/zfs/zfs.tsv", GenerationGrace: 5 * time.Minute},
		Auth: Auth{
			Local: AuthLocal{Enabled: true, MinPasswordLen: 8, BreakGlassAdmins: []string{}},
			OIDC:  AuthOIDC{GroupsClaim: "groups"},
		},
		Session:        Session{IdleTimeout: 12 * time.Hour, AbsoluteTimeout: 168 * time.Hour, OIDCAdminTimeout: 12 * time.Hour},
		TrustedProxies: []string{},
	}
	if !reflect.DeepEqual(c, want) {
		t.Fatalf("got %+v\nwant %+v", c, want)
	}
}

func TestLoad_Precedence(t *testing.T) {
	file := `
listen: ":9000"
scan:
  workers: 3
session:
  idle_timeout: 1h
auth:
  local:
    enabled: false
    break_glass_admins: [a, b]
`
	files := map[string]string{DefaultConfigPath: file}
	env := map[string]string{
		"HOGWATCH_SCAN_WORKERS":                  "2",
		"HOGWATCH_SESSION_IDLE_TIMEOUT":          "30m",
		"HOGWATCH_AUTH_LOCAL_ENABLED":            "true",
		"HOGWATCH_AUTH_LOCAL_BREAK_GLASS_ADMINS": "x, y ,,z",
	}

	// default -> file
	c, err := Load(src(nil, files, 8))
	if err != nil {
		t.Fatal(err)
	}
	if c.Listen != ":9000" || c.Scan.Workers != 3 || c.Session.IdleTimeout != time.Hour ||
		c.Auth.Local.Enabled || !reflect.DeepEqual(c.Auth.Local.BreakGlassAdmins, []string{"a", "b"}) {
		t.Fatalf("file did not override defaults: %+v", c)
	}
	// file -> env
	c, err = Load(src(env, files, 8))
	if err != nil {
		t.Fatal(err)
	}
	if c.Listen != ":9000" || c.Scan.Workers != 2 || c.Session.IdleTimeout != 30*time.Minute ||
		!c.Auth.Local.Enabled || !reflect.DeepEqual(c.Auth.Local.BreakGlassAdmins, []string{"x", "y", "z"}) {
		t.Fatalf("env did not override file: %+v", c)
	}
	// env beats default with no file
	env2 := map[string]string{"HOGWATCH_LISTEN": "127.0.0.1:1"}
	c, err = Load(src(env2, nil, 8))
	if err != nil || c.Listen != "127.0.0.1:1" {
		t.Fatalf("got %+v, %v", c, err)
	}
}

func TestLoad_EnvNames(t *testing.T) {
	want := []string{
		"HOGWATCH_LISTEN",
		"HOGWATCH_BASE_URL",
		"HOGWATCH_DATA_DIR",
		"HOGWATCH_LOG_LEVEL",
		"HOGWATCH_LOG_FORMAT",
		"HOGWATCH_SCAN_WORKERS",
		"HOGWATCH_SCAN_LISTING_WORKERS",
		"HOGWATCH_SCAN_HOST_PREFIX",
		"HOGWATCH_SCAN_ZFS_FILE",
		"HOGWATCH_SCAN_GENERATION_GRACE",
		"HOGWATCH_SCAN_ALLOW_WRITABLE_MOUNTS",
		"HOGWATCH_AUTH_LOCAL_ENABLED",
		"HOGWATCH_AUTH_LOCAL_MIN_PASSWORD_LENGTH",
		"HOGWATCH_AUTH_LOCAL_BREAK_GLASS_ADMINS",
		"HOGWATCH_AUTH_OIDC_ISSUER",
		"HOGWATCH_AUTH_OIDC_CLIENT_ID",
		"HOGWATCH_AUTH_OIDC_CLIENT_SECRET",
		"HOGWATCH_AUTH_OIDC_GROUPS_CLAIM",
		"HOGWATCH_AUTH_OIDC_ADMIN_GROUP",
		"HOGWATCH_AUTH_OIDC_AUTOLINK_LOCAL_ADMINS",
		"HOGWATCH_SESSION_IDLE_TIMEOUT",
		"HOGWATCH_SESSION_ABSOLUTE_TIMEOUT",
		"HOGWATCH_SESSION_OIDC_ADMIN_TIMEOUT",
		"HOGWATCH_TRUSTED_PROXIES",
	}
	if got := envNames(); !reflect.DeepEqual(got, want) {
		t.Fatalf("env names drifted:\ngot  %q\nwant %q", got, want)
	}
}

const secret = "s3cret-VALUE"

func TestLoad_SecretFile(t *testing.T) {
	oidc := map[string]string{
		"HOGWATCH_AUTH_OIDC_ISSUER":    "https://idp.example",
		"HOGWATCH_AUTH_OIDC_CLIENT_ID": "id",
		"HOGWATCH_BASE_URL":            "https://hog.example",
	}
	with := func(extra map[string]string) map[string]string {
		m := map[string]string{}
		for k, v := range oidc {
			m[k] = v
		}
		for k, v := range extra {
			m[k] = v
		}
		return m
	}
	t.Run("file", func(t *testing.T) {
		for _, content := range []string{"s3cret\n", "s3cret\r\n", "s3cret"} {
			c, err := Load(src(with(map[string]string{"HOGWATCH_AUTH_OIDC_CLIENT_SECRET_FILE": "/run/s"}),
				map[string]string{"/run/s": content}, 4))
			if err != nil || c.Auth.OIDC.ClientSecret != "s3cret" {
				t.Fatalf("%q: got %q, %v", content, c.Auth.OIDC.ClientSecret, err)
			}
		}
	})
	t.Run("only one trailing newline trimmed", func(t *testing.T) {
		c, err := Load(src(with(map[string]string{"HOGWATCH_AUTH_OIDC_CLIENT_SECRET_FILE": "/run/s"}),
			map[string]string{"/run/s": "a\n\n"}, 4))
		if err != nil || c.Auth.OIDC.ClientSecret != "a\n" {
			t.Fatalf("got %q, %v", c.Auth.OIDC.ClientSecret, err)
		}
	})
	t.Run("both set", func(t *testing.T) {
		_, err := Load(src(with(map[string]string{
			"HOGWATCH_AUTH_OIDC_CLIENT_SECRET":      secret,
			"HOGWATCH_AUTH_OIDC_CLIENT_SECRET_FILE": "/run/s",
		}), map[string]string{"/run/s": "x"}, 4))
		if err == nil {
			t.Fatal("want error")
		}
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("error leaks secret: %v", err)
		}
	})
	t.Run("unreadable", func(t *testing.T) {
		_, err := Load(src(with(map[string]string{"HOGWATCH_AUTH_OIDC_CLIENT_SECRET_FILE": "/run/missing"}), nil, 4))
		if err == nil {
			t.Fatal("want error")
		}
	})
	t.Run("plain env", func(t *testing.T) {
		c, err := Load(src(with(map[string]string{"HOGWATCH_AUTH_OIDC_CLIENT_SECRET": secret}), nil, 4))
		if err != nil || c.Auth.OIDC.ClientSecret != secret {
			t.Fatalf("got %q, %v", c.Auth.OIDC.ClientSecret, err)
		}
	})
}

func TestLoad_ConfigFile(t *testing.T) {
	t.Run("default path missing is fine", func(t *testing.T) {
		if _, err := Load(src(nil, nil, 4)); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("explicit path missing is an error", func(t *testing.T) {
		_, err := Load(src(map[string]string{"HOGWATCH_CONFIG": "/etc/nope.yaml"}, nil, 4))
		if err == nil {
			t.Fatal("want error")
		}
	})
	t.Run("explicit path read", func(t *testing.T) {
		c, err := Load(src(map[string]string{"HOGWATCH_CONFIG": "/etc/h.yaml"},
			map[string]string{"/etc/h.yaml": "data_dir: /srv\n"}, 4))
		if err != nil || c.DataDir != "/srv" {
			t.Fatalf("got %+v, %v", c, err)
		}
	})
	t.Run("unknown key", func(t *testing.T) {
		_, err := Load(src(nil, map[string]string{DefaultConfigPath: "listne: x\n"}, 4))
		if err == nil {
			t.Fatal("want error")
		}
	})
	t.Run("empty file", func(t *testing.T) {
		if _, err := Load(src(nil, map[string]string{DefaultConfigPath: ""}, 4)); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("type error does not echo value", func(t *testing.T) {
		_, err := Load(src(nil, map[string]string{DefaultConfigPath: "scan:\n  workers: " + secret + "\n"}, 4))
		if err == nil {
			t.Fatal("want error")
		}
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("error echoes value: %v", err)
		}
	})
}

func TestLoad_Validation(t *testing.T) {
	oidcOK := map[string]string{
		"HOGWATCH_AUTH_OIDC_ISSUER":        "https://idp",
		"HOGWATCH_AUTH_OIDC_CLIENT_ID":     "id",
		"HOGWATCH_AUTH_OIDC_CLIENT_SECRET": "sec",
		"HOGWATCH_BASE_URL":                "https://hog.example",
	}
	rows := []struct {
		name string
		env  map[string]string
		want string // substring of error; "" means success
	}{
		{"ok oidc", oidcOK, ""},
		{"base_url ok", map[string]string{"HOGWATCH_BASE_URL": "https://h.example/x"}, ""},
		{"base_url relative", map[string]string{"HOGWATCH_BASE_URL": "/x"}, "base_url"},
		{"base_url scheme", map[string]string{"HOGWATCH_BASE_URL": "ftp://h"}, "base_url"},
		{"base_url no host", map[string]string{"HOGWATCH_BASE_URL": "http://"}, "base_url"},
		{"min password", map[string]string{"HOGWATCH_AUTH_LOCAL_MIN_PASSWORD_LENGTH": "7"}, "min_password_length"},
		{"oidc missing id", map[string]string{"HOGWATCH_AUTH_OIDC_ISSUER": "https://idp", "HOGWATCH_AUTH_OIDC_CLIENT_SECRET": "s"}, "client_id"},
		{"oidc missing secret", map[string]string{"HOGWATCH_AUTH_OIDC_ISSUER": "https://idp", "HOGWATCH_AUTH_OIDC_CLIENT_ID": "i"}, "client_secret"},
		{"oidc missing base_url", map[string]string{"HOGWATCH_AUTH_OIDC_ISSUER": "https://idp", "HOGWATCH_AUTH_OIDC_CLIENT_ID": "i", "HOGWATCH_AUTH_OIDC_CLIENT_SECRET": "s"}, "base_url"},
		{"idle zero", map[string]string{"HOGWATCH_SESSION_IDLE_TIMEOUT": "0s"}, "idle_timeout"},
		{"absolute negative", map[string]string{"HOGWATCH_SESSION_ABSOLUTE_TIMEOUT": "-1h"}, "absolute_timeout"},
		{"oidc admin zero", map[string]string{"HOGWATCH_SESSION_OIDC_ADMIN_TIMEOUT": "0s"}, "oidc_admin_timeout"},
		{"idle > absolute", map[string]string{"HOGWATCH_SESSION_IDLE_TIMEOUT": "200h"}, "idle_timeout"},
		{"trusted proxy ok", map[string]string{"HOGWATCH_TRUSTED_PROXIES": "10.0.0.0/8,fd00::/8"}, ""},
		{"trusted proxy bad", map[string]string{"HOGWATCH_TRUSTED_PROXIES": "10.0.0.1"}, "trusted_proxies"},
		{"log level", map[string]string{"HOGWATCH_LOG_LEVEL": "trace"}, "log.level"},
		{"log format", map[string]string{"HOGWATCH_LOG_FORMAT": "xml"}, "log.format"},
		{"bad duration", map[string]string{"HOGWATCH_SESSION_IDLE_TIMEOUT": "soon"}, "HOGWATCH_SESSION_IDLE_TIMEOUT"},
		{"bad int", map[string]string{"HOGWATCH_SCAN_WORKERS": "many"}, "HOGWATCH_SCAN_WORKERS"},
		{"bad bool", map[string]string{"HOGWATCH_AUTH_LOCAL_ENABLED": "maybe"}, "HOGWATCH_AUTH_LOCAL_ENABLED"},
	}
	for _, r := range rows {
		t.Run(r.name, func(t *testing.T) {
			_, err := Load(src(r.env, nil, 8))
			if r.want == "" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), r.want) {
				t.Fatalf("got %v, want substring %q", err, r.want)
			}
			// An unparsable value must not be echoed.
			for _, v := range r.env {
				if v == "many" || v == "soon" || v == "maybe" {
					if strings.Contains(err.Error(), v) {
						t.Fatalf("error echoes value %q: %v", v, err)
					}
				}
			}
		})
	}
	t.Run("listen empty via file", func(t *testing.T) {
		_, err := Load(src(nil, map[string]string{DefaultConfigPath: "listen: \"\"\n"}, 4))
		if err == nil || !strings.Contains(err.Error(), "listen") {
			t.Fatalf("got %v", err)
		}
	})
}

func TestWorkers(t *testing.T) {
	rows := []struct{ g, cap, scan, listing int }{
		{1, 1, 1, 1}, {2, 1, 1, 1}, {4, 2, 2, 2}, {8, 5, 4, 2}, {16, 10, 8, 2},
	}
	for _, r := range rows {
		if got := WorkerCap(r.g); got != r.cap {
			t.Errorf("WorkerCap(%d) = %d, want %d", r.g, got, r.cap)
		}
		c, err := Load(src(nil, nil, r.g))
		if err != nil {
			t.Fatal(err)
		}
		if c.Scan.Workers != r.scan || c.Scan.ListingWorkers != r.listing {
			t.Errorf("g=%d: got %d,%d want %d,%d", r.g, c.Scan.Workers, c.Scan.ListingWorkers, r.scan, r.listing)
		}
	}
	t.Run("over cap", func(t *testing.T) {
		_, err := Load(src(map[string]string{"HOGWATCH_SCAN_WORKERS": "6"}, nil, 8))
		if err == nil || !strings.Contains(err.Error(), "5") {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("listing over cap", func(t *testing.T) {
		_, err := Load(src(map[string]string{"HOGWATCH_SCAN_LISTING_WORKERS": "6"}, nil, 8))
		if err == nil || !strings.Contains(err.Error(), "5") {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("at cap", func(t *testing.T) {
		c, err := Load(src(map[string]string{"HOGWATCH_SCAN_WORKERS": "5"}, nil, 8))
		if err != nil || c.Scan.Workers != 5 {
			t.Fatalf("got %+v, %v", c.Scan, err)
		}
	})
	t.Run("zero means derived", func(t *testing.T) {
		c, err := Load(src(map[string]string{"HOGWATCH_SCAN_WORKERS": "0"}, nil, 8))
		if err != nil || c.Scan.Workers != 4 {
			t.Fatalf("got %+v, %v", c.Scan, err)
		}
	})
	t.Run("negative", func(t *testing.T) {
		if _, err := Load(src(map[string]string{"HOGWATCH_SCAN_WORKERS": "-1"}, nil, 8)); err == nil {
			t.Fatal("want error")
		}
	})
}

func TestConfig_LogValueRedacts(t *testing.T) {
	c, err := Load(src(map[string]string{
		"HOGWATCH_AUTH_OIDC_ISSUER":        "https://idp",
		"HOGWATCH_AUTH_OIDC_CLIENT_ID":     "id",
		"HOGWATCH_AUTH_OIDC_CLIENT_SECRET": secret,
		"HOGWATCH_BASE_URL":                "https://hog.example",
	}, nil, 4))
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	slog.New(slog.NewJSONHandler(&buf, nil)).Info("config loaded", "cfg", c)
	out := buf.String()
	if strings.Contains(out, secret) {
		t.Fatalf("log leaks secret: %s", out)
	}
	if !strings.Contains(out, `"client_secret":"[redacted]"`) {
		t.Fatalf("missing redaction marker: %s", out)
	}

	// No Load error may contain the secret, whichever rule fails.
	bad := []map[string]string{
		{"HOGWATCH_AUTH_OIDC_CLIENT_SECRET": secret, "HOGWATCH_AUTH_OIDC_ISSUER": "https://idp"}, // missing client_id
		{"HOGWATCH_AUTH_OIDC_CLIENT_SECRET": secret, "HOGWATCH_LOG_LEVEL": "trace"},
		{"HOGWATCH_AUTH_OIDC_CLIENT_SECRET": secret, "HOGWATCH_SCAN_WORKERS": "x"},
		{"HOGWATCH_AUTH_OIDC_CLIENT_SECRET": secret, "HOGWATCH_AUTH_OIDC_CLIENT_SECRET_FILE": "/f"},
	}
	for _, env := range bad {
		_, err := Load(src(env, map[string]string{"/f": secret}, 4))
		if err == nil {
			t.Fatalf("want error for %v", env)
		}
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("error leaks secret: %v", err)
		}
	}
	var _ = errors.New
}

func TestNewLogger(t *testing.T) {
	var buf bytes.Buffer
	l := NewLogger(Log{Level: "warn", Format: "json"}, &buf)
	l.Info("hidden")
	l.Warn("shown")
	if strings.Contains(buf.String(), "hidden") || !strings.Contains(buf.String(), `"msg":"shown"`) {
		t.Fatalf("got %s", buf.String())
	}
}

func TestLoad_DataDir(t *testing.T) {
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	c, err := Load(src(map[string]string{"HOGWATCH_DATA_DIR": "data"}, nil, 4))
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(wd, "data"); c.DataDir != want {
		t.Fatalf("DataDir = %q, want %q", c.DataDir, want)
	}
	c, err = Load(src(map[string]string{"HOGWATCH_DATA_DIR": "/srv/./hw/"}, nil, 4))
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Separator == '/' && c.DataDir != "/srv/hw" {
		t.Fatalf("DataDir = %q, want /srv/hw", c.DataDir)
	}
	_, err = Load(src(nil, map[string]string{DefaultConfigPath: "data_dir: \"\"\n"}, 4))
	if err == nil || !strings.Contains(err.Error(), "data_dir") {
		t.Fatalf("empty data_dir: got %v, want a data_dir error", err)
	}
}

func TestConfig_ScanServiceKeys(t *testing.T) {
	c, err := Load(src(nil, nil, 8))
	if err != nil {
		t.Fatal(err)
	}
	if c.Scan.HostPrefix != "/host" || c.Scan.ZFSFile != "/zfs/zfs.tsv" ||
		c.Scan.GenerationGrace != 5*time.Minute || c.Scan.AllowWritableMounts {
		t.Fatalf("defaults wrong: %+v", c.Scan)
	}

	c, err = Load(src(map[string]string{
		"HOGWATCH_SCAN_HOST_PREFIX":           "/mnt/h",
		"HOGWATCH_SCAN_ZFS_FILE":              "/z/x.tsv",
		"HOGWATCH_SCAN_GENERATION_GRACE":      "90s",
		"HOGWATCH_SCAN_ALLOW_WRITABLE_MOUNTS": "true",
	}, nil, 8))
	if err != nil {
		t.Fatal(err)
	}
	if c.Scan.HostPrefix != "/mnt/h" || c.Scan.ZFSFile != "/z/x.tsv" ||
		c.Scan.GenerationGrace != 90*time.Second || !c.Scan.AllowWritableMounts {
		t.Fatalf("env overrides not applied: %+v", c.Scan)
	}

	for _, v := range []string{"0s", "-1m"} {
		if _, err := Load(src(map[string]string{"HOGWATCH_SCAN_GENERATION_GRACE": v}, nil, 8)); err == nil {
			t.Errorf("generation_grace %q accepted", v)
		}
	}
	file := "scan:\n  generation_grace: 0\n"
	if _, err := Load(src(map[string]string{"HOGWATCH_CONFIG": "/c.yaml"}, map[string]string{"/c.yaml": file}, 8)); err == nil {
		t.Error("yaml generation_grace: 0 accepted")
	}
}

func TestValidate_OIDCNeedsBaseURL(t *testing.T) {
	env := map[string]string{
		"HOGWATCH_AUTH_OIDC_ISSUER":        "https://idp.example",
		"HOGWATCH_AUTH_OIDC_CLIENT_ID":     "id",
		"HOGWATCH_AUTH_OIDC_CLIENT_SECRET": "s",
	}
	if _, err := Load(src(env, nil, 4)); err == nil || !strings.Contains(err.Error(), "base_url") {
		t.Fatalf("issuer without base_url: err = %v", err)
	}
	env["HOGWATCH_BASE_URL"] = "https://hog.example"
	if _, err := Load(src(env, nil, 4)); err != nil {
		t.Fatalf("issuer with base_url: %v", err)
	}
}

func TestOIDCRedirectURL(t *testing.T) {
	for base, want := range map[string]string{
		"https://hog.example":       "https://hog.example/api/v1/auth/oidc/callback",
		"https://hog.example/":      "https://hog.example/api/v1/auth/oidc/callback",
		"https://hog.example/app//": "https://hog.example/app/api/v1/auth/oidc/callback",
	} {
		if got := (Config{BaseURL: base}).OIDCRedirectURL(); got != want {
			t.Errorf("%q: got %q, want %q", base, got, want)
		}
	}
}
