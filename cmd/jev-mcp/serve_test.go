package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/tphakala/jev-mcp/internal/config"
)

// envMap returns a getenv over a fixed map, so serve tests do not depend on
// the host environment and can run in parallel.
func envMap(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

// noLookup fails the test if a host name lookup is attempted.
func noLookup(t *testing.T) func(string) ([]string, error) {
	t.Helper()
	return func(host string) ([]string, error) {
		t.Errorf("unexpected lookup of %q", host)
		return nil, errors.New("no lookup in this test")
	}
}

// syncBuffer is a strings.Builder safe for the concurrent writes a running
// server makes to stderr while the test reads it.
type syncBuffer struct {
	mu sync.Mutex
	b  strings.Builder
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func TestServeCommandStartupErrors(t *testing.T) {
	t.Parallel()

	withKey := map[string]string{config.EnvTypeSafeKey: "ts-key"}
	tests := []struct {
		name       string
		env        map[string]string
		opts       serveOptions
		lookup     func(string) ([]string, error)
		wantStderr []string
	}{
		{
			name:       "no api key",
			env:        map[string]string{},
			wantStderr: []string{"no API key", "jev-mcp doctor"},
		},
		{
			name:       "invalid provider",
			env:        map[string]string{config.EnvProvider: "bogus", config.EnvTypeSafeKey: "k"},
			wantStderr: []string{"JEV_MCP_PROVIDER", "jev-mcp doctor"},
		},
		{
			name:       "unparsable timeout",
			env:        map[string]string{config.EnvTypeSafeKey: "k", config.EnvTimeout: "soon"},
			wantStderr: []string{"jev-mcp doctor"},
		},
		{
			name:       "all interfaces",
			env:        withKey,
			opts:       serveOptions{httpAddr: ":8765"},
			wantStderr: []string{"binds all interfaces"},
		},
		{
			name:       "non-loopback ip",
			env:        withKey,
			opts:       serveOptions{httpAddr: "0.0.0.0:8765"},
			wantStderr: []string{"not loopback"},
		},
		{
			name: "host resolving off loopback",
			env:  withKey,
			opts: serveOptions{httpAddr: "localhost:8765"},
			lookup: func(string) ([]string, error) {
				return []string{"127.0.0.1", "192.0.2.10"}, nil
			},
			wantStderr: []string{"non-loopback address 192.0.2.10"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			lookup := tt.lookup
			if lookup == nil {
				lookup = noLookup(t)
			}
			var stdout, stderr strings.Builder
			code := serveCommand(t.Context(), tt.opts, envMap(tt.env), lookup, io.NopCloser(strings.NewReader("")), &stdout, &stderr)
			if code != exitError {
				t.Fatalf("exit = %d, want %d (stderr: %s)", code, exitError, stderr.String())
			}
			for _, want := range tt.wantStderr {
				if !strings.Contains(stderr.String(), want) {
					t.Errorf("stderr %q does not contain %q", stderr.String(), want)
				}
			}
			if stdout.Len() != 0 {
				t.Errorf("stdout = %q, want nothing", stdout.String())
			}
		})
	}
}

// TestServeCommandStdio drives a full stdio session through serveCommand:
// initialize and list tools over pipes, then hang up. The protocol stream is
// the only thing on stdout; the startup log line goes to stderr.
func TestServeCommandStdio(t *testing.T) {
	t.Parallel()

	serverIn, clientOut := io.Pipe()
	clientIn, serverOut := io.Pipe()
	var stderr syncBuffer
	env := envMap(map[string]string{config.EnvTypeSafeKey: "ts-key"})
	done := make(chan int, 1)
	go func() {
		done <- serveCommand(t.Context(), serveOptions{logLevel: slog.LevelInfo}, env, noLookup(t), serverIn, serverOut, &stderr)
		_ = serverOut.Close()
	}()

	client := mcp.NewClient(&mcp.Implementation{Name: "t", Version: "0"}, nil)
	cs, err := client.Connect(t.Context(), &mcp.IOTransport{Reader: clientIn, Writer: clientOut}, nil)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	tools, err := cs.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatalf("list tools: %v", err)
	}
	if len(tools.Tools) != 1 || tools.Tools[0].Name != "jev_evaluate" {
		t.Fatalf("tools = %v, want jev_evaluate", tools.Tools)
	}
	_ = cs.Close()

	select {
	case code := <-done:
		if code != exitOK {
			t.Fatalf("exit = %d, want %d (stderr: %s)", code, exitOK, stderr.String())
		}
	case <-time.After(10 * time.Second):
		t.Fatal("serveCommand did not return after the client hung up")
	}
	if got := stderr.String(); !strings.Contains(got, logMsgServeStdio) {
		t.Errorf("stderr %q does not contain the startup log line", got)
	}
	if strings.Contains(stderr.String(), "ts-key") {
		t.Error("the API key reached the log")
	}
}

// TestServeCommandStdioCancel checks that a cancelled context (SIGTERM) ends an
// idle stdio session with exit 0.
func TestServeCommandStdioCancel(t *testing.T) {
	t.Parallel()

	serverIn, clientOut := io.Pipe()
	t.Cleanup(func() { _ = clientOut.Close() })
	ctx, cancel := context.WithCancel(t.Context())
	env := envMap(map[string]string{config.EnvTypeSafeKey: "ts-key"})
	done := make(chan int, 1)
	var stderr syncBuffer
	go func() {
		done <- serveCommand(ctx, serveOptions{}, env, noLookup(t), serverIn, io.Discard, &stderr)
	}()
	cancel()
	select {
	case code := <-done:
		if code != exitOK {
			t.Fatalf("exit = %d, want %d (stderr: %s)", code, exitOK, stderr.String())
		}
	case <-time.After(10 * time.Second):
		t.Fatal("serveCommand did not return after cancel")
	}
}

// TestServeCommandHTTPToken checks which bearer token HTTP mode enforces: the
// environment's by default, the flag's when given, and none when the flag is
// given empty.
func TestServeCommandHTTPToken(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		opts     serveOptions
		authz    string
		want401  bool
		wantAuth string
	}{
		{name: "env token enforced", opts: serveOptions{}, want401: true, wantAuth: "auth=true"},
		{name: "env token accepted", opts: serveOptions{}, authz: "Bearer env-tok", wantAuth: "auth=true"},
		{name: "flag token overrides env", opts: serveOptions{httpToken: "flag-tok", httpTokenSet: true}, authz: "Bearer env-tok", want401: true, wantAuth: "auth=true"},
		{name: "empty flag disables auth", opts: serveOptions{httpTokenSet: true}, wantAuth: "auth=false"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			addr := freeLoopbackAddr(t)
			tt.opts.httpAddr = addr
			env := envMap(map[string]string{config.EnvTypeSafeKey: "ts-key", config.EnvHTTPToken: "env-tok"})
			ctx, cancel := context.WithCancel(t.Context())
			var stderr syncBuffer
			done := make(chan int, 1)
			go func() {
				done <- serveCommand(ctx, tt.opts, env, noLookup(t), io.NopCloser(strings.NewReader("")), io.Discard, &stderr)
			}()

			status := waitForStatus(t, "http://"+addr, tt.authz)
			cancel()
			if code := <-done; code != exitOK {
				t.Fatalf("exit = %d, want %d (stderr: %s)", code, exitOK, stderr.String())
			}
			if got401 := status == http.StatusUnauthorized; got401 != tt.want401 {
				t.Errorf("status = %d, want401 = %v", status, tt.want401)
			}
			if !strings.Contains(stderr.String(), tt.wantAuth) {
				t.Errorf("startup log %q does not contain %q", stderr.String(), tt.wantAuth)
			}
			if got := stderr.String(); strings.Contains(got, "env-tok") || strings.Contains(got, "flag-tok") {
				t.Error("the HTTP token reached the log")
			}
		})
	}
}

