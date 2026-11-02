package oidc

import (
	"strconv"
	"strings"
)

const (
	maxUsername = 64 // local.ValidUsername allows one leading character and 63 more
	baseCap     = 56 // leaves room for "-NN" collision suffixes
)

// DeriveUsername builds a valid, normalized username from the IdP's
// preferred_username, falling back to the local part of the email and then to
// "user". Every byte outside [a-z0-9._-] becomes "-" (a run becomes one), the
// result starts with [a-z0-9] and is at most 56 bytes, so a collision suffix
// still fits. It never fails and never returns an invalid name.
func DeriveUsername(preferred, email string) string {
	if u := sanitizeUsername(preferred); u != "" {
		return u
	}
	local, _, _ := strings.Cut(email, "@")
	if u := sanitizeUsername(local); u != "" {
		return u
	}
	return "user"
}

func sanitizeUsername(s string) string {
	b := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'A' && c <= 'Z' {
			c += 'a' - 'A'
		}
		switch {
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9', c == '.', c == '-', c == '_':
			b = append(b, c)
		case len(b) == 0 || b[len(b)-1] != '-': // a run of other bytes becomes one dash
			b = append(b, '-')
		}
	}
	out := strings.TrimLeftFunc(string(b), func(r rune) bool { return (r < 'a' || r > 'z') && (r < '0' || r > '9') })
	if len(out) > baseCap {
		out = out[:baseCap]
	}
	return out
}

// WithSuffix appends "-n" to base, cutting base so the result is at most 64
// bytes. Callers use it after a UNIQUE(username) conflict.
func WithSuffix(base string, n int) string {
	suffix := "-" + strconv.Itoa(n)
	if keep := maxUsername - len(suffix); len(base) > keep {
		base = base[:keep]
	}
	return base + suffix
}
