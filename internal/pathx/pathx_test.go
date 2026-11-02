package pathx

import (
	"errors"
	"strings"
	"testing"
)

func TestNormalize(t *testing.T) {
	ok := map[string]string{
		"a/b":                    "a/b",
		"/a/b/":                  "a/b",
		"":                       "",
		"/":                      "",
		"a/\xff\n/c":             "a/\xff\n/c",
		strings.Repeat("x", 255): strings.Repeat("x", 255),
	}
	for in, want := range ok {
		got, err := Normalize(in)
		if err != nil || got != want {
			t.Errorf("Normalize(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	bad := []string{"a//b", "//a", "a/./b", "a/../b", "..", "a\x00b", strings.Repeat("x", 256), ".", "a//", "//"}
	for _, in := range bad {
		if _, err := Normalize(in); !errors.Is(err, ErrInvalidPath) {
			t.Errorf("Normalize(%q) err = %v; want ErrInvalidPath", in, err)
		}
	}
}

func TestSegments(t *testing.T) {
	if s := Segments(""); len(s) != 0 {
		t.Errorf("Segments(\"\") = %q", s)
	}
	if s := Segments("a/b"); len(s) != 2 || s[0] != "a" || s[1] != "b" {
		t.Errorf("Segments(a/b) = %q", s)
	}
}

func TestCompare(t *testing.T) {
	cases := []struct {
		a, b string
		want Relation
	}{
		{"a/b", "a/bc", Disjoint},
		{"a", "a", Equal},
		{"a/b", "a", AInsideB},
		{"a", "a/b", BInsideA},
		{"", "x", BInsideA},
		{"x", "", AInsideB},
		{"", "", Equal},
		{"a/b", "a/c", Disjoint},
	}
	for _, c := range cases {
		if got := Compare(c.a, c.b); got != c.want {
			t.Errorf("Compare(%q,%q) = %v; want %v", c.a, c.b, got, c.want)
		}
	}
}

func TestClassify(t *testing.T) {
	as := func(u, r int64, p string) Assignment { return Assignment{u, r, p} }
	cases := []struct {
		name  string
		a, b  Assignment
		v     Verdict
		inner *Assignment
	}{
		{"same user nested", as(1, 1, "a"), as(1, 1, "a/b"), Redundant, ptr(as(1, 1, "a/b"))},
		{"same user nested reversed", as(1, 1, "a/b"), as(1, 1, "a"), Redundant, ptr(as(1, 1, "a/b"))},
		{"same user equal", as(1, 1, "a"), as(1, 1, "a"), Redundant, nil},
		{"diff user equal", as(1, 1, "a"), as(2, 1, "a"), Conflict, nil},
		{"diff user nested", as(1, 1, "a"), as(2, 1, "a/b"), Conflict, nil},
		{"diff user disjoint", as(1, 1, "a"), as(2, 1, "b"), Independent, nil},
		{"diff user prefix string only", as(1, 1, "a/b"), as(2, 1, "a/bc"), Independent, nil},
		{"same user disjoint", as(1, 1, "a"), as(1, 1, "b"), Independent, nil},
		{"diff roots equal", as(1, 1, "a"), as(2, 2, "a"), Independent, nil},
		{"diff roots same user nested", as(1, 1, "a"), as(1, 2, "a/b"), Independent, nil},
		{"root path nests all", as(1, 1, ""), as(2, 1, "x"), Conflict, nil},
	}
	for _, c := range cases {
		v, inner := Classify(c.a, c.b)
		if v != c.v {
			t.Errorf("%s: verdict = %v; want %v", c.name, v, c.v)
			continue
		}
		if c.inner != nil && inner != *c.inner {
			t.Errorf("%s: inner = %+v; want %+v", c.name, inner, *c.inner)
		}
		if v == Redundant && inner != c.a && inner != c.b {
			t.Errorf("%s: inner %+v is neither input", c.name, inner)
		}
	}
}

func ptr(a Assignment) *Assignment { return &a }
