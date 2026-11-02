// Package gentest builds small generation files for tests of the packages
// that read them.
package gentest

import (
	"cmp"
	"encoding/json"
	"maps"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/vinRosso/hogwatch/internal/cube"
	"github.com/vinRosso/hogwatch/internal/gen"
	"github.com/vinRosso/hogwatch/internal/pathx"
)

// Dir is one directory of a test tree. Rel is the "/"-joined raw-byte path
// below the scan root ("" is the root, which is implied when absent). The
// numbers are the directory's own files; rollups are computed.
type Dir struct {
	Rel                             string
	Files, BytesLogical, BytesAlloc int64
	// Mtime is Unix seconds; 0 means 1_700_000_000.
	Mtime int64
	Flags int64
	// Cells holds the cube cells of the directory's own files as
	// (bytes, files); each directory's cube is the sum over its subtree.
	Cells map[cube.Cell][2]int64
	// Top rows describe this directory's files; BuildWith stores them for every
	// anchor in BuildOpts.TopAnchors with this directory as the parent.
	Top []TopFile
	// Exts and Errors are stored against this directory (DirID is filled in).
	// Exts also count toward every BuildOpts.ExtAnchors directory above it.
	Exts   []gen.TopExt
	Errors []gen.ErrorRow
}

// TopFile is one top-N file of a test directory.
type TopFile struct {
	Kind               string
	Rank               int
	Name               string
	Size, Alloc, Mtime int64
}

// BuildOpts adds optional content to BuildWith.
type BuildOpts struct {
	// TopAnchors are the Rel paths whose top-N lists receive the Top rows of
	// every directory below it, itself included.
	TopAnchors []string
	// ExtAnchors are the Rel paths whose top_exts rows hold the Exts of every
	// directory below it, itself included, summed per extension and cut to the
	// largest 20, as the scanner stores them for qualifying directories.
	ExtAnchors []string
}

// maxExts is the scanner's default top_exts cut (agg.DefaultLimits).
const maxExts = 20

// Build writes a finished generation file for (rootID, scanID) below dataDir
// and returns its path. Ids are dense in sorted path order, so every parent
// precedes its children; the root is id 1.
func Build(tb testing.TB, dataDir string, rootID, scanID int64, dirs []Dir) string {
	tb.Helper()
	return BuildWith(tb, dataDir, rootID, scanID, dirs, BuildOpts{})
}

