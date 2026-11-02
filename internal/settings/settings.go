// Package settings is the registry of runtime settings an admin may change.
// A setting is stored as text in the settings table; a key with no row has
// its default. The registry is pure: the web validates writes with it, and
// the web and the scanner read through it, so a default lives in one place.
package settings

import (
	"fmt"
	"math"
	"strconv"

	"github.com/vinRosso/hogwatch/internal/scanner/agg"
)

// Kind is the type of a setting's value.
type Kind uint8

const (
	Bool Kind = iota + 1
	Int
)

// The known keys.
const (
	UsersSeeMountCapacity = "ui.users_see_mount_capacity"
	IncludeSnapshots      = "quota.include_snapshots"
	TopN                  = "top_n"
	TopMaxDepth           = "top_max_depth"
	TopMinBytes           = "top_min_bytes"
)

// Def describes one setting. Min and Max bound an Int.
type Def struct {
	Key      string
	Kind     Kind
	Default  string
	Min, Max int64
}

// Defs lists every known setting in key order.
var Defs = func() []Def {
	l := agg.DefaultLimits()
	return []Def{
		{Key: IncludeSnapshots, Kind: Bool, Default: "false"},
		{Key: TopMaxDepth, Kind: Int, Default: strconv.FormatInt(l.TopMaxDepth, 10), Min: 0, Max: 1 << 20},
		{Key: TopMinBytes, Kind: Int, Default: strconv.FormatInt(l.TopMinAlloc, 10), Min: 0, Max: math.MaxInt64},
		{Key: TopN, Kind: Int, Default: strconv.Itoa(l.TopN), Min: 1, Max: 1 << 20},
		{Key: UsersSeeMountCapacity, Kind: Bool, Default: "true"},
	}
}()

// Lookup returns the definition of key.
func Lookup(key string) (Def, bool) {
	for _, d := range Defs {
		if d.Key == key {
			return d, true
		}
	}
	return Def{}, false
}

// Validate reports whether v is an acceptable value: a boolean is exactly
// "true" or "false"; an integer is canonical base 10 within [Min, Max].
func (d Def) Validate(v string) error {
	switch d.Kind {
	case Bool:
		if v != "true" && v != "false" {
			return fmt.Errorf("%s must be true or false", d.Key)
		}
	case Int:
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || strconv.FormatInt(n, 10) != v {
			return fmt.Errorf("%s must be a base-10 integer", d.Key)
		}
		if n < d.Min || n > d.Max {
			return fmt.Errorf("%s must be between %d and %d", d.Key, d.Min, d.Max)
		}
	}
	return nil
}

// Effective returns every known key with its stored value, or its default
// when the key has no row or the stored value is invalid. Unknown stored keys
// are dropped.
func Effective(stored map[string]string) map[string]string {
	out := make(map[string]string, len(Defs))
	for _, d := range Defs {
		out[d.Key] = d.Default
		if v, ok := stored[d.Key]; ok && d.Validate(v) == nil {
			out[d.Key] = v
		}
	}
	return out
}
