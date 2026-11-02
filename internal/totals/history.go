package totals

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/binary"
	"fmt"

	"github.com/cespare/xxhash/v2"

	"github.com/vinRosso/hogwatch/internal/gen"
)

// PathHash is the stable identity of a directory across generations: xxhash64 of the root id as 8 bytes big-endian followed by the raw
// bytes of the root-relative path, bit-cast to int64. The root's path is
// empty.
func PathHash(rootID int64, rel []byte) int64 {
	var h xxhash.Digest
	h.Reset()
	var b [8]byte
	binary.BigEndian.PutUint64(b[:], uint64(rootID)) //nolint:gosec // G115: bit-cast, wrap-around is intended
	_, _ = h.Write(b[:])
	_, _ = h.Write(rel)
	return int64(h.Sum64()) //nolint:gosec // G115: bit-cast to the stored int64
}

// HistoryDir is the history row of one directory that carries top lists.
// Rel holds raw bytes joined by "/" and is empty for the scan root.
type HistoryDir struct {
	PathHash          int64
	Rel               []byte
	BytesAlloc, Files int64
}

// HistoryDirs returns one row per distinct top_files.anchor_id of a
// generation, ordered by directory id. The path hash uses the root id the
// generation was built for (its meta row).
func HistoryDirs(ctx context.Context, genDB *sql.DB) ([]HistoryDir, error) {
	var rootID int64
	if err := genDB.QueryRowContext(ctx, `SELECT root_id FROM meta WHERE id = 1`).Scan(&rootID); err != nil {
		return nil, fmt.Errorf("totals: history dirs: meta: %w", err)
	}
	rs, err := genDB.QueryContext(ctx, `SELECT DISTINCT t.anchor_id, d.bytes_alloc, d.files
		FROM top_files t JOIN dirs d ON d.id = t.anchor_id ORDER BY t.anchor_id`)
	if err != nil {
		return nil, fmt.Errorf("totals: history dirs: %w", err)
	}
	type anchor struct{ id, alloc, files int64 }
	var as []anchor
	for rs.Next() {
		var a anchor
		if err := rs.Scan(&a.id, &a.alloc, &a.files); err != nil {
			_ = rs.Close()
			return nil, fmt.Errorf("totals: history dirs: %w", err)
		}
		as = append(as, a)
	}
	if err := rs.Err(); err != nil {
		_ = rs.Close()
		return nil, fmt.Errorf("totals: history dirs: %w", err)
	}
	_ = rs.Close()

	out := make([]HistoryDir, 0, len(as))
	for _, a := range as {
		segs, err := gen.DirPath(ctx, genDB, a.id)
		if err != nil {
			return nil, fmt.Errorf("totals: history dir %d: %w", a.id, err)
		}
		rel := bytes.Join(segs, []byte("/"))
		out = append(out, HistoryDir{PathHash: PathHash(rootID, rel), Rel: rel, BytesAlloc: a.alloc, Files: a.files})
	}
	return out, nil
}
