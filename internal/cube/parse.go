package cube

import (
	"errors"
	"fmt"
	"strings"
)

// ErrBadFilter is wrapped by every ParseFilter failure.
var ErrBadFilter = errors.New("cube: bad filter")

// MaxFilterLen is the longest filter string ParseFilter accepts.
const MaxFilterLen = 512

// AgeNames and SizeNames are the filter tokens of the age and size buckets, in
// bucket order.
var (
	AgeNames  = [NumAges]string{"lt1w", "lt1mo", "lt3mo", "lt1y", "lt2y", "lt5y", "lt10y", "ge10y"}
	SizeNames = [NumSizes]string{"lt1m", "lt10m", "lt100m", "lt1g", "lt10g", "ge10g"}
)

// ParseFilter parses the filter grammar:
// "cat:<c>[,<c>…][;age:<a>,…][;size:<s>,…]". Each key appears at most once, in
// any order. The empty string is the zero Filter (everything).
func ParseFilter(s string) (Filter, error) {
	var f Filter
	if len(s) > MaxFilterLen {
		return Filter{}, fmt.Errorf("%w: longer than %d bytes", ErrBadFilter, MaxFilterLen)
	}
	if s == "" {
		return f, nil
	}
	seen := map[string]bool{}
	for _, part := range strings.Split(s, ";") {
		key, list, ok := strings.Cut(part, ":")
		if !ok || list == "" {
			return Filter{}, fmt.Errorf("%w: %q is not key:list", ErrBadFilter, part)
		}
		if seen[key] {
			return Filter{}, fmt.Errorf("%w: duplicate key %q", ErrBadFilter, key)
		}
		seen[key] = true
		var mask uint16
		for _, tok := range strings.Split(list, ",") {
			i, err := tokenIndex(key, tok)
			if err != nil {
				return Filter{}, err
			}
			mask |= 1 << uint(i)
		}
		switch key {
		case "cat":
			f.Categories = mask
		case "age":
			f.Ages = mask
		case "size":
			f.Sizes = mask
		}
	}
	return f, nil
}

func tokenIndex(key, tok string) (int, error) {
	switch key {
	case "cat":
		c, err := ParseCategory(tok)
		if err != nil {
			return 0, fmt.Errorf("%w: category %q", ErrBadFilter, tok)
		}
		return int(c), nil
	case "age":
		for i, n := range AgeNames {
			if n == tok {
				return i, nil
			}
		}
	case "size":
		for i, n := range SizeNames {
			if n == tok {
				return i, nil
			}
		}
	default:
		return 0, fmt.Errorf("%w: unknown key %q", ErrBadFilter, key)
	}
	return 0, fmt.Errorf("%w: %s token %q", ErrBadFilter, key, tok)
}

// String is the canonical form of f: keys in the order cat, age, size, tokens
// in bucket order, and "" for the zero Filter.
func (f Filter) String() string {
	var parts []string
	add := func(key string, mask uint16, n int, name func(int) string) {
		var toks []string
		for i := 0; i < n; i++ {
			if mask&(1<<uint(i)) != 0 {
				toks = append(toks, name(i))
			}
		}
		if len(toks) > 0 {
			parts = append(parts, key+":"+strings.Join(toks, ","))
		}
	}
	add("cat", f.Categories, NumCategories, func(i int) string { return categoryNames[i] })
	add("age", f.Ages, NumAges, func(i int) string { return AgeNames[i] })
	add("size", f.Sizes, NumSizes, func(i int) string { return SizeNames[i] })
	return strings.Join(parts, ";")
}
