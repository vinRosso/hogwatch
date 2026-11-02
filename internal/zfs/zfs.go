// Package zfs parses the zfs.tsv file written by the hogwatch-zfs sidecar.
// It only reads that one file; it never talks to ZFS.
package zfs

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"strconv"
	"strings"
)

// Dataset is one dataset line of zfs.tsv. Sizes are bytes; "-" and "none"
// in the file are 0.
type Dataset struct {
	Name, Mountpoint                            string
	Used, Avail, Quota, Refquota, UsedSnapshots int64
}

const fields = 7

// Parse reads `name mountpoint used avail quota refquota usedbysnapshots`
// lines separated by tabs (zfs list -Hp). Snapshot lines (name holds "@") and
// blank lines are skipped. A line with the wrong number of fields or a bad
// number is an error naming the line.
func Parse(r io.Reader) ([]Dataset, error) {
	var out []Dataset
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimRight(sc.Text(), "\r")
		if line == "" {
			continue
		}
		f := strings.Split(line, "\t")
		if len(f) != fields {
			return nil, fmt.Errorf("zfs: line %d: want %d tab-separated fields, got %d", n, fields, len(f))
		}
		if strings.Contains(f[0], "@") {
			continue
		}
		d := Dataset{Name: f[0], Mountpoint: f[1]}
		for i, dst := range []*int64{&d.Used, &d.Avail, &d.Quota, &d.Refquota, &d.UsedSnapshots} {
			v, err := number(f[2+i])
			if err != nil {
				return nil, fmt.Errorf("zfs: line %d: field %d: %w", n, 3+i, err)
			}
			*dst = v
		}
		out = append(out, d)
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("zfs: read: %w", err)
	}
	return out, nil
}

func number(s string) (int64, error) {
	if s == "-" || s == "none" {
		return 0, nil
	}
	v, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("bad number %q", s)
	}
	return v, nil
}

// Load parses the file at path. A missing file is not an error: the sidecar
// is optional, and Load then returns nil, nil.
func Load(path string) ([]Dataset, error) {
	f, err := os.Open(path) //nolint:gosec // G304: the caller names the zfs.tsv path from configuration
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("zfs: %w", err)
	}
	defer func() { _ = f.Close() }()
	return Parse(f)
}

// ByMountpoint indexes datasets by mountpoint. Datasets without an absolute
// mountpoint ("none", "legacy", "-") are left out. If two share a mountpoint
// the later line wins.
func ByMountpoint(ds []Dataset) map[string]Dataset {
	m := make(map[string]Dataset, len(ds))
	for _, d := range ds {
		if strings.HasPrefix(d.Mountpoint, "/") {
			m[d.Mountpoint] = d
		}
	}
	return m
}

// EffectiveQuota is the smaller nonzero of Quota and Refquota, or nil when
// both are 0 (no quota).
func (d Dataset) EffectiveQuota() *int64 {
	var q int64
	switch {
	case d.Quota > 0 && d.Refquota > 0:
		q = min(d.Quota, d.Refquota)
	case d.Quota > 0:
		q = d.Quota
	case d.Refquota > 0:
		q = d.Refquota
	default:
		return nil
	}
	return &q
}
