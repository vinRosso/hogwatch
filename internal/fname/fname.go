// Package fname encodes filenames, which are raw bytes, as JSON-safe strings
// (invariant 8). Valid UTF-8 passes through unchanged, each byte that is not
// part of a valid sequence becomes \xNN with lowercase hex digits, and a
// backslash becomes \\. The encoding is lossless and canonical: every byte
// slice has exactly one encoding.
package fname

import (
	"errors"
	"strings"
	"unicode/utf8"
)

// ErrInvalid is returned by Unescape for a string that Escape could not have
// produced.
var ErrInvalid = errors.New("fname: invalid escaped name")

const hexDigits = "0123456789abcdef"

// Escape returns the canonical string form of the name b.
func Escape(b []byte) string {
	var sb strings.Builder
	sb.Grow(len(b))
	for i := 0; i < len(b); {
		r, n := utf8.DecodeRune(b[i:])
		switch {
		case r == utf8.RuneError && n == 1:
			sb.WriteString(`\x`)
			sb.WriteByte(hexDigits[b[i]>>4])
			sb.WriteByte(hexDigits[b[i]&0xf])
		case r == '\\':
			sb.WriteString(`\\`)
		default:
			sb.Write(b[i : i+n])
		}
		i += n
	}
	return sb.String()
}

// Unescape is the inverse of Escape. It returns ErrInvalid for input that is
// not valid UTF-8, has a malformed or unknown escape, or is not the canonical
// encoding of the bytes it decodes to (for example an escaped ASCII byte).
func Unescape(s string) ([]byte, error) {
	if !utf8.ValidString(s) {
		return nil, ErrInvalid
	}
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		if s[i] != '\\' {
			out = append(out, s[i])
			continue
		}
		if i+1 >= len(s) {
			return nil, ErrInvalid
		}
		switch s[i+1] {
		case '\\':
			out = append(out, '\\')
			i++
		case 'x':
			if i+3 >= len(s) {
				return nil, ErrInvalid
			}
			hi, lo := unhex(s[i+2]), unhex(s[i+3])
			if hi < 0 || lo < 0 {
				return nil, ErrInvalid
			}
			out = append(out, byte(hi<<4|lo)) //nolint:gosec // two nibbles fit a byte
			i += 3
		default:
			return nil, ErrInvalid
		}
	}
	if Escape(out) != s {
		return nil, ErrInvalid
	}
	return out, nil
}

func unhex(c byte) int {
	switch {
	case '0' <= c && c <= '9':
		return int(c - '0')
	case 'a' <= c && c <= 'f':
		return int(c-'a') + 10
	}
	return -1
}
