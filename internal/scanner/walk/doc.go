// Package walk traverses a directory tree through the fsx primitives. It never
// follows a symlink and keeps going past per-entry errors. The implementation
// is Linux-only.
package walk
