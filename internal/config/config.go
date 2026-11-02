// Package config loads Hogwatch settings with the precedence environment
// variables > YAML file > defaults and validates them.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/netip"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// DefaultConfigPath is read when HOGWATCH_CONFIG is unset. It may be absent.
const DefaultConfigPath = "/config/hogwatch.yaml"

// Config is the full process configuration. The yaml tags define both the
// file keys and, through reflection, the environment variable names.
type Config struct {
	Listen  string  `yaml:"listen"`
	BaseURL string  `yaml:"base_url"`
	DataDir string  `yaml:"data_dir"`
	Log     Log     `yaml:"log"`
	Scan    Scan    `yaml:"scan"`
	Auth    Auth    `yaml:"auth"`
	Session Session `yaml:"session"`
	// TrustedProxies are the CIDRs of reverse proxies whose X-Forwarded-For is believed.
	TrustedProxies []string `yaml:"trusted_proxies"`
}

// Log configures logging.
type Log struct {
	Level  string `yaml:"level"`  // debug|info|warn|error
	Format string `yaml:"format"` // text|json
}

// Scan configures the scanner pools. After Load both hold resolved values.
type Scan struct {
	Workers        int `yaml:"workers"`
	ListingWorkers int `yaml:"listing_workers"`
	// HostPrefix is where the host filesystem is mounted in the scanner container.
	HostPrefix string `yaml:"host_prefix"`
	// ZFSFile is the zfs.tsv written by the hogwatch-zfs sidecar.
	ZFSFile string `yaml:"zfs_file"`
	// GenerationGrace is how long a superseded generation file is kept.
	GenerationGrace time.Duration `yaml:"generation_grace"`
	// AllowWritableMounts lets the scanner enter writable filesystems (invariant 5).
	AllowWritableMounts bool `yaml:"allow_writable_mounts"`
}

// Auth groups authentication settings.
type Auth struct {
	Local AuthLocal `yaml:"local"`
	OIDC  AuthOIDC  `yaml:"oidc"`
}

// AuthLocal configures local accounts.
type AuthLocal struct {
	Enabled          bool     `yaml:"enabled"`
	MinPasswordLen   int      `yaml:"min_password_length"`
	BreakGlassAdmins []string `yaml:"break_glass_admins"`
}

// AuthOIDC configures OpenID Connect.
type AuthOIDC struct {
	Issuer              string `yaml:"issuer"`
	ClientID            string `yaml:"client_id"`
	ClientSecret        string `yaml:"client_secret" hogwatch:"secret"`
	GroupsClaim         string `yaml:"groups_claim"`
	AdminGroup          string `yaml:"admin_group"`
	AutolinkLocalAdmins bool   `yaml:"autolink_local_admins"`
}

// Session holds session lifetimes.
type Session struct {
	IdleTimeout      time.Duration `yaml:"idle_timeout"`
	AbsoluteTimeout  time.Duration `yaml:"absolute_timeout"`
	OIDCAdminTimeout time.Duration `yaml:"oidc_admin_timeout"`
}

// Source abstracts the process environment so tests never touch it.
type Source struct {
	LookupEnv  func(string) (string, bool)
	ReadFile   func(string) ([]byte, error)
	GOMAXPROCS int
}

// WorkerCap is the largest permitted worker setting for a GOMAXPROCS value:
// max(1, floor(2*g/3)).
func WorkerCap(gomaxprocs int) int {
	if gomaxprocs < 1 {
		gomaxprocs = 1
	}
	return max(1, 2*gomaxprocs/3)
}

func defaultScanWorkers(g int) int { return max(1, g/2) }

func defaultListingWorkers(g int) int {
	if g < 4 {
		return 1
	}
	return 2
}

func defaults() Config {
	return Config{
		Listen:  ":8080",
		DataDir: "/data",
		Log:     Log{Level: "info", Format: "text"},
		Scan:    Scan{HostPrefix: "/host", ZFSFile: "/zfs/zfs.tsv", GenerationGrace: 5 * time.Minute},
		Auth: Auth{
			Local: AuthLocal{Enabled: true, MinPasswordLen: 8, BreakGlassAdmins: []string{}},
			OIDC:  AuthOIDC{GroupsClaim: "groups"},
		},
		Session: Session{
			IdleTimeout:      12 * time.Hour,
			AbsoluteTimeout:  168 * time.Hour,
			OIDCAdminTimeout: 12 * time.Hour,
		},
		TrustedProxies: []string{},
	}
}

