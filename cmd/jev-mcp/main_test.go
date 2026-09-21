package main

import (
	"context"
	"strings"
	"testing"

	jevmcp "github.com/tphakala/jev-mcp"
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
		{name: "no command", args: nil, wantCode: exitUsage, wantStderr: "no command given"},
		{name: "unknown flag", args: []string{"-bogus"}, wantCode: exitUsage, wantStderr: "flag provided but not defined: -bogus"},
		{name: "positional argument", args: []string{"extra"}, wantCode: exitUsage, wantStderr: `unexpected argument "extra"`},
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

func TestVersionStringStartsWithVersion(t *testing.T) {
	t.Parallel()

	if got, want := versionString(), "jev-mcp "+jevmcp.Version; !strings.HasPrefix(got, want) {
		t.Fatalf("versionString() = %q, want prefix %q", got, want)
	}
}
