// Package agg holds the scanner's per-directory accumulators: subtree counters,
// the category cube, bounded top-N heaps and extension totals. It is
// pure: it does no I/O and imports neither fsx nor x/sys/unix, so it builds and
// tests on every platform.
package agg
