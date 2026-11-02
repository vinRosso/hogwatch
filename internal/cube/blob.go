package cube

import (
	"encoding/binary"
	"fmt"
	"math"
)

// Blob is the canonical sparse encoding of an Acc: version byte 0x01, then per
// non-zero cell in ascending order uvarint(index-prev-1), uvarint(bytes),
// uvarint(files), with prev starting at -1.
type Blob []byte

const blobVersion = 0x01

// Encode returns the canonical blob. It fails if any cell is negative.
func (a *Acc) Encode() (Blob, error) {
	out := []byte{blobVersion}
	prev := -1
	for i := range a {
		c := a[i]
		if c.Bytes < 0 || c.Files < 0 {
			return nil, fmt.Errorf("cube: negative cell %d (bytes %d, files %d)", i, c.Bytes, c.Files)
		}
		if c.Bytes == 0 && c.Files == 0 {
			continue
		}
		out = binary.AppendUvarint(out, uint64(i-prev-1)) //nolint:gosec // i > prev
		out = binary.AppendUvarint(out, uint64(c.Bytes))  //nolint:gosec // checked >= 0
		out = binary.AppendUvarint(out, uint64(c.Files))  //nolint:gosec // checked >= 0
		prev = i
	}
	return out, nil
}

// uvarint reads one minimally-encoded uvarint.
func uvarint(b []byte) (uint64, []byte, bool) {
	v, n := binary.Uvarint(b)
	if n <= 0 {
		return 0, nil, false // truncated or overflow
	}
	if n > 1 && b[n-1] == 0 {
		return 0, nil, false // non-minimal
	}
	return v, b[n:], true
}

// Decode parses a blob strictly; every violation yields ErrCorrupt.
func Decode(b Blob) (Acc, error) {
	var a Acc
	if len(b) == 0 || b[0] != blobVersion {
		return Acc{}, ErrCorrupt
	}
	rest := []byte(b[1:])
	prev := -1
	for len(rest) > 0 {
		var delta, by, fi uint64
		var ok bool
		if delta, rest, ok = uvarint(rest); !ok || delta >= NumCells {
			return Acc{}, ErrCorrupt
		}
		idx := prev + 1 + int(delta) //nolint:gosec // delta < NumCells
		if idx >= NumCells {
			return Acc{}, ErrCorrupt
		}
		if by, rest, ok = uvarint(rest); !ok || by > math.MaxInt64 {
			return Acc{}, ErrCorrupt
		}
		if fi, rest, ok = uvarint(rest); !ok || fi > math.MaxInt64 {
			return Acc{}, ErrCorrupt
		}
		if by == 0 && fi == 0 {
			return Acc{}, ErrCorrupt
		}
		a[idx].Bytes, a[idx].Files = int64(by), int64(fi) //nolint:gosec // checked <= MaxInt64
		prev = idx
	}
	return a, nil
}
