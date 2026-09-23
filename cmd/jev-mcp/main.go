// Command jev-mcp is an MCP server that exposes TypeSafe's Jev decision model
// to MCP clients. main only wires the process into run, which takes its
// arguments and output streams as parameters so tests can drive it without a
// subprocess.
//
// With no subcommand it serves the jev_evaluate tool over stdio, or over
// loopback Streamable HTTP with -http. "jev-mcp doctor" runs the read-only
// preflight checks, and -version prints the version.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
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
	// stdout is the JSON-RPC stream in stdio mode. The standard logger already
	// writes to stderr; this reasserts it in case a dependency redirected it.
	log.SetOutput(os.Stderr)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := run(ctx, os.Args[1:], os.Stdout, os.Stderr)
	// Not deferred: os.Exit skips deferred calls, so stop would never run.
	stop()
	os.Exit(code)
}

// run executes the command and returns the process exit code. The arguments,
// output streams, and cancellation arrive through the parameters so the tests
// can call it directly. For serve, the environment, stdin, and host name
// resolution are read from the process here and handed to serveCommand as
// parameters, which is the level the serve tests drive.
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

	showVersion, opts, err := parseFlags(args, stderr)
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

	return serveCommand(ctx, opts, os.Getenv, net.LookupHost, os.Stdin, stdout, stderr)
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

// usageHeader is printed above the flag list by -h and on a usage error.
const usageHeader = `Usage:
  jev-mcp [flags]           serve MCP over stdio, or over HTTP with -http
  jev-mcp doctor [-probe]   check the configuration and providers

Flags:
`

// parseFlags parses args and reports whether -version was requested, plus the
// serve options. On a usage error the flag package has already printed the
// message and usage to stderr, so parseFlags returns errUsage and the caller
// only has to map it to an exit code.
func parseFlags(args []string, stderr io.Writer) (bool, serveOptions, error) {
	var showVersion bool
	fs := flag.NewFlagSet("jev-mcp", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.BoolVar(&showVersion, "version", false, "print the version and exit")
	serveOpts := registerServeFlags(fs)
	fs.Usage = func() {
		_, _ = fmt.Fprint(stderr, usageHeader)
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return false, serveOptions{}, errUsage
	}
	if rejectPositional(fs, stderr) {
		return false, serveOptions{}, errUsage
	}
	return showVersion, serveOpts(), nil
}

// rejectPositional reports whether fs was left with a positional argument
// after parsing, which no command accepts. When it was, the argument and the
// usage have been written to stderr.
func rejectPositional(fs *flag.FlagSet, stderr io.Writer) bool {
	if fs.NArg() == 0 {
		return false
	}
	_, _ = fmt.Fprintf(stderr, "unexpected argument %q\n", fs.Arg(0))
	fs.Usage()
	return true
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
