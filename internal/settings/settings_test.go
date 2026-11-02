package settings

import (
	"strconv"
	"testing"

	"github.com/vinRosso/hogwatch/internal/scanner/agg"
)

func TestDefs(t *testing.T) {
	l := agg.DefaultLimits()
	want := map[string]string{
		UsersSeeMountCapacity: "true", IncludeSnapshots: "false",
		TopN: strconv.Itoa(l.TopN), TopMaxDepth: strconv.FormatInt(l.TopMaxDepth, 10),
		TopMinBytes: strconv.FormatInt(l.TopMinAlloc, 10),
	}
	if len(Defs) != len(want) {
		t.Fatalf("%d defs, want %d", len(Defs), len(want))
	}
	for _, d := range Defs {
		if d.Default != want[d.Key] {
			t.Errorf("%s default = %q, want %q", d.Key, d.Default, want[d.Key])
		}
		if err := d.Validate(d.Default); err != nil {
			t.Errorf("%s default is invalid: %v", d.Key, err)
		}
	}
}

func TestValidate(t *testing.T) {
	cases := []struct {
		key, v string
		ok     bool
	}{
		{IncludeSnapshots, "true", true}, {IncludeSnapshots, "false", true},
		{IncludeSnapshots, "yes", false}, {IncludeSnapshots, "1", false}, {IncludeSnapshots, " true", false}, {IncludeSnapshots, "", false},
		{TopN, "1", true}, {TopN, "0", false}, {TopN, "-1", false}, {TopN, "+5", false}, {TopN, "05", false},
		{TopN, "1048576", true}, {TopN, "1048577", false}, {TopN, " 5", false}, {TopN, "1e3", false},
		{TopMaxDepth, "0", true}, {TopMaxDepth, "-1", false},
		{TopMinBytes, "0", true}, {TopMinBytes, "9223372036854775807", true}, {TopMinBytes, "9223372036854775808", false},
	}
	for _, c := range cases {
		d, ok := Lookup(c.key)
		if !ok {
			t.Fatalf("no def for %s", c.key)
		}
		if err := d.Validate(c.v); (err == nil) != c.ok {
			t.Errorf("%s=%q: err = %v, want ok=%v", c.key, c.v, err, c.ok)
		}
	}
	if _, ok := Lookup("bogus"); ok {
		t.Error("Lookup found an unknown key")
	}
}

func TestEffective(t *testing.T) {
	got := Effective(map[string]string{TopN: "7", TopMaxDepth: "junk", "bogus": "1", IncludeSnapshots: "true"})
	if got[TopN] != "7" || got[IncludeSnapshots] != "true" {
		t.Errorf("stored values lost: %v", got)
	}
	if got[TopMaxDepth] != "5" {
		t.Errorf("invalid stored value must give the default, got %q", got[TopMaxDepth])
	}
	if _, ok := got["bogus"]; ok || len(got) != len(Defs) {
		t.Errorf("unknown key leaked or key missing: %v", got)
	}
}
