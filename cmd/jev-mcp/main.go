// Command jev-mcp is an MCP server that exposes TypeSafe's Jev decision model
// to MCP clients. main only wires the process into run, which takes its inputs
// and outputs as parameters so tests can drive it without a subprocess.
//
// This build implements -version and the read-only doctor preflight command;
// the serve command and the MCP tool surface arrive in a later change.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"runtime/debug"
	"syscall"

	jevmcp "github.com/tphakala/jev-mcp"
)

// errUsage marks a command-line error: the usage text has already been written
// to stderr and the process should exit with exitUsage.
var errUsage = errors.New("usage error")

const (
	exitOK    = 0
	exitError = 1
	exitUsage = 2
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := run(ctx, os.Args[1:], os.Stdout, os.Stderr)
	// Not deferred: os.Exit skips deferred calls, so stop would never run.
	stop()
	os.Exit(code)
}

// run executes the command and returns the process exit code. Every dependency
// on the process (arguments, streams, signals) arrives through the parameters
// so the tests can call it directly.
func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	// Check for cancellation before doing any work so a caller that is already
	// shutting down gets a clean exit rather than partial output.
	if err := ctx.Err(); err != nil {
		_, _ = fmt.Fprintln(stderr, "error:", err)
		return exitError
	}

	// Subcommands are dispatched before flag parsing: the top-level flag set
	// rejects any positional argument, so "doctor" has to be intercepted here.
	if len(args) > 0 && args[0] == "doctor" {
		return doctorCommand(ctx, args[1:], stdout, stderr)
	}

	showVersion, err := parseFlags(args, stderr)
	if err != nil {
		if errors.Is(err, errUsage) {
			return exitUsage
		}
		// A diagnostic that cannot be written has nowhere else to go.
		_, _ = fmt.Fprintln(stderr, "error:", err)
		return exitError
	}
	if showVersion {
		return writeLine(stdout, stderr, versionString())
	}

	_, _ = fmt.Fprintln(stderr, "jev-mcp: no command given; run 'jev-mcp -version' or 'jev-mcp doctor'")
	return exitUsage
}

// writeLine writes the command's output line to stdout. The output is the
// product of the command, so a failed write (a closed pipe, a full disk) is
// reported on stderr and turned into a failing exit code rather than ignored.
func writeLine(stdout, stderr io.Writer, line string) int {
	if _, err := fmt.Fprintln(stdout, line); err != nil {
		_, _ = fmt.Fprintln(stderr, "error: writing output:", err)
		return exitError
	}
	return exitOK
}

// parseFlags parses args and reports whether -version was requested. On a usage
// error the flag package has already printed the message and usage to stderr,
// so parseFlags returns errUsage and the caller only has to map it to an exit
// code.
func parseFlags(args []string, stderr io.Writer) (bool, error) {
	var showVersion bool
	fs := flag.NewFlagSet("jev-mcp", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.BoolVar(&showVersion, "version", false, "print the version and exit")
	if err := fs.Parse(args); err != nil {
		return false, errUsage
	}
	if fs.NArg() != 0 {
		_, _ = fmt.Fprintf(stderr, "unexpected argument %q\n", fs.Arg(0))
		fs.Usage()
		return false, errUsage
	}
	return showVersion, nil
}

// versionString reports Version plus the VCS revision the toolchain embedded
// when it built the binary from a git checkout, when that is available.
func versionString() string {
	s := "jev-mcp " + jevmcp.Version
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return s
	}
	var revision, modified string
	for _, kv := range info.Settings {
		switch kv.Key {
		case "vcs.revision":
			revision = kv.Value
		case "vcs.modified":
			modified = kv.Value
		}
	}
	if revision == "" {
		return s
	}
	const shortSHA = 12
	if len(revision) > shortSHA {
		revision = revision[:shortSHA]
	}
	s += " (" + revision
	if modified == "true" {
		s += ", modified"
	}
	return s + ")"
}