// Load builds the configuration: defaults, then the YAML file, then the
// environment. DataDir comes back absolute and cleaned. No error message
// contains a secret value.
func Load(src Source) (Config, error) {
	g := max(src.GOMAXPROCS, 1)
	cfg := defaults()

	path, explicit := DefaultConfigPath, false
	if v, ok := src.LookupEnv("HOGWATCH_CONFIG"); ok && v != "" {
		path, explicit = v, true
	}
	data, err := src.ReadFile(path)
	switch {
	case err == nil:
		if err := decodeYAML(data, &cfg); err != nil {
			return Config{}, fmt.Errorf("config file %s: %w", path, err)
		}
	case errors.Is(err, fs.ErrNotExist) && !explicit:
		// The default file is optional.
	default:
		return Config{}, fmt.Errorf("config file %s: %w", path, err)
	}

	if err := overlayEnv(&cfg, src); err != nil {
		return Config{}, err
	}
	if err := resolveWorkers(&cfg, g); err != nil {
		return Config{}, err
	}
	if err := cfg.validate(); err != nil {
		return Config{}, err
	}
	// SQLite file: URIs need an absolute path (db.FileURI).
	abs, err := filepath.Abs(cfg.DataDir)
	if err != nil {
		return Config{}, fmt.Errorf("data_dir: %w", err)
	}
	cfg.DataDir = abs
	return cfg, nil
}

var quoted = regexp.MustCompile("`[^`]*`")

// decodeYAML decodes strictly. The decoder's type errors quote the offending
// value, so those quotes are stripped to keep secrets out of messages.
func decodeYAML(data []byte, cfg *Config) error {
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	err := dec.Decode(cfg)
	if err == nil || errors.Is(err, io.EOF) { // io.EOF: empty file
		return nil
	}
	var te *yaml.TypeError
	if errors.As(err, &te) {
		msgs := make([]string, len(te.Errors))
		for i, m := range te.Errors {
			msgs[i] = quoted.ReplaceAllString(m, "`...`")
		}
		return errors.New(strings.Join(msgs, "; "))
	}
	return errors.New(quoted.ReplaceAllString(err.Error(), "`...`"))
}

func resolveWorkers(cfg *Config, g int) error {
	cp := WorkerCap(g)
	resolve := func(name string, v *int, def int) error {
		switch {
		case *v == 0:
			*v = def
		case *v < 0:
			return fmt.Errorf("%s must not be negative", name)
		}
		if *v > cp {
			return fmt.Errorf("%s=%d exceeds the maximum %d for GOMAXPROCS=%d", name, *v, cp, g)
		}
		return nil
	}
	if err := resolve("scan.workers", &cfg.Scan.Workers, defaultScanWorkers(g)); err != nil {
		return err
	}
	return resolve("scan.listing_workers", &cfg.Scan.ListingWorkers, defaultListingWorkers(g))
}

// OIDCRedirectURL is the callback the identity provider sends the browser to.
func (c Config) OIDCRedirectURL() string {
	return strings.TrimRight(c.BaseURL, "/") + "/api/v1/auth/oidc/callback"
}

func (c Config) validate() error {
	if c.Listen == "" {
		return errors.New("listen must not be empty")
	}
	if c.DataDir == "" {
		return errors.New("data_dir must not be empty")
	}
	if c.BaseURL != "" {
		u, err := url.Parse(c.BaseURL)
		if err != nil || !u.IsAbs() || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
			return errors.New("base_url must be an absolute http or https URL")
		}
	}
	for _, p := range c.TrustedProxies {
		if _, err := netip.ParsePrefix(p); err != nil {
			return fmt.Errorf("trusted_proxies: %q is not a CIDR", p)
		}
	}
	if c.Auth.Local.MinPasswordLen < 8 {
		return errors.New("auth.local.min_password_length must be at least 8")
	}
	if c.Auth.OIDC.Issuer != "" {
		if c.Auth.OIDC.ClientID == "" {
			return errors.New("auth.oidc.issuer requires auth.oidc.client_id")
		}
		if c.Auth.OIDC.ClientSecret == "" {
			return errors.New("auth.oidc.issuer requires auth.oidc.client_secret")
		}
		if c.BaseURL == "" {
			return errors.New("auth.oidc.issuer requires base_url (the redirect URI is built from it)")
		}
	}
	for _, d := range []struct {
		name string
		v    time.Duration
	}{
		{"session.idle_timeout", c.Session.IdleTimeout},
		{"session.absolute_timeout", c.Session.AbsoluteTimeout},
		{"session.oidc_admin_timeout", c.Session.OIDCAdminTimeout},
	} {
		if d.v <= 0 {
			return fmt.Errorf("%s must be greater than zero", d.name)
		}
	}
	if c.Session.IdleTimeout > c.Session.AbsoluteTimeout {
		return errors.New("session.idle_timeout must not exceed session.absolute_timeout")
	}
	if c.Scan.GenerationGrace <= 0 {
		return errors.New("scan.generation_grace must be greater than zero")
	}
	switch c.Log.Level {
	case "debug", "info", "warn", "error":
	default:
		return errors.New("log.level must be one of debug, info, warn, error")
	}
	switch c.Log.Format {
	case "text", "json":
	default:
		return errors.New("log.format must be one of text, json")
	}
	return nil
}
