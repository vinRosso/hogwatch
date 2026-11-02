// Package totals computes per-user usage from a finished generation and joins
// it to ZFS dataset numbers. It reads the generation through database/sql only and imports neither fsx nor
// golang.org/x/sys/unix (invariant 1), so the web can reuse it to recompute
// usage later.
package totals
