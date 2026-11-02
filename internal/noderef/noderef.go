// Package noderef parses and formats client-visible node references of the
// form "<scan_id>.<dir_id>". Clients never reference nodes by path.
package noderef

import (
	"errors"
	"strconv"
	"strings"
)

// ErrInvalid is returned by Parse for any malformed reference.
var ErrInvalid = errors.New("noderef: invalid node reference")

// Ref identifies a directory node within one scan generation.
type Ref struct{ ScanID, DirID int64 }

// String formats r as "<scan_id>.<dir_id>".
func (r Ref) String() string {
	return strconv.FormatInt(r.ScanID, 10) + "." + strconv.FormatInt(r.DirID, 10)
}

// Parse accepts only canonical references: two positive decimal int64 values
// in ASCII digits without sign, leading zeros or whitespace.
func Parse(s string) (Ref, error) {
	a, b, ok := strings.Cut(s, ".")
	if !ok {
		return Ref{}, ErrInvalid
	}
	scan, ok1 := parsePositive(a)
	dir, ok2 := parsePositive(b)
	if !ok1 || !ok2 {
		return Ref{}, ErrInvalid
	}
	return Ref{ScanID: scan, DirID: dir}, nil
}

func parsePositive(s string) (int64, bool) {
	if s == "" || s[0] == '0' {
		return 0, false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return 0, false
		}
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0, false
	}
	return n, true
}
