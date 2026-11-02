package cube

import (
	"errors"
	"strings"
	"testing"
)

func TestParseFilter(t *testing.T) {
	cat := func(cs ...Category) uint16 {
		var m uint16
		for _, c := range cs {
			m |= 1 << c
		}
		return m
	}
	ok := []struct {
		in   string
		want Filter
		str  string
	}{
		{"", Filter{}, ""},
		{"cat:video", Filter{Categories: cat(Video)}, "cat:video"},
		{"cat:video,image", Filter{Categories: cat(Video, Image)}, "cat:video,image"},
		{"cat:image,video", Filter{Categories: cat(Video, Image)}, "cat:video,image"},
		{"cat:disk-image", Filter{Categories: cat(DiskImage)}, "cat:disk-image"},
		{"age:lt1w;size:ge10g", Filter{Ages: 1, Sizes: 1 << 5}, "age:lt1w;size:ge10g"},
		{"size:lt1m,lt10m;cat:code;age:ge10y,lt1mo", Filter{Categories: cat(Code), Ages: 1<<7 | 1<<1, Sizes: 1 | 1<<1}, "cat:code;age:lt1mo,ge10y;size:lt1m,lt10m"},
		{"cat:video,video", Filter{Categories: cat(Video)}, "cat:video"},
	}
	for _, c := range ok {
		got, err := ParseFilter(c.in)
		if err != nil {
			t.Errorf("ParseFilter(%q): %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("ParseFilter(%q) = %+v, want %+v", c.in, got, c.want)
		}
		if s := got.String(); s != c.str {
			t.Errorf("(%q).String() = %q, want %q", c.in, s, c.str)
		}
	}

	bad := []string{
		"cat:", "cat:foo", "cat:video;cat:audio", "bogus:1", "cat:video;", ";cat:video",
		"cat", "cat:video,", "cat:,video", "age:lt2w", "size:ge1g", "cat:Video", " cat:video",
		"cat:video;;age:lt1w", "age:lt1w;age:lt1mo", "cat:video:image", "age:", "cat:video;size:",
		strings.Repeat("a", 513),
	}
	for _, in := range bad {
		if _, err := ParseFilter(in); !errors.Is(err, ErrBadFilter) {
			t.Errorf("ParseFilter(%q) err = %v, want ErrBadFilter", in, err)
		}
	}
}

func FuzzParseFilter(f *testing.F) {
	for _, s := range []string{"", "cat:video,image;age:lt1w;size:ge10g", "cat:", ";", "cat:video;cat:audio", "age:ge10y,lt1w"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		got, err := ParseFilter(s)
		if err != nil {
			if !errors.Is(err, ErrBadFilter) {
				t.Fatalf("ParseFilter(%q) error %v is not ErrBadFilter", s, err)
			}
			return
		}
		again, err := ParseFilter(got.String())
		if err != nil || again != got {
			t.Fatalf("round trip of %q: %+v -> %q -> %+v, %v", s, got, got.String(), again, err)
		}
	})
}

func TestParseFilter_LengthBoundary(t *testing.T) {
	at := "cat:code" + strings.Repeat(",code", 98) + ";age:lt1w,lt1w"
	if len(at) != MaxFilterLen {
		t.Fatalf("fixture is %d bytes, want %d", len(at), MaxFilterLen)
	}
	if _, err := ParseFilter(at); err != nil {
		t.Errorf("a %d-byte filter: %v", len(at), err)
	}
	if _, err := ParseFilter(at + "x"); !errors.Is(err, ErrBadFilter) {
		t.Errorf("a %d-byte filter err = %v, want ErrBadFilter", len(at)+1, err)
	}
}