// BuildWith is Build plus cubes, top-N rows, extensions and errors.
func BuildWith(tb testing.TB, dataDir string, rootID, scanID int64, dirs []Dir, opts BuildOpts) string {
	tb.Helper()
	ctx := tb.Context()
	byRel := map[string]Dir{"": {}}
	for _, d := range dirs {
		if _, err := pathx.Normalize(d.Rel); err != nil {
			tb.Fatalf("gentest: path %q: %v", d.Rel, err)
		}
		byRel[d.Rel] = d
		// Create the ancestors implicitly.
		for p := parent(d.Rel); p != ""; p = parent(p) {
			if _, ok := byRel[p]; !ok {
				byRel[p] = Dir{Rel: p}
			}
		}
	}
	for _, a := range slices.Concat(opts.TopAnchors, opts.ExtAnchors) {
		for p := a; p != ""; p = parent(p) {
			if _, ok := byRel[p]; !ok {
				byRel[p] = Dir{Rel: p}
			}
		}
	}
	rels := make([]string, 0, len(byRel))
	for r := range byRel {
		rels = append(rels, r)
	}
	slices.Sort(rels)
	id := map[string]int64{}
	for i, r := range rels {
		id[r] = int64(i + 1)
	}

	rows := make([]gen.Dir, len(rels))
	for i, r := range rels {
		d := byRel[r]
		row := gen.Dir{
			ID: id[r], Name: []byte(base(r)), Dev: 1, Ino: id[r], Mtime: 1_700_000_000,
			OwnFiles: d.Files, OwnAlloc: d.BytesAlloc, Flags: d.Flags,
		}
		if d.Mtime != 0 {
			row.Mtime = d.Mtime
		}
		if r != "" {
			row.ParentID = id[parent(r)]
		}
		rows[i] = row
	}
	// Roll up: descendants come after their ancestors in sorted order, so a
	// reverse pass adds every directory to its parent once it is complete.
	for i, r := range rels {
		rows[i].Files = byRel[r].Files
		rows[i].BytesLogical = byRel[r].BytesLogical
		rows[i].BytesAlloc = byRel[r].BytesAlloc
	}
	for i := len(rels) - 1; i > 0; i-- {
		p := id[parent(rels[i])] - 1
		rows[p].Files += rows[i].Files
		rows[p].BytesLogical += rows[i].BytesLogical
		rows[p].BytesAlloc += rows[i].BytesAlloc
		rows[p].Dirs += rows[i].Dirs + 1
	}

	// Cubes: each directory's cube is the sum of its subtree's own cells.
	cubes := make([]cube.Acc, len(rels))
	for i, r := range rels {
		for c, v := range byRel[r].Cells {
			cubes[i].Add(c, v[0], v[1])
		}
	}
	for i := len(rels) - 1; i > 0; i-- {
		cubes[id[parent(rels[i])]-1].Merge(&cubes[i])
	}

	path := gen.Path(dataDir, rootID, scanID)
	w, err := gen.Create(ctx, path)
	if err != nil {
		tb.Fatalf("gentest: %v", err)
	}
	for _, row := range rows {
		if err := w.AddDir(ctx, row); err != nil {
			tb.Fatalf("gentest: %v", err)
		}
	}
	for i, r := range rels {
		b, err := cubes[i].Encode()
		if err != nil {
			tb.Fatalf("gentest: %v", err)
		}
		if len(b) > 1 {
			if err := w.AddCube(ctx, id[r], b); err != nil {
				tb.Fatalf("gentest: %v", err)
			}
		}
	}
	for _, r := range rels {
		d := byRel[r]
		for _, a := range opts.TopAnchors {
			if a != "" && r != a && !strings.HasPrefix(r, a+"/") {
				continue // an anchor lists only files below itself
			}
			for _, tf := range d.Top {
				err := w.AddTopFile(ctx, gen.TopFile{
					AnchorID: id[a], Kind: tf.Kind, Rank: int64(tf.Rank), ParentDirID: id[r],
					Name: []byte(tf.Name), Size: tf.Size, Alloc: tf.Alloc, Mtime: tf.Mtime,
				})
				if err != nil {
					tb.Fatalf("gentest: %v", err)
				}
			}
		}
		exts := d.Exts
		if slices.Contains(opts.ExtAnchors, r) {
			exts = subtreeExts(byRel, r)
		}
		for _, e := range exts {
			e.DirID = id[r]
			if err := w.AddTopExt(ctx, e); err != nil {
				tb.Fatalf("gentest: %v", err)
			}
		}
		for _, e := range d.Errors {
			e.DirID = id[r]
			if err := w.AddError(ctx, e); err != nil {
				tb.Fatalf("gentest: %v", err)
			}
		}
	}
	if err := w.BuildIndexes(ctx); err != nil {
		tb.Fatalf("gentest: %v", err)
	}
	if err := w.NumberPrePost(ctx); err != nil {
		tb.Fatalf("gentest: %v", err)
	}
	meta := gen.Meta{
		ScanID: scanID, RootID: rootID, ScannerVersion: "gentest",
		StartedAt: time.Unix(1_700_000_000, 0).UTC(), FinishedAt: time.Unix(1_700_000_100, 0).UTC(),
		Stats: json.RawMessage("{}"),
	}
	if err := w.Finish(ctx, meta); err != nil {
		tb.Fatalf("gentest: %v", err)
	}
	return path
}

// subtreeExts sums the Exts of anchor and every directory below it per
// extension, largest first (ties by extension), cut to maxExts.
func subtreeExts(byRel map[string]Dir, anchor string) []gen.TopExt {
	sum := map[string]gen.TopExt{}
	for r, d := range byRel {
		if anchor != "" && r != anchor && !strings.HasPrefix(r, anchor+"/") {
			continue
		}
		for _, e := range d.Exts {
			s := sum[e.Ext]
			s.Ext, s.Bytes, s.Files = e.Ext, s.Bytes+e.Bytes, s.Files+e.Files
			sum[e.Ext] = s
		}
	}
	out := slices.Collect(maps.Values(sum))
	slices.SortFunc(out, func(a, b gen.TopExt) int {
		if a.Bytes != b.Bytes {
			return cmp.Compare(b.Bytes, a.Bytes)
		}
		return strings.Compare(a.Ext, b.Ext)
	})
	return out[:min(len(out), maxExts)]
}

func parent(rel string) string {
	if i := strings.LastIndexByte(rel, '/'); i >= 0 {
		return rel[:i]
	}
	return ""
}

func base(rel string) string {
	if i := strings.LastIndexByte(rel, '/'); i >= 0 {
		return rel[i+1:]
	}
	return rel
}
