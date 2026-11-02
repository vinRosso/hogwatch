// Package fsx holds the fd-relative, no-follow filesystem primitives the
// scanner builds on. It is the only package that makes host-filesystem
// syscalls (invariants 1 and 4). The implementation is Linux-only.
package fsx
