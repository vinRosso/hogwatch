package gen_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"testing"

	"github.com/vinRosso/hogwatch/internal/gen"
)

// readerFixture writes a finished generation: root (id 1), a hostile-named
// chain 2 = "\xff", 3 = "a\nb" below it, 4 = "plain" below the root, plus
// pre/post numbers so ForEachDir has them to return.
func readerFixture(t *testing.T) *sql.DB {
	t.Helper()
	ctx := t.Context()
	path := newPath(t)
	w := mustCreate(t, path)
	for _, d := range []gen.Dir{
		dir(1, 0, ""),
		dir(2, 1, "\xff"),
		dir(3, 2, "a\nb"),
		dir(4, 1, "plain"),
	} {
		if err := w.AddDir(ctx, d); err != nil {
			t.Fatal(err)
		}
	}
	for id, pp := range map[int64][2]int64{1: {1, 4}, 2: {2, 3}, 3: {3, 3}, 4: {4, 4}} {
		if err := w.SetPrePost(ctx, id, pp[0], pp[1]); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.BuildIndexes(ctx); err != nil {
		t.Fatal(err)
	}
	mustFinish(t, w)
	d, _ := mustOpenRO(t, path)
	return d
}

func TestLookup(t *testing.T) {
	ctx := t.Context()
	d := readerFixture(t)

	got, err := gen.Lookup(ctx, d, [][]byte{[]byte("\xff"), []byte("a\nb")})
	if err != nil || got.ID != 3 || got.ParentID != 2 || string(got.Name) != "a\nb" {
		t.Fatalf("hostile path = %+v, %v; want dir 3", got, err)
	}
	if got, err = gen.Lookup(ctx, d, nil); err != nil || got.ID != 1 || got.ParentID != 0 {
		t.Fatalf("empty segments = %+v, %v; want the root", got, err)
	}
	for _, segs := range [][][]byte{
		{[]byte("missing"), []byte("a\nb")}, // missing middle segment
		{[]byte("\xff"), []byte("missing")},
		{[]byte("plain"), []byte("a\nb")}, // exists, but under another parent
	} {
		if _, err := gen.Lookup(ctx, d, segs); !errors.Is(err, gen.ErrNoDir) {
			t.Errorf("Lookup(%q) err = %v, want ErrNoDir", segs, err)
		}
	}
}

func TestDirByID(t *testing.T) {
	ctx := t.Context()
	d := readerFixture(t)
	got, err := gen.DirByID(ctx, d, 4)
	if err != nil || string(got.Name) != "plain" || got.Pre != 4 || got.Post != 4 || got.Files != 1 {
		t.Fatalf("DirByID(4) = %+v, %v", got, err)
	}
	for _, id := range []int64{0, 99, -1} {
		if _, err := gen.DirByID(ctx, d, id); !errors.Is(err, gen.ErrNoDir) {
			t.Errorf("DirByID(%d) err = %v, want ErrNoDir", id, err)
		}
	}
}

func TestDirPath(t *testing.T) {
	ctx := t.Context()
	d := readerFixture(t)
	p, err := gen.DirPath(ctx, d, 1)
	if err != nil || len(p) != 0 {
		t.Fatalf("DirPath(root) = %q, %v; want empty", p, err)
	}
	p, err = gen.DirPath(ctx, d, 3)
	if err != nil || len(p) != 2 || string(p[0]) != "\xff" || string(p[1]) != "a\nb" {
		t.Fatalf("DirPath(3) = %q, %v", p, err)
	}
	if _, err := gen.DirPath(ctx, d, 99); !errors.Is(err, gen.ErrNoDir) {
		t.Errorf("DirPath(99) err = %v, want ErrNoDir", err)
	}
}

func TestDirPath_RoundTrip(t *testing.T) {
	ctx := t.Context()
	path := newPath(t)
	w := mustCreate(t, path)
	// 50 dirs: root, 7 children, and the rest hanging off earlier dirs, with
	// hostile names (the index is the unique part so siblings never collide).
	const n = 50
	for id := int64(1); id <= n; id++ {
		var parent int64
		if id > 1 {
			parent = (id-1)/3 + 1
		}
		name := ""
		if id > 1 {
			name = fmt.Sprintf("d%d\xff\n", id)
		}
		if err := w.AddDir(ctx, dir(id, parent, name)); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.BuildIndexes(ctx); err != nil {
		t.Fatal(err)
	}
	mustFinish(t, w)
	d, _ := mustOpenRO(t, path)
	for id := int64(1); id <= n; id++ {
		p, err := gen.DirPath(ctx, d, id)
		if err != nil {
			t.Fatalf("DirPath(%d): %v", id, err)
		}
		got, err := gen.Lookup(ctx, d, p)
		if err != nil || got.ID != id {
			t.Fatalf("Lookup(DirPath(%d)) = %+v, %v", id, got, err)
		}
	}
}

func TestForEachDir(t *testing.T) {
	ctx := t.Context()
	d := readerFixture(t)
	var ids []int64
	var last gen.Dir
	err := gen.ForEachDir(ctx, d, func(r gen.Dir) error {
		ids = append(ids, r.ID)
		last = r
		return nil
	})
	if err != nil || !slices.Equal(ids, []int64{1, 2, 3, 4}) {
		t.Fatalf("ids = %v, %v", ids, err)
	}
	if last.Pre != 4 || last.Post != 4 || string(last.Name) != "plain" {
		t.Errorf("last = %+v", last)
	}
	stop := errors.New("stop")
	calls := 0
	if err := gen.ForEachDir(ctx, d, func(gen.Dir) error { calls++; return stop }); !errors.Is(err, stop) || calls != 1 {
		t.Errorf("early stop: err = %v after %d calls", err, calls)
	}
	cctx, cancel := context.WithCancel(ctx)
	cancel()
	if err := gen.ForEachDir(cctx, d, func(gen.Dir) error { return nil }); err == nil {
		t.Error("cancelled context: no error")
	}
}

func TestOpenReadOnly_RejectsV1(t *testing.T) {
	path := newPath(t)
	w := mustCreate(t, path)
	mustFinish(t, w)
	rw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rw.ExecContext(t.Context(), "PRAGMA user_version = 1"); err != nil {
		t.Fatal(err)
	}
	if err := rw.Close(); err != nil {
		t.Fatal(err)
	}
	d, _, err := gen.OpenReadOnly(t.Context(), path)
	if d != nil {
		_ = d.Close()
	}
	if !errors.Is(err, gen.ErrSchemaVersion) {
		t.Fatalf("err = %v, want ErrSchemaVersion", err)
	}
}
