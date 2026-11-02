package local

import (
	"regexp"
	"strings"
)

var usernameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)

// NormalizeUsername is the form a username is stored and looked up in.
func NormalizeUsername(s string) string { return strings.ToLower(strings.TrimSpace(s)) }

// ValidUsername reports whether a normalized username may be created.
func ValidUsername(s string) bool { return usernameRE.MatchString(s) }
