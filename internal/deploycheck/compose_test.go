package deploycheck

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// volume is one entry of a service's volumes, in short or long syntax.
type volume struct {
	Source, Target string
	ReadOnly       bool
	Propagation    string
	CreateHostPath bool // true when the file does not say false (the compose default)
}

// splitShortVolume splits src:dst[:opts]; a source written as ${VAR:-default} keeps its colon.
func splitShortVolume(s string) []string {
	if strings.HasPrefix(s, "${") {
		if i := strings.Index(s, "}"); i > 0 && strings.HasPrefix(s[i+1:], ":") {
			return append([]string{s[:i+1]}, strings.Split(s[i+2:], ":")...)
		}
	}
	return strings.Split(s, ":")
}

func (v *volume) UnmarshalYAML(n *yaml.Node) error {
	v.CreateHostPath = true
	if n.Kind == yaml.ScalarNode { // src:dst[:opts]
		parts := splitShortVolume(n.Value)
		if len(parts) < 2 {
			return fmt.Errorf("volume %q is not src:dst", n.Value)
		}
		v.Source, v.Target = parts[0], parts[1]
		if len(parts) > 2 {
			for _, o := range strings.Split(parts[2], ",") {
				switch o {
				case "ro":
					v.ReadOnly = true
				case "rslave", "rshared", "slave", "shared", "private", "rprivate":
					v.Propagation = o
				}
			}
		}
		return nil
	}
	var long struct {
		Type     string `yaml:"type"`
		Source   string `yaml:"source"`
		Target   string `yaml:"target"`
		ReadOnly bool   `yaml:"read_only"`
		Bind     struct {
			Propagation    string `yaml:"propagation"`
			CreateHostPath *bool  `yaml:"create_host_path"`
		} `yaml:"bind"`
	}
	if err := n.Decode(&long); err != nil {
		return err
	}
	v.Source, v.Target, v.ReadOnly, v.Propagation = long.Source, long.Target, long.ReadOnly, long.Bind.Propagation
	if long.Bind.CreateHostPath != nil {
		v.CreateHostPath = *long.Bind.CreateHostPath
	}
	return nil
}

// The state folders default to ./data and ./zfs and can be moved with DATA_PATH and ZFS_PATH in .env.
const (
	dataSrc = "${DATA_PATH:-./data}"
	zfsSrc  = "${ZFS_PATH:-./zfs}"
)

// service lists every key compose.yaml may use. loadCompose decodes strictly,
// so a key that is not here (pid, ipc, userns_mode, volumes_from, sysctls,
// group_add, device_cgroup_rules, ...) fails the test until someone
// decides it is safe and adds a rule for it.
type service struct {
	Image         string   `yaml:"image"`
	Command       string   `yaml:"command"`
	EnvFile       any      `yaml:"env_file"`
	Restart       string   `yaml:"restart"`
	Ports         []string `yaml:"ports"`
	ContainerName string   `yaml:"container_name"`
	ReadOnly      bool     `yaml:"read_only"`
	Privileged    bool     `yaml:"privileged"`
	CapAdd        []string `yaml:"cap_add"`
	CapDrop       []string `yaml:"cap_drop"`
	SecurityOpt   []string `yaml:"security_opt"`
	NetworkMode   string   `yaml:"network_mode"`
	User          string   `yaml:"user"`
	Devices       []string `yaml:"devices"`
	Profiles      []string `yaml:"profiles"`
	Volumes       []volume `yaml:"volumes"`
	Tmpfs         []string `yaml:"tmpfs"`
	DependsOn     map[string]struct {
		Condition string `yaml:"condition"`
	} `yaml:"depends_on"`
}

type compose struct {
	XBase    any                `yaml:"x-base"`
	Name     string             `yaml:"name"`
	Services map[string]service `yaml:"services"`
}

// scanSource is the one host path the scanner may mount, left for the user.
const scanSource = "${SCAN_PATH:?set SCAN_PATH in .env}"

const composePath = "../../compose.yaml"

func loadCompose(t *testing.T) compose {
	t.Helper()
	data, err := os.ReadFile(composePath)
	if err != nil {
		t.Fatal(err)
	}
	var c compose
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&c); err != nil {
		t.Fatal(err)
	}
	return c
}

