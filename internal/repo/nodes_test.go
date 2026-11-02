package repo

import (
	"errors"
	"testing"
)

func TestNode_RelPath(t *testing.T) {
	f := newFixture(t)
	f.scan(1, tree...)
	s := ScopeAll()
	for _, rel := range []string{"", "a", "a/b", "bad\xff", "bad\xff/sub"} {
		n, err := f.repo.Node(t.Context(), s, f.ref(1, rel))
		if err != nil {
			t.Fatalf("Node(%q): %v", rel, err)
		}
		if string(n.RelPath) != rel {
			t.Errorf("RelPath of %q = %q", rel, n.RelPath)
		}
	}
}

func TestResolve(t *testing.T) {
	f := newFixture(t)
	f.scan(1, tree...)
	u := f.user("u")
	f.path(u, "a/b")
	s := f.scope(u, 1)

	got, err := f.repo.Resolve(t.Context(), s, 1, []byte("a/b/c"))
	if err != nil || got != f.ref(1, "a/b/c") {
		t.Fatalf("Resolve(a/b/c) = %v, %v", got, err)
	}
	if got, err = f.repo.Resolve(t.Context(), s, 1, []byte("a/b")); err != nil || got != f.ref(1, "a/b") {
		t.Fatalf("Resolve(anchor) = %v, %v", got, err)
	}
	for _, rel := range []string{"a", "", "a/x", "d", "nope", "a/b/nope"} {
		if _, err := f.repo.Resolve(t.Context(), s, 1, []byte(rel)); !errors.Is(err, ErrNotFound) {
			t.Errorf("Resolve(%q) err = %v, want ErrNotFound", rel, err)
		}
	}
	if _, err := f.repo.Resolve(t.Context(), s, 9, []byte("a/b")); !errors.Is(err, ErrNotFound) {
		t.Errorf("Resolve on a scan outside the scope: %v", err)
	}
	// Invalid UTF-8 resolves byte-exactly.
	if got, err = f.repo.Resolve(t.Context(), ScopeAll(), 1, []byte("bad\xff/sub")); err != nil || got != f.ref(1, "bad\xff/sub") {
		t.Fatalf("Resolve(bad\\xff/sub) = %v, %v", got, err)
	}
}