// waitForStatus posts to url until the server answers, and returns the status.
func waitForStatus(t *testing.T, url, authz string) int {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, url, strings.NewReader(`{}`))
		if err != nil {
			t.Fatalf("new request: %v", err)
		}
		req.Header.Set("Content-Type", "application/json")
		if authz != "" {
			req.Header.Set("Authorization", authz)
		}
		resp, err := http.DefaultClient.Do(req)
		if err == nil {
			_ = resp.Body.Close()
			return resp.StatusCode
		}
		if time.Now().After(deadline) {
			t.Fatalf("server never answered: %v", err)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// freeLoopbackAddr returns a loopback address with a port that was free a
// moment ago.
func freeLoopbackAddr(t *testing.T) string {
	t.Helper()
	ln, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := ln.Addr().String()
	if err := ln.Close(); err != nil {
		t.Fatalf("close listener: %v", err)
	}
	return addr
}

func TestCheckLoopbackAddrResolved(t *testing.T) {
	t.Parallel()

	resolveTo := func(addrs ...string) func(string) ([]string, error) {
		return func(string) ([]string, error) { return addrs, nil }
	}
	failLookup := func(string) ([]string, error) { return nil, errors.New("no such host") }

	tests := []struct {
		name    string
		addr    string
		lookup  func(string) ([]string, error)
		wantErr string // empty means accepted
	}{
		{name: "ipv4 loopback", addr: "127.0.0.1:8765"},
		{name: "other 127/8", addr: "127.0.0.2:8765"},
		{name: "ipv6 loopback", addr: "[::1]:8765"},
		{name: "bracketed ipv6 without port", addr: "[::1]"},
		{name: "ip without port", addr: "127.0.0.1"},
		{name: "localhost", addr: "localhost:8765", lookup: resolveTo("127.0.0.1", "::1")},
		{name: "empty host", addr: ":8765", wantErr: "binds all interfaces"},
		{name: "unspecified ipv4", addr: "0.0.0.0:8765", wantErr: "not loopback"},
		{name: "unspecified ipv6", addr: "[::]:8765", wantErr: "not loopback"},
		{name: "routable ip", addr: "192.0.2.1:8765", wantErr: "not loopback"},
		{name: "host resolving off loopback", addr: "localhost:8765", lookup: resolveTo("192.0.2.1"), wantErr: "non-loopback"},
		{name: "host resolving to nothing", addr: "localhost:8765", lookup: resolveTo(), wantErr: "no addresses"},
		{name: "lookup failure", addr: "nohost:8765", lookup: failLookup, wantErr: "no such host"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			lookup := tt.lookup
			if lookup == nil {
				lookup = noLookup(t)
			}
			err := checkLoopbackAddrResolved(tt.addr, lookup)
			switch {
			case tt.wantErr == "" && err != nil:
				t.Fatalf("checkLoopbackAddrResolved(%q) = %v, want nil", tt.addr, err)
			case tt.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tt.wantErr)):
				t.Fatalf("checkLoopbackAddrResolved(%q) = %v, want an error containing %q", tt.addr, err, tt.wantErr)
			}
		})
	}
}

func TestParseFlagsServeOptions(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		args []string
		want serveOptions
	}{
		{name: "defaults", args: nil, want: serveOptions{logLevel: slog.LevelInfo}},
		{name: "http and token", args: []string{"-http", "127.0.0.1:1", "-http-token", "x"}, want: serveOptions{httpAddr: "127.0.0.1:1", httpToken: "x", httpTokenSet: true, logLevel: slog.LevelInfo}},
		{name: "empty token is still set", args: []string{"-http-token", ""}, want: serveOptions{httpTokenSet: true, logLevel: slog.LevelInfo}},
		{name: "log flags", args: []string{"-log-level", "debug", "-log-json"}, want: serveOptions{logLevel: slog.LevelDebug, logJSON: true}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var stderr strings.Builder
			_, got, err := parseFlags(tt.args, &stderr)
			if err != nil {
				t.Fatalf("parseFlags(%q) = %v (stderr: %s)", tt.args, err, stderr.String())
			}
			if got != tt.want {
				t.Fatalf("parseFlags(%q) = %+v, want %+v", tt.args, got, tt.want)
			}
		})
	}
}
