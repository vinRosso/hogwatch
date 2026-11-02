// Package cube defines the (category, age, size) cube buckets and the sparse
// blob that stores one directory's subtree cube.
package cube

import (
	"errors"
	"fmt"
	"time"
)

// Category is a file category bucket.
type Category uint8

// Categories, in cube order.
const (
	Video Category = iota
	Audio
	Image
	Archive
	DiskImage
	Document
	Code
	Other
)

// Cube dimensions.
const (
	NumCategories = 8
	NumAges       = 8
	NumSizes      = 6
	NumCells      = NumCategories * NumAges * NumSizes // 384
)

var categoryNames = [NumCategories]string{
	"video", "audio", "image", "archive", "disk-image", "document", "code", "other",
}

// String returns the category's canonical name, or "invalid".
func (c Category) String() string {
	if int(c) < NumCategories {
		return categoryNames[c]
	}
	return "invalid"
}

// ParseCategory is the inverse of String; matching is exact.
func ParseCategory(s string) (Category, error) {
	for i, n := range categoryNames {
		if n == s {
			return Category(i), nil //nolint:gosec // i < NumCategories
		}
	}
	return 0, fmt.Errorf("cube: unknown category %q", s)
}

// Cell addresses one cube cell.
type Cell struct{ Category, Age, Size uint8 }

// Index returns the cell's position in an Acc: (cat*8+age)*6+size.
func (c Cell) Index() int {
	return (int(c.Category)*NumAges+int(c.Age))*NumSizes + int(c.Size)
}

// AgeBucket places mtime relative to scanStart using calendar intervals
// (AddDate). An instant exactly on a bucket edge belongs to the older bucket;
// a future mtime is bucket 0.
func AgeBucket(mtime, scanStart time.Time) uint8 {
	edges := [NumAges - 1]time.Time{
		scanStart.AddDate(0, 0, -7),
		scanStart.AddDate(0, -1, 0),
		scanStart.AddDate(0, -3, 0),
		scanStart.AddDate(-1, 0, 0),
		scanStart.AddDate(-2, 0, 0),
		scanStart.AddDate(-5, 0, 0),
		scanStart.AddDate(-10, 0, 0),
	}
	for i, e := range edges {
		if mtime.After(e) {
			return uint8(i) //nolint:gosec // i < 7
		}
	}
	return NumAges - 1
}

// SizeBucket places a size in bytes using binary units (1 MB = 2^20 B). A
// value equal to a bound goes to the higher bucket. Negative sizes are bucket 0.
func SizeBucket(size int64) uint8 {
	switch {
	case size < 1<<20:
		return 0
	case size < 10<<20:
		return 1
	case size < 100<<20:
		return 2
	case size < 1<<30:
		return 3
	case size < 10<<30:
		return 4
	default:
		return 5
	}
}

// Acc is a dense accumulator of per-cell byte and file counts.
type Acc [NumCells]struct{ Bytes, Files int64 }

// Add adds to a cell. Negative deltas are allowed (hardlink re-attribution);
// the caller keeps totals within int64.
func (a *Acc) Add(c Cell, bytes, files int64) {
	i := c.Index()
	a[i].Bytes += bytes
	a[i].Files += files
}

// Merge adds every cell of b into a.
func (a *Acc) Merge(b *Acc) {
	for i := range a {
		a[i].Bytes += b[i].Bytes
		a[i].Files += b[i].Files
	}
}

// ErrCorrupt is returned for any malformed blob.
var ErrCorrupt = errors.New("cube: corrupt blob")
