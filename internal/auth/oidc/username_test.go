package oidc

import (
	"strings"
	"testing"

	"github.com/vinRosso/hogwatch/internal/auth/local"
)

func TestDeriveUsername(t *testing.T) {
	rows := []struct{ name, preferred, email, want string }{
		{"lowercased", "Alice", "", "alice"},
		{"path traversal", "../x", "", "x"},
		{"dot dot only falls back to email", "..", "bob@x.example", "bob"},
		{"unicode", "jürgen", "", "j-rgen"},
		{"runs collapse", "a  b!!c", "", "a-b-c"},
		{"leading junk trimmed", "-_.alice", "", "alice"},
		{"long", strings.Repeat("a", 200), "", strings.Repeat("a", 56)},
		{"empty falls back to email local part", "", "bob@x.example", "bob"},
		{"email local part is sanitised too", "", "Bob.Smith+tag@x.example", "bob.smith-tag"},
		{"everything empty", "", "", "user"},
		{"only junk everywhere", "!!!", "@x.example", "user"},
		{"admin is just a name", "Admin", "", "admin"},
		{"underscore kept", "alice_bob", "", "alice_bob"},
		{"whitespace", "  Al ice ", "", "al-ice-"},
		{"control bytes", "a\x00b\nc", "", "a-b-c"},
	}
	for _, r := range rows {
		t.Run(r.name, func(t *testing.T) {
			got := DeriveUsername(r.preferred, r.email)
			if got != r.want {
				t.Fatalf("DeriveUsername(%q, %q) = %q, want %q", r.preferred, r.email, got, r.want)
			}
			if !local.ValidUsername(got) {
				t.Fatalf("%q is not a valid username", got)
			}
		})
	}
}

func TestWithSuffix(t *testing.T) {
	if got := WithSuffix("alice", 2); got != "alice-2" {
		t.Fatalf("got %q", got)
	}
	long := strings.Repeat("a", 56)
	got := WithSuffix(long, 50)
	if len(got) > 64 || !local.ValidUsername(got) || !strings.HasSuffix(got, "-50") {
		t.Fatalf("got %q (%d bytes)", got, len(got))
	}
	// Even a base already at the limit keeps room for the suffix.
	got = WithSuffix(strings.Repeat("b", 64), 12345)
	if len(got) != 64 || !strings.HasSuffix(got, "-12345") || !local.ValidUsername(got) {
		t.Fatalf("got %q (%d bytes)", got, len(got))
	}
}

func FuzzDeriveUsername(f *testing.F) {
	for _, s := range []string{"", "Alice", "../x", "jürgen", "\x00", "--", "a@b", strings.Repeat("é", 100), "İstanbul", "ADMIN"} {
		f.Add(s, s)
	}
	f.Add("", "x@y")
	f.Fuzz(func(t *testing.T, preferred, email string) {
		got := DeriveUsername(preferred, email)
		if !local.ValidUsername(got) {
			t.Fatalf("DeriveUsername(%q, %q) = %q", preferred, email, got)
		}
		if s := WithSuffix(got, 99); !local.ValidUsername(s) {
			t.Fatalf("WithSuffix(%q, 99) = %q", got, s)
		}
		if got != local.NormalizeUsername(got) {
			t.Fatalf("%q is not in normalized form", got)
		}
	})
}