// hasZFSDevice sees /dev/zfs in any devices syntax, such as /dev/zfs:/dev/zfs:rwm.
func hasZFSDevice(devs []string) bool {
	for _, d := range devs {
		if strings.HasPrefix(strings.SplitN(d, ":", 2)[0], "/dev/zfs") {
			return true
		}
	}
	return false
}

func isHostBind(v volume) bool { return strings.HasPrefix(v.Target, "/host") }

func sameSet(a, b []string) bool {
	a, b = slices.Clone(a), slices.Clone(b)
	slices.Sort(a)
	slices.Sort(b)
	return slices.Equal(a, b)
}

// violations lists every way c breaks the container privilege rules.
func violations(c compose) []string {
	var out []string
	bad := func(format string, args ...any) { out = append(out, fmt.Sprintf(format, args...)) }

	if c.Name != "hogwatch" {
		bad("project name is %q, want hogwatch", c.Name)
	}
	for name, s := range c.Services {
		if !strings.HasPrefix(name, "hogwatch-") || s.ContainerName != name {
			bad("%s: service and container_name must be the same hogwatch- name", name)
		}
		if !s.ReadOnly {
			bad("%s: root filesystem must be read_only", name)
		}
		if !slices.Contains(s.CapDrop, "ALL") {
			bad("%s: must cap_drop ALL", name)
		}
		if !slices.Equal(s.SecurityOpt, []string{"no-new-privileges:true"}) {
			bad("%s: security_opt must be exactly no-new-privileges:true, got %v", name, s.SecurityOpt)
		}
		if s.Privileged || s.NetworkMode == "host" {
			bad("%s: must not be privileged or use host networking", name)
		}
		if hasZFSDevice(s.Devices) && name != "hogwatch-zfs" { // invariant 6
			bad("%s: only hogwatch-zfs may get /dev/zfs", name)
		}
		if name != "hogwatch-init" && s.DependsOn["hogwatch-init"].Condition != "service_completed_successfully" {
			bad("%s: must wait for hogwatch-init to complete", name)
		}
	}

	if s, ok := c.Services["hogwatch-scanner"]; !ok {
		bad("hogwatch-scanner is missing")
	} else { // invariant 5
		if s.User != "0:10001" {
			bad("scanner: user %q, want 0:10001", s.User)
		}
		if !sameSet(s.CapAdd, []string{"DAC_READ_SEARCH"}) {
			bad("scanner: cap_add %v, want only DAC_READ_SEARCH", s.CapAdd)
		}
		if s.NetworkMode != "none" {
			bad("scanner: network_mode %q, want none", s.NetworkMode)
		}
		hostBinds := 0
		for _, v := range s.Volumes {
			switch {
			case isHostBind(v):
				hostBinds++
				if v.Source != scanSource || !v.ReadOnly || v.Propagation != "rslave" || v.CreateHostPath {
					bad("scanner: host bind %s must come from SCAN_PATH, read-only, rslave and create_host_path: false", v.Target)
				}
			case v.Target == "/data" && v.Source == dataSrc:
			case v.Target == "/zfs" && v.Source == zfsSrc && v.ReadOnly:
			default:
				bad("scanner: unexpected volume %s:%s", v.Source, v.Target)
			}
		}
		if hostBinds == 0 {
			bad("scanner: no host bind to scan")
		}
	}

	if s, ok := c.Services["hogwatch-web"]; !ok { // invariant 1
		bad("hogwatch-web is missing")
	} else {
		for _, p := range s.Ports {
			if !strings.HasPrefix(p, "127.0.0.1:") {
				bad("web: port %q must be published on 127.0.0.1 only", p)
			}
		}
		if len(s.CapAdd) != 0 || (s.User != "" && s.User != "10001:10001") {
			bad("web: must run as the image user without added capabilities")
		}
		if len(s.Volumes) != 1 || s.Volumes[0].Target != "/data" || s.Volumes[0].Source != dataSrc || s.Volumes[0].ReadOnly {
			bad("web: /data must be its only mount, got %v", s.Volumes)
		}
		if !slices.Equal(s.Tmpfs, []string{"/tmp:size=512m"}) {
			bad("web: tmpfs %v, want only a bounded /tmp for SQLite sorts", s.Tmpfs)
		}
	}

	if s, ok := c.Services["hogwatch-init"]; !ok {
		bad("hogwatch-init is missing")
	} else {
		if !sameSet(s.CapAdd, []string{"CHOWN", "FOWNER"}) {
			bad("init: cap_add %v, want CHOWN and FOWNER", s.CapAdd)
		}
		if s.User != "0:10001" { // a root process outside the data group has its setgid bit cleared
			bad("init: user %q, want 0:10001", s.User)
		}
		if s.NetworkMode != "none" {
			bad("init: network_mode %q, want none", s.NetworkMode)
		}
		for _, v := range s.Volumes {
			if (v.Target != "/data" || v.Source != dataSrc) && (v.Target != "/zfs" || v.Source != zfsSrc) {
				bad("init: unexpected volume %s:%s", v.Source, v.Target)
			}
		}
	}

	if s, ok := c.Services["hogwatch-zfs"]; ok {
		if !slices.Equal(s.Profiles, []string{"zfs"}) {
			bad("zfs: must be in the zfs profile only")
		}
		if !slices.Equal(s.Devices, []string{"/dev/zfs"}) || s.NetworkMode != "none" {
			bad("zfs: needs /dev/zfs and no network")
		}
		if len(s.Volumes) != 1 || s.Volumes[0].Source != zfsSrc || s.Volumes[0].Target != "/out" {
			bad("zfs: only the zfs folder at /out may be mounted, got %v", s.Volumes)
		}
		if len(s.CapAdd) != 0 {
			bad("zfs: must not add capabilities")
		}
	} else {
		bad("hogwatch-zfs is missing")
	}
	return out
}

