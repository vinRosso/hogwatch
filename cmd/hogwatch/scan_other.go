//go:build !linux

package main

import (
	"context"
	"fmt"
	"io"
)

func scan(_ context.Context, _ []string, _, stderr io.Writer) int {
	_, _ = fmt.Fprintln(stderr, "hogwatch scan: Linux only")
	return 1
}
