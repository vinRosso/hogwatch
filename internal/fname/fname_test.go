package fname

import (
	"bytes"
	"errors"
	"testing"
	"unicode/utf8"
)

func TestEscape_Table(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"abc", "abc"},
		{`a\b`, `a\\b`},
		{"\xff", `\xff`},
		{"é", "é"},
		{"\n", "\n"},
		{"\xc3", `\xc3`},
		{rlo, rlo},
		{"a\xc3(b", `a\xc3(b`},
		{"\xef\xbf\xbd", "\xef\xbf\xbd"}, // a real U+FFFD stays
		{"", ""},
	} {
		if got := Escape([]byte(c.in)); got != c.want {
			t.Errorf("Escape(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestUnescape_Rejects(t *testing.T) {
	for _, in := range []string{`\`, `\x`, `\xg0`, `\q`, `\xFF`, `\x4`, "\xff", `\x41`, `\xc3\xa9`} {
		if _, err := Unescape(in); !errors.Is(err, ErrInvalid) {
			t.Errorf("Unescape(%q) = %v, want ErrInvalid", in, err)
		}
	}
}

func TestUnescape_Table(t *testing.T) {
	for _, c := range []struct {
		in   string
		want []byte
	}{
		{`a\\b`, []byte(`a\b`)},
		{`\xff`, []byte{0xff}},
		{"é", []byte("é")},
	} {
		got, err := Unescape(c.in)
		if err != nil || !bytes.Equal(got, c.want) {
			t.Errorf("Unescape(%q) = %q, %v", c.in, got, err)
		}
	}
}

func FuzzRoundTrip(f *testing.F) {
	for _, s := range []string{"", "abc", `a\b`, "\xff", "é\xc3", `\x41`, `\\x41`, "\xe2\x82", "\xf0\x9f\x98"} {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, b []byte) {
		e := Escape(b)
		if !utf8.ValidString(e) {
			t.Fatalf("Escape(%q) = %q is not valid UTF-8", b, e)
		}
		back, err := Unescape(e)
		if err != nil || !bytes.Equal(back, b) {
			t.Fatalf("Unescape(Escape(%q)) = %q, %v", b, back, err)
		}
		// Any string Unescape accepts is the canonical escape of its result.
		if u, err := Unescape(string(b)); err == nil {
			if Escape(u) != string(b) {
				t.Fatalf("Escape(Unescape(%q)) = %q", b, Escape(u))
			}
		}
	})
}

// rlo is U+202E, the right-to-left override.
var rlo = string(rune(0x202e))