func TestCompose_Invariants(t *testing.T) {
	if v := violations(loadCompose(t)); len(v) != 0 {
		t.Fatalf("compose.yaml breaks the rules:\n  %s", strings.Join(v, "\n  "))
	}
}

func edit(c *compose, name string, f func(*service)) {
	s := c.Services[name]
	f(&s)
	c.Services[name] = s
}

func mapHostBinds(s *service, f func(*volume)) {
	s.Volumes = slices.Clone(s.Volumes)
	for i := range s.Volumes {
		if isHostBind(s.Volumes[i]) {
			f(&s.Volumes[i])
		}
	}
}

// TestCompose_NegativeControls proves each rule can fail: a mutated copy of
// the real file must be reported, with the expected message.
func TestCompose_NegativeControls(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*compose)
		want   string
	}{
		{"zfs device on the scanner", func(c *compose) { edit(c, "hogwatch-scanner", func(s *service) { s.Devices = []string{"/dev/zfs"} }) }, "only hogwatch-zfs may get /dev/zfs"},
		{"zfs device on the web", func(c *compose) { edit(c, "hogwatch-web", func(s *service) { s.Devices = []string{"/dev/zfs"} }) }, "only hogwatch-zfs may get /dev/zfs"},
		{"extra capability", func(c *compose) {
			edit(c, "hogwatch-scanner", func(s *service) { s.CapAdd = append(slices.Clone(s.CapAdd), "NET_ADMIN") })
		}, "scanner: cap_add"},
		{"scanner with network", func(c *compose) { edit(c, "hogwatch-scanner", func(s *service) { s.NetworkMode = "bridge" }) }, "network_mode"},
		{"writable host bind", func(c *compose) {
			edit(c, "hogwatch-scanner", func(s *service) { mapHostBinds(s, func(v *volume) { v.ReadOnly = false }) })
		}, "must come from SCAN_PATH"},
		{"host path created when missing", func(c *compose) {
			edit(c, "hogwatch-scanner", func(s *service) { mapHostBinds(s, func(v *volume) { v.CreateHostPath = true }) })
		}, "create_host_path"},
		{"scan path in the web", func(c *compose) {
			edit(c, "hogwatch-web", func(s *service) {
				s.Volumes = append(slices.Clone(s.Volumes), volume{Source: "/storage", Target: "/host/data", ReadOnly: true})
			})
		}, "only mount"},
		{"init outside the data group", func(c *compose) { edit(c, "hogwatch-init", func(s *service) { s.User = "0:0" }) }, "init: user"},
		{"web without a tmp directory", func(c *compose) { edit(c, "hogwatch-web", func(s *service) { s.Tmpfs = nil }) }, "web: tmpfs"},
		{"scan path in init", func(c *compose) {
			edit(c, "hogwatch-init", func(s *service) {
				s.Volumes = append(slices.Clone(s.Volumes), volume{Source: "/storage", Target: "/host/data"})
			})
		}, "init: unexpected volume"},
		{"writable root filesystem", func(c *compose) { edit(c, "hogwatch-scanner", func(s *service) { s.ReadOnly = false }) }, "read_only"},
		{"init capabilities", func(c *compose) {
			edit(c, "hogwatch-init", func(s *service) { s.CapAdd = []string{"CHOWN", "SYS_ADMIN"} })
		}, "init: cap_add"},
		{"web does not wait for init", func(c *compose) { edit(c, "hogwatch-web", func(s *service) { s.DependsOn = nil }) }, "wait for hogwatch-init"},
		{"docker socket scanned", func(c *compose) {
			edit(c, "hogwatch-scanner", func(s *service) { mapHostBinds(s, func(v *volume) { v.Source = "/var/run/docker.sock" }) })
		}, "must come from SCAN_PATH"},
		{"host root in the web", func(c *compose) {
			edit(c, "hogwatch-web", func(s *service) { s.Volumes = []volume{{Source: "/", Target: "/data"}} })
		}, "only mount"},
		{"host root in init", func(c *compose) {
			edit(c, "hogwatch-init", func(s *service) { s.Volumes = []volume{{Source: "/", Target: "/data"}} })
		}, "init: unexpected volume"},
		{"zfs device with options", func(c *compose) {
			edit(c, "hogwatch-scanner", func(s *service) { s.Devices = []string{"/dev/zfs:/dev/zfs:rwm"} })
		}, "only hogwatch-zfs may get /dev/zfs"},
		{"seccomp off", func(c *compose) {
			edit(c, "hogwatch-web", func(s *service) { s.SecurityOpt = append(slices.Clone(s.SecurityOpt), "seccomp:unconfined") })
		}, "security_opt"},
		{"web on all interfaces", func(c *compose) { edit(c, "hogwatch-web", func(s *service) { s.Ports = []string{"8080:8080"} }) }, "127.0.0.1 only"},
		{"unprefixed service", func(c *compose) { c.Services["web"] = c.Services["hogwatch-web"]; delete(c.Services, "hogwatch-web") }, "hogwatch- name"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := loadCompose(t)
			tc.mutate(&c)
			v := strings.Join(violations(c), "\n")
			if !strings.Contains(v, tc.want) {
				t.Errorf("violations = %q, want one containing %q", v, tc.want)
			}
		})
	}
}

