package walk

import (
	"fmt"
	"io/fs"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// treeInfo describes what makeTree created inside the root.
type treeInfo struct {
	root     string
	files    int64 // regular files, hard links included
	symlinks int64
}

// openFDs counts the entries of /proc/self/fd (the listing's own descriptor
// included, so before and after compare like for like).
func openFDs(t *testing.T) int {
	t.Helper()
	ents, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		t.Fatal(err)
	}
	return len(ents)
}

func writeFile(t *testing.T, path string, size int) {
	t.Helper()
	if size < 64<<10 {
		if err := os.WriteFile(path, make([]byte, size), 0o600); err != nil {
			t.Fatal(err)
		}
		return
	}
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(path, int64(size)); err != nil { // sparse: Blocks != Size/512
		t.Fatal(err)
	}
}

// makeTree builds 4 levels of directories with random file sizes, a hard link,
// a symlink to a file, a symlink to a directory outside the root, a symlink
// loop and hostile names.
func makeTree(t *testing.T, seed uint64) treeInfo {
	t.Helper()
	rng := rand.New(rand.NewPCG(seed, seed^0x9e3779b97f4a7c15)) //nolint:gosec // seeded on purpose: reproducible test trees
	root := t.TempDir()
	info := treeInfo{root: root}
	var dirs, files []string

	var build func(dir string, level int)
	build = func(dir string, level int) {
		dirs = append(dirs, dir)
		for i := range rng.IntN(5) {
			p := filepath.Join(dir, fmt.Sprintf("f%d", i))
			writeFile(t, p, rng.IntN(3<<20+1))
			info.files++
			files = append(files, p)
		}
		if level == 4 {
			return
		}
		for i := range 3 {
			sub := filepath.Join(dir, fmt.Sprintf("d%d", i))
			if err := os.Mkdir(sub, 0o750); err != nil {
				t.Fatal(err)
			}
			build(sub, level+1)
		}
	}
	build(root, 1)

	// A hard link across directories.
	if len(files) == 0 {
		t.Fatal("seed produced no files")
	}
	if err := os.Link(files[0], filepath.Join(dirs[len(dirs)-1], "hardlink")); err != nil {
		t.Fatal(err)
	}
	info.files++

	// Symlinks that must not be followed.
	if err := os.Symlink(files[0], filepath.Join(root, "link-to-file")); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	for i := range 10 {
		writeFile(t, filepath.Join(outside, fmt.Sprintf("o%d", i)), 100)
	}
	if err := os.Symlink(outside, filepath.Join(root, "link-to-outside")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("b", filepath.Join(root, "a")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("a", filepath.Join(root, "b")); err != nil {
		t.Fatal(err)
	}
	info.symlinks = 4

	// Hostile names; U+202E is spelled as bytes to keep lint quiet.
	for _, name := range []string{"a\nb", "\xff\xfe", "\xe2\x80\xaeevil", "=cmd", strings.Repeat("x", 255)} {
		writeFile(t, filepath.Join(root, name), 10)
		info.files++
	}
	return info
}

// makeDirs builds n directories, two levels deep, with a file in some.
func makeDirs(t *testing.T, n int) string {
	t.Helper()
	root := t.TempDir()
	for i := range n / 50 {
		p := filepath.Join(root, fmt.Sprintf("p%03d", i))
		if err := os.Mkdir(p, 0o750); err != nil {
			t.Fatal(err)
		}
		for j := range 49 {
			c := filepath.Join(p, fmt.Sprintf("c%02d", j))
			if err := os.Mkdir(c, 0o750); err != nil {
				t.Fatal(err)
			}
			if j%7 == 0 {
				writeFile(t, filepath.Join(c, "f"), 10)
			}
		}
	}
	return root
}

// oracle counts root with filepath.WalkDir and os.Lstat under Scan's rules.
func oracle(t *testing.T, root string) Stats {
	t.Helper()
	var s Stats
	err := filepath.WalkDir(root, func(p string, de fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if de.IsDir() {
			s.Dirs++
			return nil
		}
		fi, err := os.Lstat(p)
		if err != nil {
			return err
		}
		s.Files++
		if fi.Mode()&fs.ModeSymlink != 0 {
			s.Symlinks++
		}
		s.BytesLogical += fi.Size()
		s.BytesAlloc += fi.Sys().(*syscall.Stat_t).Blocks * 512
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// blocksOf returns the 512-byte blocks allocated to fi.
func blocksOf(fi fs.FileInfo) int64 { return fi.Sys().(*syscall.Stat_t).Blocks }
