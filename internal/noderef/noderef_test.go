package noderef

import (
	"errors"
	"testing"
)

func TestParse(t *testing.T) {
	good := map[string]Ref{
		"1.1":                    {1, 1},
		"42.9223372036854775807": {42, 9223372036854775807},
	}
	for s, want := range good {
		got, err := Parse(s)
		if err != nil || got != want {
			t.Errorf("Parse(%q) = %v, %v; want %v", s, got, err, want)
		}
		if got.String() != s {
			t.Errorf("String() = %q; want %q", got.String(), s)
		}
	}
	bad := []string{"", "1", "1.", ".1", "1.2.3", "0.1", "1.0", "-1.2", "+1.2", "01.2", "1.02", " 1.2", "1.2 ", "1.9223372036854775808", "١.٢"}
	for _, s := range bad {
		if _, err := Parse(s); !errors.Is(err, ErrInvalid) {
			t.Errorf("Parse(%q) err = %v; want ErrInvalid", s, err)
		}
	}
}

func FuzzParse(f *testing.F) {
	for _, s := range []string{"1.1", "42.9223372036854775807", "", "1.2.3", "01.2"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		r, err := Parse(s)
		if err != nil {
			if !errors.Is(err, ErrInvalid) {
				t.Fatalf("err %v is not ErrInvalid", err)
			}
			return
		}
		if r.String() != s {
			t.Fatalf("round trip %q -> %q", s, r.String())
		}
	})
}