func TestCompose_Slim(t *testing.T) {
	data, err := os.ReadFile(composePath)
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(string(data), "\n"); n > 60 {
		t.Errorf("compose.yaml has %d lines; keep it under 60", n)
	}
	f, err := os.Open("../../.env.example")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	var keys []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line != "" && !strings.HasPrefix(line, "#") {
			keys = append(keys, strings.SplitN(line, "=", 2)[0])
		}
	}
	if !slices.Equal(keys, []string{"SCAN_PATH"}) {
		t.Errorf(".env.example sets %v, want only SCAN_PATH", keys)
	}
}

// TestDockerfile_VersionsMatch keeps the build ARG defaults equal to the
// single source of truth, scripts/versions.env.
func TestDockerfile_VersionsMatch(t *testing.T) {
	env := map[string]string{}
	data, err := os.ReadFile(filepath.Join("..", "..", "scripts", "versions.env"))
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(data), "\n") {
		if k, v, ok := strings.Cut(line, "="); ok && !strings.HasPrefix(line, "#") {
			env[k] = v
		}
	}
	df, err := os.ReadFile(filepath.Join("..", "..", "deploy", "Dockerfile"))
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"NODE_VERSION", "GO_VERSION"} {
		want := "ARG " + k + "=" + env[k] + "\n"
		if env[k] == "" || !strings.Contains(string(df), want) {
			t.Errorf("deploy/Dockerfile lacks %q (versions.env says %q)", strings.TrimSpace(want), env[k])
		}
	}
}

// TestCompose_RejectsUnknownKeys proves the strict decode: a namespace or
// volume-sharing key must fail the load, not be ignored.
func TestCompose_RejectsUnknownKeys(t *testing.T) {
	data, err := os.ReadFile(composePath)
	if err != nil {
		t.Fatal(err)
	}
	bad := strings.Replace(string(data), "    command: scan\n", "    command: scan\n    pid: host\n", 1)
	if bad == string(data) {
		t.Fatal("test fixture did not change")
	}
	var c compose
	dec := yaml.NewDecoder(strings.NewReader(bad))
	dec.KnownFields(true)
	if err := dec.Decode(&c); err == nil {
		t.Fatal("a service with pid: host decoded without error")
	}
}
