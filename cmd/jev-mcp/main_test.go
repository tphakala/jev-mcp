package main

import (
	"context"
	"strings"
	"testing"

	jevmcp "github.com/tphakala/jev-mcp"
	"github.com/tphakala/jev-mcp/internal/config"
)

func TestRun(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		args       []string
		wantCode   int
		wantStdout string
		wantStderr string
	}{
		{name: "version", args: []string{"-version"}, wantCode: exitOK, wantStdout: "jev-mcp " + jevmcp.Version},
		{name: "bad log level", args: []string{"-log-level", "loud"}, wantCode: exitUsage, wantStderr: "invalid value"},
		{name: "unknown flag", args: []string{"-bogus"}, wantCode: exitUsage, wantStderr: "flag provided but not defined: -bogus"},
		{name: "positional argument", args: []string{"extra"}, wantCode: exitUsage, wantStderr: `unexpected argument "extra"`},
		// These reach doctorCommand and fail in its flag parsing, before any
		// environment is read, so they are deterministic and parallel-safe.
		{name: "doctor unknown flag", args: []string{"doctor", "-bogus"}, wantCode: exitUsage, wantStderr: "flag provided but not defined: -bogus"},
		{name: "doctor positional", args: []string{"doctor", "extra"}, wantCode: exitUsage, wantStderr: `unexpected argument "extra"`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var stdout, stderr strings.Builder
			code := run(t.Context(), tt.args, &stdout, &stderr)
			if code != tt.wantCode {
				t.Fatalf("run(%q) exit = %d, want %d (stderr: %s)", tt.args, code, tt.wantCode, stderr.String())
			}
			if !strings.HasPrefix(stdout.String(), tt.wantStdout) {
				t.Fatalf("run(%q) stdout = %q, want prefix %q", tt.args, stdout.String(), tt.wantStdout)
			}
			if !strings.Contains(stderr.String(), tt.wantStderr) {
				t.Fatalf("run(%q) stderr = %q, want it to contain %q", tt.args, stderr.String(), tt.wantStderr)
			}
		})
	}
}

func TestRunCancelledContext(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	var stdout, stderr strings.Builder
	if code := run(ctx, []string{"-version"}, &stdout, &stderr); code != exitError {
		t.Fatalf("run with cancelled context exit = %d, want %d", code, exitError)
	}
	if !strings.Contains(stderr.String(), "context canceled") {
		t.Fatalf("stderr = %q, want the cancellation to be reported", stderr.String())
	}
	if stdout.String() != "" {
		t.Fatalf("stdout = %q, want no output when the context is already cancelled", stdout.String())
	}
}

// TestRunDoctorDispatch confirms the "doctor" subcommand routes to doctor.Run
// and returns its exit code. It sets a controlled environment (a single
// explicit provider with its key) so the outcome does not depend on the host's
// own environment; t.Setenv forbids t.Parallel, so this test is serial.
func TestRunDoctorDispatch(t *testing.T) {
	t.Setenv(config.EnvProvider, "typesafe")
	t.Setenv(config.EnvTypeSafeKey, "ts-key")

	var stdout, stderr strings.Builder
	code := run(t.Context(), []string{"doctor"}, &stdout, &stderr)
	if code != exitOK {
		t.Fatalf("run(doctor) exit = %d, want %d (stderr: %s)", code, exitOK, stderr.String())
	}
	if got := stdout.String(); !strings.Contains(got, "[PASS] providers") {
		t.Fatalf("run(doctor) did not reach the doctor report:\n%s", got)
	}
}

// TestRunDoctorPropagatesFailure confirms a failing doctor.Run exit code flows
// out through run("doctor"). An explicit provider with an empty key fails
// selection, so doctor exits 1. Serial: t.Setenv forbids t.Parallel.
func TestRunDoctorPropagatesFailure(t *testing.T) {
	t.Setenv(config.EnvProvider, "typesafe")
	t.Setenv(config.EnvTypeSafeKey, "")

	var stdout, stderr strings.Builder
	code := run(t.Context(), []string{"doctor"}, &stdout, &stderr)
	if code != exitError {
		t.Fatalf("run(doctor) exit = %d, want %d (stdout: %s)", code, exitError, stdout.String())
	}
	if got := stdout.String(); !strings.Contains(got, "[FAIL] providers") {
		t.Fatalf("run(doctor) should report a provider failure:\n%s", got)
	}
}

func TestVersionStringStartsWithVersion(t *testing.T) {
	t.Parallel()

	if got, want := versionString(), "jev-mcp "+jevmcp.Version; !strings.HasPrefix(got, want) {
		t.Fatalf("versionString() = %q, want prefix %q", got, want)
	}
}
