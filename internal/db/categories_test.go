package db

import (
	"context"
	"testing"

	"github.com/vinRosso/hogwatch/internal/cube"
)

func TestDefaultCategories_MatchSeed(t *testing.T) {
	d := openMigrated(t)
	rows, err := d.QueryContext(context.Background(), `SELECT ext, category FROM file_categories`)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	got := map[string]cube.Category{}
	for rows.Next() {
		var ext, cat string
		if err := rows.Scan(&ext, &cat); err != nil {
			t.Fatal(err)
		}
		c, err := cube.ParseCategory(cat)
		if err != nil {
			t.Fatal(err)
		}
		got[ext] = c
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	want := cube.DefaultCategories()
	for k, v := range want {
		if g, ok := got[k]; !ok || g != v {
			t.Errorf("ext %q: seed %v (present %v), default %v", k, g, ok, v)
		}
	}
	for k := range got {
		if _, ok := want[k]; !ok {
			t.Errorf("ext %q in seed but not in DefaultCategories", k)
		}
	}
}
