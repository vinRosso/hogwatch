package cube

import "testing"

func TestCategoryOf(t *testing.T) {
	cats := DefaultCategories()
	for _, tc := range []struct {
		name string
		want Category
	}{
		{"a.MKV", Video}, {"noext", Other}, {"x.ts", Video}, {".mkv", Other}, {"a.unknown", Other},
	} {
		if got := CategoryOf([]byte(tc.name), cats); got != tc.want {
			t.Errorf("CategoryOf(%q) = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestDefaultCategories_FreshCopy(t *testing.T) {
	a := DefaultCategories()
	a["mp4"] = Other
	delete(a, "mkv")
	b := DefaultCategories()
	if b["mp4"] != Video || b["mkv"] != Video {
		t.Fatal("DefaultCategories shares state between calls")
	}
}
