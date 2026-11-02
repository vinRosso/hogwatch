package fsx

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"slices"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

// Mount is one line of /proc/self/mountinfo, reduced to what the scanner needs.
type Mount struct {
	Dev        uint64 // st_dev of the mounted filesystem
	FSType     string
	MountPoint string
	ReadOnly   bool // "ro" among the per-mount options (field 6)
}

// ReadMounts parses /proc/self/mountinfo.
func ReadMounts() ([]Mount, error) {
	f, err := os.Open("/proc/self/mountinfo")
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	return ParseMountinfo(f)
}

// ParseMountinfo parses mountinfo(5) lines: the device is the third field, the
// mount point the fifth (octal escapes decoded), and the fstype follows the
// " - " separator that ends the variable-length optional fields.
func ParseMountinfo(r io.Reader) ([]Mount, error) {
	var out []Mount
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
	for n := 1; sc.Scan(); n++ {
		line := sc.Text()
		if strings.TrimSpace(line) == "" {
			continue
		}
		f := strings.Fields(line)
		if len(f) < 7 {
			return nil, fmt.Errorf("mountinfo line %d: %d fields, want at least 7", n, len(f))
		}
		sep := -1
		for i := 6; i < len(f); i++ {
			if f[i] == "-" {
				sep = i
				break
			}
		}
		if sep < 0 || sep+1 >= len(f) {
			return nil, fmt.Errorf("mountinfo line %d: no optional-fields separator and fstype", n)
		}
		maj, min, ok := strings.Cut(f[2], ":")
		if !ok {
			return nil, fmt.Errorf("mountinfo line %d: bad device %q", n, f[2])
		}
		major, err1 := strconv.ParseUint(maj, 10, 32)
		minor, err2 := strconv.ParseUint(min, 10, 32)
		if err1 != nil || err2 != nil {
			return nil, fmt.Errorf("mountinfo line %d: bad device %q", n, f[2])
		}
		out = append(out, Mount{
			Dev:        unix.Mkdev(uint32(major), uint32(minor)),
			FSType:     f[sep+1],
			MountPoint: unescapeOctal(f[4]),
			ReadOnly:   slices.Contains(strings.Split(f[5], ","), "ro"),
		})
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// unescapeOctal decodes the \NNN escapes the kernel uses for space, tab,
// newline and backslash in mountinfo paths.
func unescapeOctal(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+3 < len(s) {
			if v, err := strconv.ParseUint(s[i+1:i+4], 8, 8); err == nil {
				b.WriteByte(byte(v))
				i += 3
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}
