//go:build !linux

package main

import (
	"context"
	"fmt"
	"io"
)

func bench(_ context.Context, _ []string, _, stderr io.Writer) int {
	_, _ = fmt.Fprintln(stderr, "hogwatch bench: Linux only")
	return 1
}
