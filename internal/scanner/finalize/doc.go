// Package finalize holds the scanner's post-walk passes over a generation
// written through gen.Writer: today the deterministic hardlink re-attribution.
// It uses only data already in the generation file and never
// touches the filesystem.
package finalize
