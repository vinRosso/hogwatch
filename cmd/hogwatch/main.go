// Command hogwatch is the Hogwatch disk usage analyzer: one binary whose
// subcommands run the web server, the scanner and helper tools.
package main

import (
	"context"
	"fmt"
	"io"
	"os"
	_ "time/tzdata" // robfig/cron evaluates CRON_TZ schedules; distroless has no zoneinfo
)

// version and commit are set at build time with -ldflags -X main.version=...
// (see deploy/Dockerfile). A plain go build reports "dev".
var (
	version = "dev"
	commit  = ""
)

type command struct {
	name    string
	summary string
	fn      func(ctx context.Context, args []string, stdout, stderr io.Writer) int
}

func commands() []command {
	return []command{
		{"serve", "Run the web server", serve},
		{"scan", "Run the scanner", scan},
		{"bench", "Benchmark a directory walk (Phase 0)", bench},
		{"healthcheck", "Check the running process (container HEALTHCHECK)", healthcheck},
		{"version", "Print the build version", versionCmd},
		{"user", "Manage local users", user},
		{"init", "Prepare the state directories (first start)", initCmd},
	}
}

func versionCmd(_ context.Context, _ []string, stdout, _ io.Writer) int {
	if commit == "" {
		_, _ = fmt.Fprintln(stdout, "hogwatch", version)
	} else {
		_, _ = fmt.Fprintf(stdout, "hogwatch %s (%s)\n", version, commit)
	}
	return 0
}

func usage(w io.Writer) {
	_, _ = fmt.Fprintf(w, "Usage: hogwatch <command> [flags]\n\nCommands:\n")
	for _, c := range commands() {
		_, _ = fmt.Fprintf(w, "  %-12s %s\n", c.name, c.summary)
	}
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		usage(stderr)
		return 2
	}
	switch args[0] {
	case "help", "-h", "--help":
		usage(stdout)
		return 0
	}
	for _, c := range commands() {
		if c.name == args[0] {
			return c.fn(ctx, args[1:], stdout, stderr)
		}
	}
	_, _ = fmt.Fprintf(stderr, "hogwatch: unknown command %q\n\n", args[0])
	usage(stderr)
	return 2
}

func main() {
	os.Exit(run(context.Background(), os.Args[1:], os.Stdout, os.Stderr))
}
