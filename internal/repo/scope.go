package repo

import (
	"slices"
	"strings"
)

// span is the closed pre-order interval of one directory subtree.
type span struct{ pre, post int64 }

// Scope says which directories of one scan generation a caller may see
// (invariant 2). The zero Scope denies everything.
type Scope struct {
	all    bool
	scanID int64
	spans  []span // sorted by pre, disjoint
}

// ScopeAll is the admin scope: every directory of every generation.
func ScopeAll() Scope { return Scope{all: true} }

// allows reports whether the scope can see anything in scan scanID at all.
func (s Scope) allows(scanID int64) bool {
	if s.all {
		return true
	}
	return s.scanID == scanID && len(s.spans) > 0
}

// where returns an SQL predicate over the pre column col and its arguments.
// A denied scope yields "0", the admin scope "1".
func (s Scope) where(col string) (string, []any) {
	if s.all {
		return "1", nil
	}
	if len(s.spans) == 0 {
		return "0", nil
	}
	var sb strings.Builder
	args := make([]any, 0, 2*len(s.spans))
	sb.WriteByte('(')
	for i, sp := range s.spans {
		if i > 0 {
			sb.WriteString(" OR ")
		}
		sb.WriteString(col + " BETWEEN ? AND ?")
		args = append(args, sp.pre, sp.post)
	}
	sb.WriteByte(')')
	return sb.String(), args
}

// mergeSpans sorts spans and drops those nested in another.
func mergeSpans(in []span) []span {
	slices.SortFunc(in, func(a, b span) int {
		switch {
		case a.pre < b.pre:
			return -1
		case a.pre > b.pre:
			return 1
		case a.post > b.post:
			return -1
		case a.post < b.post:
			return 1
		}
		return 0
	})
	var out []span
	for _, sp := range in {
		if n := len(out); n > 0 && sp.pre <= out[n-1].post {
			continue // nested: subtrees are laminar
		}
		out = append(out, sp)
	}
	return out
}
