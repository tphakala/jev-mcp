package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/tphakala/jev-mcp/internal/doctor"
)

// doctorCommand runs the read-only preflight checks. It parses its own flag set
// so "jev-mcp doctor -probe" works, maps a flag error to exitUsage, and
// otherwise returns doctor.Run's exit code (1 if a check failed, else 0). The
// checks read the process environment directly through os.Getenv.
func doctorCommand(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	var probe bool
	fs := flag.NewFlagSet("jev-mcp doctor", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.BoolVar(&probe, "probe", false, "make one live decision call per provider (spends a small amount)")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if fs.NArg() != 0 {
		_, _ = fmt.Fprintf(stderr, "unexpected argument %q\n", fs.Arg(0))
		fs.Usage()
		return exitUsage
	}
	return doctor.Run(ctx, stdout, os.Getenv, probe, nil)
}
