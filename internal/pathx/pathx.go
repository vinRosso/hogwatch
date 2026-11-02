// Package pathx normalises relative paths assigned to users and classifies
// how two assignments overlap. Paths are raw bytes split on
// "/"; no UTF-8 assumptions are made.
package pathx

import (
	"errors"
	"strings"
)

// ErrInvalidPath is wrapped by every Normalize failure.
var ErrInvalidPath = errors.New("pathx: invalid path")

const maxSegment = 255

// Normalize returns the canonical relative form of p: at most one leading and
// one trailing "/" trimmed, no empty, "." or ".." segments, no NUL, and each
// segment at most 255 bytes. "" means the root itself.
func Normalize(p string) (string, error) {
	if p == "" || p == "/" {
		return "", nil
	}
	s := strings.TrimPrefix(p, "/")
	if len(s) > 1 {
		s = strings.TrimSuffix(s, "/")
	}
	for _, seg := range strings.Split(s, "/") {
		if seg == "" || seg == "." || seg == ".." ||
			strings.IndexByte(seg, 0) >= 0 || len(seg) > maxSegment {
			return "", ErrInvalidPath
		}
	}
	return s, nil
}

// Segments splits a normalised path; the root ("") has no segments.
func Segments(p string) []string {
	if p == "" {
		return nil
	}
	return strings.Split(p, "/")
}

// Relation describes how path A relates to path B.
type Relation int

const (
	Disjoint Relation = iota
	Equal
	AInsideB
	BInsideA
)

// Compare relates two normalised paths on whole segments.
func Compare(a, b string) Relation {
	sa, sb := Segments(a), Segments(b)
	n := min(len(sa), len(sb))
	for i := 0; i < n; i++ {
		if sa[i] != sb[i] {
			return Disjoint
		}
	}
	switch {
	case len(sa) == len(sb):
		return Equal
	case len(sa) > len(sb):
		return AInsideB
	default:
		return BInsideA
	}
}

// Assignment is a path assigned to a user within a root.
type Assignment struct {
	UserID, RootID int64
	Path           string
}

// Verdict is the outcome of comparing two assignments.
type Verdict int

const (
	Independent Verdict = iota
	Redundant
	Conflict
)

// Classify applies the overlap table. Assignments in different roots are always
// independent. For Redundant, inner is the assignment not counted: the nested
// one, or b when the paths are equal.
func Classify(a, b Assignment) (v Verdict, inner Assignment) {
	if a.RootID != b.RootID {
		return Independent, Assignment{}
	}
	rel := Compare(a.Path, b.Path)
	if rel == Disjoint {
		return Independent, Assignment{}
	}
	if a.UserID != b.UserID {
		return Conflict, Assignment{}
	}
	if rel == BInsideA || rel == Equal {
		return Redundant, b
	}
	return Redundant, a
}
